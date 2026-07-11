package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// ModelAvailabilityDiagnosis describes whether the requested model can be
// served by any active configured account in the group. Handlers use this on
// the "no available accounts" error path to distinguish 404 model_not_found
// from 503 service_unavailable.
type ModelAvailabilityDiagnosis struct {
	// HasAccountsInPool is true if the group has at least one active configured
	// account on the queried platform (or, for Anthropic/Gemini, on the
	// platform plus mixed-scheduled Antigravity accounts).
	HasAccountsInPool bool
	// HasModelSupport is true if at least one account's model mapping admits
	// the requested model.
	HasModelSupport bool
}

// ModelAvailabilityDiagnoser is implemented by gateway services that can
// report whether the requested model is configured to be served by any
// account. Both *GatewayService and *OpenAIGatewayService implement this so
// handlers in either package can share a single classifier.
type ModelAvailabilityDiagnoser interface {
	DiagnoseModelAvailabilityForPlatform(
		ctx context.Context,
		groupID *int64,
		requestedModel string,
		platform string,
	) ModelAvailabilityDiagnosis
}

// DiagnoseModelAvailabilityForPlatform inspects active configured accounts of
// the given platform and returns whether the requested model is configured to
// be served by any of them. It deliberately includes accounts that are
// temporarily unschedulable, rate-limited, or overloaded: those conditions
// should remain 503 because the account can serve the model after recovery.
//
// Safe to call on the error path: returns {true,true} on any internal failure
// or when the inputs preclude meaningful diagnosis (empty model, etc.), so
// callers stay on the 503 fallback branch.
func (s *GatewayService) DiagnoseModelAvailabilityForPlatform(
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
		// No model specified — cannot decide model_not_found. Caller falls back to 503.
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	if strings.TrimSpace(platform) == "" {
		// Without a platform we cannot scope the lookup; bail out to the
		// 503 branch rather than make an unscoped scan.
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	// Mirror the selection path exactly: a forced platform does not use mixed
	// scheduling, and it overrides the handler's protocol-default platform.
	forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
	if hasForcePlatform && strings.TrimSpace(forcePlatform) != "" {
		platform = forcePlatform
	} else {
		hasForcePlatform = false
	}
	accounts, err := s.listConfiguredAccountsForModelDiagnosis(ctx, groupID, platform, hasForcePlatform)
	if err != nil {
		// Conservative fallback: pretend everything is fine so the caller
		// returns 503 (we don't want to flip to 404 just because a lookup
		// hiccup'd).
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{}
	for i := range accounts {
		diag.HasAccountsInPool = true
		if s.isModelSupportedByAccountWithContext(ctx, &accounts[i], requestedModel) {
			diag.HasModelSupport = true
			return diag
		}
	}
	return diag
}

func (s *GatewayService) listConfiguredAccountsForModelDiagnosis(
	ctx context.Context,
	groupID *int64,
	platform string,
	hasForcePlatform bool,
) ([]Account, error) {
	var (
		accounts []Account
		err      error
	)
	switch {
	case s.cfg != nil && s.cfg.RunMode == config.RunModeSimple:
		accounts, err = s.accountRepo.ListByPlatform(ctx, platform)
	case groupID != nil:
		accounts, err = s.accountRepo.ListByGroup(ctx, *groupID)
	default:
		accounts, err = s.accountRepo.ListActive(ctx)
	}
	if err != nil {
		return nil, err
	}

	useMixed := (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform
	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if groupID == nil && !s.isAccountInGroup(account, nil) {
			continue
		}
		if !s.isAccountAllowedForPlatform(account, platform, useMixed) {
			continue
		}
		filtered = append(filtered, *account)
	}
	return filtered, nil
}
