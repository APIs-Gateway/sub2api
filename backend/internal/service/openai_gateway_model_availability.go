package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// DiagnoseModelAvailabilityForPlatform reports whether the requested model
// is configured to be served by any persistently eligible account in the
// requested OpenAI-compatible platform. It ignores transient runtime state so
// rate limits, overloads, temporary unschedulability, and expiry windows stay
// on the 503 path.
//
// Safe to call on the error path: returns {true,true} on any internal
// failure or when the inputs preclude meaningful diagnosis (empty model,
// nil service), so callers stay on the 503 fallback branch.
func (s *OpenAIGatewayService) DiagnoseModelAvailabilityForPlatform(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil || s.accountRepo == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	accounts, err := s.listConfiguredOpenAIAccountsForModelDiagnosis(ctx, groupID, platform)
	if err != nil {
		// Conservative fallback so the caller keeps returning 503; we do not
		// want a transient lookup failure to flip into 404 model_not_found.
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{}
	for i := range accounts {
		diag.HasAccountsInPool = true
		// Mirrors the per-candidate filter used during account selection
		// (openai_account_scheduler.isAccountRequestCompatible): empty
		// model_mapping accepts everything; otherwise the explicit / wildcard
		// mapping must match.
		if accounts[i].IsModelSupported(requestedModel) {
			diag.HasModelSupport = true
			return diag
		}
	}
	return diag
}

func (s *OpenAIGatewayService) listConfiguredOpenAIAccountsForModelDiagnosis(ctx context.Context, groupID *int64, platform string) ([]Account, error) {
	var (
		accounts []Account
		err      error
	)
	simpleMode := s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
	switch {
	case simpleMode:
		// Simple-mode selection scans the whole platform pool, including
		// accounts that also have group bindings.
		accounts, err = s.accountRepo.ListByPlatform(ctx, platform)
	case groupID != nil:
		accounts, err = s.accountRepo.ListByGroup(ctx, *groupID)
	default:
		accounts, err = s.accountRepo.ListActive(ctx)
	}
	if err != nil {
		return nil, err
	}

	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if !isPersistentlyEligibleModelAvailabilityAccount(account) {
			continue
		}
		if !simpleMode && groupID == nil && len(account.AccountGroups) != 0 {
			continue
		}
		if account.Platform == platform {
			filtered = append(filtered, *account)
		}
	}
	return filtered, nil
}
