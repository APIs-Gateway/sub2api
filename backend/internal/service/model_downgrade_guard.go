package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

const ModelDowngradeGuardReason = "model_downgrade"

// ModelDowngradeCounterCache counts confirmed upstream attempts across server
// instances. Losing Redis must never make a single failure quarantine an account.
type ModelDowngradeCounterCache interface {
	IncrementModelDowngradeCount(context.Context, int64, string, int) (int64, error)
	ResetModelDowngradeCount(context.Context, int64, string) error
}

type modelDowngradeBlocker interface {
	TryBlockDowngradedModel(context.Context, int64, string, time.Time, float64) (bool, error)
}

func (s *RateLimitService) SetModelDowngradeCounterCache(cache ModelDowngradeCounterCache) {
	s.modelDowngradeCounter = cache
}

// HandleConfirmedModelDowngrade is called once for an upstream attempt whose
// response was rejected before any semantic output reached the client. The
// fork's mismatch audit, failover, and zero-charge path remain the source of
// truth for that attempt; this guard only changes future account selection.
func (s *RateLimitService) HandleConfirmedModelDowngrade(ctx context.Context, account *Account, sentModel, responseModel string) {
	if s == nil || s.cfg == nil || account == nil || account.Platform != PlatformOpenAI ||
		account.ID <= 0 || s.modelDowngradeCounter == nil {
		return
	}
	guard := s.cfg.Gateway.ModelDowngradeGuard
	if !guard.Enabled {
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
	applied, err := blocker.TryBlockDowngradedModel(ctx, account.ID, sentModel, time.Now().Add(time.Duration(blockHours)*time.Hour), ratio)
	if err != nil {
		slog.Warn("model_downgrade_block_failed", "account_id", account.ID, "model", sentModel, "error", err)
		return
	}
	if applied {
		slog.Warn("model_downgrade_model_blocked", "account_id", account.ID, "model", sentModel, "hits", count)
	}
}
