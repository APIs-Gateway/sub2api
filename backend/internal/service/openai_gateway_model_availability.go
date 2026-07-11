package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// DiagnoseModelAvailabilityForPlatform reports whether the requested model
// is configured to be served by any active OpenAI account in the group. The
// platform argument is accepted to satisfy ModelAvailabilityDiagnoser but
// is ignored — OpenAIGatewayService only scans OpenAI accounts.
//
// Safe to call on the error path: returns {true,true} on any internal
// failure or when the inputs preclude meaningful diagnosis (empty model,
// nil service), so callers stay on the 503 fallback branch.
func (s *OpenAIGatewayService) DiagnoseModelAvailabilityForPlatform(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	_ string,
) ModelAvailabilityDiagnosis {
	if s == nil || s.accountRepo == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	accounts, err := s.listConfiguredOpenAIAccountsForModelDiagnosis(ctx, groupID)
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

func (s *OpenAIGatewayService) listConfiguredOpenAIAccountsForModelDiagnosis(ctx context.Context, groupID *int64) ([]Account, error) {
	var (
		accounts []Account
		err      error
	)
	switch {
	case s.cfg != nil && s.cfg.RunMode == config.RunModeSimple:
		accounts, err = s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
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
		if groupID == nil && len(account.AccountGroups) != 0 {
			continue
		}
		if account.Platform == PlatformOpenAI {
			filtered = append(filtered, *account)
		}
	}
	return filtered, nil
}
