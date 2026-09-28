package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	coderws "github.com/coder/websocket"
)

const (
	OpenAIWSTurnAccountIneligibleNotSchedulable   = "not_schedulable"
	OpenAIWSTurnAccountIneligibleModelRateLimited = "model_rate_limited"
	OpenAIWSTurnAccountIneligibleRuntimeBlocked   = "runtime_blocked"
	OpenAIWSTurnAccountIneligibleAccountMissing   = "account_missing"
)

// openAIWSTurnModelKeys returns only keys used by the selected ingress. A
// passthrough connection sends the client model unchanged; ctx_pool and HTTP
// bridge send the account-mapped and normalized model. Runtime cooldown may
// be recorded under the mapped key before normalization.
func openAIWSTurnModelKeys(account *Account, clientModel string, passthrough bool) (rateLimitKeys, runtimeKeys []string) {
	clientModel = strings.TrimSpace(clientModel)
	if clientModel == "" {
		return nil, nil
	}
	if passthrough || account == nil {
		return []string{clientModel}, []string{clientModel}
	}
	mapped := strings.TrimSpace(account.GetMappedModel(clientModel))
	if mapped == "" {
		mapped = clientModel
	}
	upstream := strings.TrimSpace(normalizeOpenAIModelForUpstream(account, mapped))
	if mapped == upstream {
		return []string{upstream}, []string{mapped}
	}
	// Model-not-found cooldown writes the pre-normalization mapped key, while
	// other WS failure paths can write the final upstream key.
	return []string{mapped, upstream}, []string{mapped, upstream}
}

func (s *OpenAIGatewayService) openAIWSTurnAccountIneligibleReason(ctx context.Context, bound *Account, groupID *int64, clientModel string, passthrough bool) string {
	if s == nil || bound == nil {
		return ""
	}
	account := bound
	if s.schedulerSnapshot != nil || s.accountRepo != nil {
		var current *Account
		var err error
		if s.accountRepo != nil {
			// The scheduler snapshot may still contain an account disabled in DB
			// until the outbox refresh runs. A long-lived WS must not rely on it.
			current, err = s.accountRepo.GetByID(ctx, bound.ID)
		} else {
			current, err = s.getSchedulableAccount(ctx, bound.ID)
		}
		switch {
		case errors.Is(err, ErrAccountNotFound):
			return OpenAIWSTurnAccountIneligibleAccountMissing
		case err != nil:
			// A transient snapshot or repository failure must not terminate all
			// active connections; the next turn rechecks state again.
			slog.Warn("openai_ws_turn_account_refresh_failed", "account_id", bound.ID, "error", err)
		case current == nil:
			return OpenAIWSTurnAccountIneligibleAccountMissing
		default:
			account = current
		}
	}
	if !account.IsSchedulable() || !s.openAIAccountMatchesSchedulingGroup(account, groupID) {
		return OpenAIWSTurnAccountIneligibleNotSchedulable
	}
	// The live WS keeps forwarding with its handshake account. If an admin
	// changes mapping or mode, reconnect so eligibility uses the same model
	// transformation as the next upstream write.
	if account.Platform != bound.Platform || account.Type != bound.Type || !reflect.DeepEqual(account.Credentials["model_mapping"], bound.Credentials["model_mapping"]) ||
		(s.cfg != nil && s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled && account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault) != bound.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault)) {
		return OpenAIWSTurnAccountIneligibleNotSchedulable
	}
	if paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, account); paused {
		return OpenAIWSTurnAccountIneligibleNotSchedulable
	}
	if clientModel != "" && !account.IsModelSupported(clientModel) {
		return OpenAIWSTurnAccountIneligibleNotSchedulable
	}
	if s.isOpenAIAccountRuntimeBlocked(account) {
		return OpenAIWSTurnAccountIneligibleRuntimeBlocked
	}
	rateLimitKeys, runtimeKeys := openAIWSTurnModelKeys(bound, clientModel, passthrough)
	for _, key := range rateLimitKeys {
		if account.isModelRateLimitedForFinalKeyWithContext(ctx, key) {
			return OpenAIWSTurnAccountIneligibleModelRateLimited
		}
	}
	for _, key := range runtimeKeys {
		if s.isOpenAIAccountModelRuntimeBlockedForFinalModel(account, key) {
			return OpenAIWSTurnAccountIneligibleRuntimeBlocked
		}
	}
	return ""
}

func (s *OpenAIGatewayService) releaseOpenAIWSTurnStickyBinding(ctx context.Context, groupID *int64, sessionHash string, accountID int64) {
	if s == nil || s.cache == nil || sessionHash == "" || accountID <= 0 {
		return
	}
	cache, ok := s.cache.(interface {
		CompareAndDeleteSessionAccountID(context.Context, int64, string, int64) (bool, error)
	})
	if !ok {
		// An unconditional delete can erase a newer connection's binding.
		slog.Warn("openai_ws_turn_sticky_compare_delete_unavailable", "account_id", accountID)
		return
	}
	keys := []string{s.openAISessionCacheKey(sessionHash)}
	if s.openAISessionHashReadOldFallbackEnabled() || s.openAISessionHashDualWriteOldEnabled() {
		if legacyKey := s.openAILegacySessionCacheKey(ctx, sessionHash); legacyKey != "" {
			keys = append(keys, legacyKey)
		}
	}
	for _, key := range keys {
		if _, err := cache.CompareAndDeleteSessionAccountID(ctx, derefGroupID(groupID), key, accountID); err != nil {
			slog.Warn("openai_ws_turn_account_sticky_release_failed", "account_id", accountID, "key", key, "error", err)
		}
	}
}

// EnforceOpenAIWSTurnAccountEligibility runs after the next response.create
// has been read and before it is sent upstream. A rejected turn closes the
// bound connection so the client can reconnect and select another account.
// The caller invokes it only for turns after the handshake's first turn.
func (s *OpenAIGatewayService) EnforceOpenAIWSTurnAccountEligibility(ctx context.Context, account *Account, groupID *int64, sessionHash, clientModel string, passthrough bool) (string, error) {
	if s == nil || account == nil {
		return "", nil
	}
	reason := s.openAIWSTurnAccountIneligibleReason(ctx, account, groupID, strings.TrimSpace(clientModel), passthrough)
	if reason == "" {
		return "", nil
	}
	s.releaseOpenAIWSTurnStickyBinding(ctx, groupID, sessionHash, account.ID)
	s.RecordOpenAIAccountSwitch()
	return reason, NewOpenAIWSClientCloseError(
		coderws.StatusTryAgainLater,
		fmt.Sprintf("account no longer schedulable (%s); please reconnect", reason),
		nil,
	)
}
