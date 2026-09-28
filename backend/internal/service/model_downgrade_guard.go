package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const ModelDowngradeGuardReason = "model_downgrade"

// ModelDowngradeCounterCache counts confirmed upstream attempts across server
// instances. Losing Redis must never make a single failure quarantine an account.
type ModelDowngradeCounterCache interface {
	IncrementModelDowngradeCount(context.Context, int64, string, int) (int64, error)
	ResetModelDowngradeCount(context.Context, int64, string) error
}

// ModelDowngradeCandidateFilter uses the same request-specific vetoes as the
// gateway selector before a pool member can count as an alternate route.
type ModelDowngradeCandidateFilter func(context.Context, *Account, *int64) bool

type modelDowngradeBlocker interface {
	TryBlockDowngradedModel(context.Context, int64, string, string, time.Time, float64, bool, ModelDowngradeCandidateFilter) (bool, error)
}

func (s *RateLimitService) SetModelDowngradeCounterCache(cache ModelDowngradeCounterCache) {
	s.modelDowngradeCounter = cache
}

// HandleConfirmedModelDowngrade is called once for an upstream attempt whose
// response was rejected before any semantic output reached the client. The
// fork's mismatch audit, failover, and zero-charge path remain the source of
// truth for that attempt; this guard only changes future account selection.
func (s *RateLimitService) HandleConfirmedModelDowngrade(ctx context.Context, account *Account, requestedModel, sentModel, responseModel string, candidateFilter ModelDowngradeCandidateFilter) {
	if s == nil || s.cfg == nil || account == nil || account.Platform != PlatformOpenAI ||
		account.ID <= 0 || s.modelDowngradeCounter == nil || candidateFilter == nil {
		return
	}
	guard := s.cfg.Gateway.ModelDowngradeGuard
	if !guard.Enabled {
		return
	}
	// Forced single-account routes have no alternate account even when their
	// group has healthy peers. Keep their cross-request routing untouched.
	if openAIForcedAccountRoutingID(ctx) == account.ID {
		return
	}
	sentModel = strings.TrimSpace(sentModel)
	responseModel = strings.TrimSpace(responseModel)
	if sentModel == "" || responseModel == "" || sentModel == responseModel {
		return
	}
	matched := false
	for _, pair := range guard.Pairs {
		if strings.EqualFold(strings.TrimSpace(pair.SentModel), sentModel) &&
			strings.EqualFold(strings.TrimSpace(pair.ResponseModel), responseModel) {
			matched = true
			break
		}
	}
	if !matched {
		return
	}
	threshold := guard.ThresholdCount
	if threshold < 2 {
		threshold = 5
	}
	window := guard.WindowMinutes
	if window < 1 || window > 24*60 {
		window = 30
	}
	blockHours := guard.BlockHours
	if blockHours < 1 || blockHours > 7*24 {
		blockHours = 24
	}
	ratio := guard.MaxBlockedRatio
	if ratio <= 0 || ratio > 0.5 {
		ratio = 0.3
	}
	count, err := s.modelDowngradeCounter.IncrementModelDowngradeCount(ctx, account.ID, sentModel, window)
	if err != nil {
		slog.Warn("model_downgrade_count_failed", "account_id", account.ID, "model", sentModel, "error", err)
		return
	}
	if count < int64(threshold) {
		return
	}
	// A failed reset only causes redundant block attempts. The repository
	// serializes ratio checks and refuses to overwrite any active limit.
	if err := s.modelDowngradeCounter.ResetModelDowngradeCount(ctx, account.ID, sentModel); err != nil {
		slog.Warn("model_downgrade_count_reset_failed", "account_id", account.ID, "model", sentModel, "error", err)
	}
	blocker, ok := s.accountRepo.(modelDowngradeBlocker)
	if !ok {
		return
	}
	applied, err := blocker.TryBlockDowngradedModel(ctx, account.ID, requestedModel, sentModel, time.Now().Add(time.Duration(blockHours)*time.Hour), ratio, s.cfg.RunMode == config.RunModeSimple, candidateFilter)
	if err != nil {
		slog.Warn("model_downgrade_block_failed", "account_id", account.ID, "model", sentModel, "error", err)
		return
	}
	if applied {
		slog.Warn("model_downgrade_model_blocked", "account_id", account.ID, "model", sentModel, "hits", count)
	}
}

func (s *OpenAIGatewayService) modelDowngradeCandidateFilter(ctx context.Context, requestedModel, path string, requiredTransport OpenAIUpstreamTransport) ModelDowngradeCandidateFilter {
	// Requiring native Responses support on this path is deliberately
	// conservative: ordinary Responses can fall back to raw Chat, but image
	// intent and compaction cannot. A raw-Chat-only account is not a safe
	// witness for every request sharing this upstream model.
	capability := OpenAIEndpointCapabilityChatCompletions
	if strings.Contains(path, "/responses") {
		capability = OpenAIEndpointCapabilityResponses
	}
	requireCompact := false
	selectionModel := requestedModel
	if forward, ok := openAIForwardModelFromContext(ctx); ok {
		requireCompact = forward.useCompactModelMapping
		if strings.TrimSpace(forward.model) != "" {
			selectionModel = forward.model
		}
	}
	return func(ctx context.Context, candidate *Account, groupID *int64) bool {
		if !isOpenAICompatibleAccountEligibleForRequest(ctx, candidate, PlatformOpenAI, selectionModel, requireCompact, capability) ||
			!s.isOpenAIAccountTransportCompatible(candidate, requiredTransport) ||
			s.isOpenAIAccountRequestRuntimeBlocked(candidate, selectionModel) ||
			s.isOpenAIProxyStreamQuarantined(ctx, candidate) {
			return false
		}
		if groupID != nil {
			var group *Group
			for _, linked := range candidate.Groups {
				if linked != nil && linked.ID == *groupID {
					group = linked
					break
				}
			}
			if group == nil || (group.RequirePrivacySet && !candidate.IsPrivacySet()) {
				return false
			}
		}
		if groupID != nil && s.channelService != nil {
			channel, err := s.channelService.GetChannelForGroup(ctx, *groupID)
			if err != nil {
				return false
			}
			if channel != nil && channel.RestrictModels && channel.BillingModelSource == BillingModelSourceUpstream &&
				s.isUpstreamModelRestrictedByChannel(ctx, *groupID, candidate, selectionModel, requireCompact) {
				return false
			}
		}
		return true
	}
}
