package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	coderws "github.com/coder/websocket"
)

const (
	OpenAIWSTurnAccountIneligibleNotSchedulable   = "not_schedulable"
	OpenAIWSTurnAccountIneligibleModelRateLimited = "model_rate_limited"
	OpenAIWSTurnAccountIneligibleRuntimeBlocked   = "runtime_blocked"
	OpenAIWSTurnAccountIneligibleAccountMissing   = "account_missing"
)

// openAIWSTurnModelKeys covers the wire model of each fork WS ingress. The
// passthrough relay sends the client model unchanged; ctx_pool and HTTP bridge
// apply account mapping and upstream normalization. Runtime failures may also
// be recorded under the mapped model before normalization. Keys are never
// passed through model_mapping again during the eligibility check.
func openAIWSTurnModelKeys(account *Account, clientModel string) []string {
	clientModel = strings.TrimSpace(clientModel)
	if clientModel == "" {
		return nil
	}
	keys := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		keys = append(keys, model)
	}
	add(clientModel)
	if account != nil {
		mapped := account.GetMappedModel(clientModel)
		add(mapped)
		add(normalizeOpenAIModelForUpstream(account, mapped))
	}
	return keys
}

func (s *OpenAIGatewayService) openAIWSTurnAccountIneligibleReason(ctx context.Context, bound *Account, groupID *int64, clientModel string) string {
	if s == nil || bound == nil {
		return ""
	}
	account := bound
	if s.schedulerSnapshot != nil || s.accountRepo != nil {
		current, err := s.getSchedulableAccount(ctx, bound.ID)
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
	if paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, account); paused {
		return OpenAIWSTurnAccountIneligibleNotSchedulable
	}
	if s.isOpenAIAccountRuntimeBlocked(account) {
		return OpenAIWSTurnAccountIneligibleRuntimeBlocked
	}
	for _, key := range openAIWSTurnModelKeys(account, clientModel) {
		if account.isModelRateLimitedForFinalKeyWithContext(ctx, key) {
			return OpenAIWSTurnAccountIneligibleModelRateLimited
		}
	}
	for _, key := range openAIWSTurnModelKeys(account, clientModel) {
		if s.isOpenAIAccountModelRuntimeBlockedForFinalModel(account, key) {
			return OpenAIWSTurnAccountIneligibleRuntimeBlocked
		}
	}
	return ""
}

func (s *OpenAIGatewayService) releaseOpenAIWSTurnStickyBinding(ctx context.Context, groupID *int64, sessionHash string, accountID int64) {
	if s == nil || sessionHash == "" {
		return
	}
	boundID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash)
	if err != nil || boundID != accountID {
		return
	}
	if err := s.deleteStickySessionAccountID(ctx, groupID, sessionHash); err != nil {
		slog.Warn("openai_ws_turn_account_sticky_release_failed", "account_id", accountID, "error", err)
	}
}

// EnforceOpenAIWSTurnAccountEligibility runs after the next response.create
// has been read and before it is sent upstream. A rejected turn closes the
// bound connection so the client can reconnect and select another account.
// The caller invokes it only for turns after the handshake's first turn.
func (s *OpenAIGatewayService) EnforceOpenAIWSTurnAccountEligibility(ctx context.Context, account *Account, groupID *int64, sessionHash, clientModel string) (string, error) {
	if s == nil || account == nil {
		return "", nil
	}
	reason := s.openAIWSTurnAccountIneligibleReason(ctx, account, groupID, strings.TrimSpace(clientModel))
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
