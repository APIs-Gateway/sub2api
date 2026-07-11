//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// modelAvailabilityAccountRepo makes the model-diagnosis tests exercise the
// active-account read path. It deliberately includes temporarily
// unschedulable accounts, unlike mockAccountRepoForPlatform's scheduler
// methods.
type modelAvailabilityAccountRepo struct {
	*mockAccountRepoForPlatform
	listByGroupErr error
	listActiveErr  error
}

func newModelAvailabilityAccountRepo(accounts []Account) *modelAvailabilityAccountRepo {
	byID := make(map[int64]*Account, len(accounts))
	for i := range accounts {
		byID[accounts[i].ID] = &accounts[i]
	}
	return &modelAvailabilityAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accounts:     accounts,
			accountsByID: byID,
		},
	}
}

func (m *modelAvailabilityAccountRepo) activeAccounts() []Account {
	accounts := make([]Account, 0, len(m.accounts))
	for i := range m.accounts {
		if m.accounts[i].Status == StatusActive {
			accounts = append(accounts, m.accounts[i])
		}
	}
	return accounts
}

func (m *modelAvailabilityAccountRepo) ListByGroup(context.Context, int64) ([]Account, error) {
	if m.listByGroupErr != nil {
		return nil, m.listByGroupErr
	}
	return m.activeAccounts(), nil
}

func (m *modelAvailabilityAccountRepo) ListActive(context.Context) ([]Account, error) {
	if m.listActiveErr != nil {
		return nil, m.listActiveErr
	}
	return m.activeAccounts(), nil
}

func (m *modelAvailabilityAccountRepo) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	accounts := m.activeAccounts()
	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		if accounts[i].Platform == platform {
			filtered = append(filtered, accounts[i])
		}
	}
	return filtered, nil
}

func TestDiagnoseModelAvailabilityForPlatform_NoModel_AlwaysAvailable(t *testing.T) {
	repo := newModelAvailabilityAccountRepo(nil)
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool, "empty model must return HasAccountsInPool=true so caller stays on 503")
	require.True(t, diag.HasModelSupport, "empty model must return HasModelSupport=true so caller stays on 503")
}

func TestDiagnoseModelAvailabilityForPlatform_EmptyPlatform_AlwaysAvailable(t *testing.T) {
	repo := newModelAvailabilityAccountRepo(nil)
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5", "")

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport, "empty platform must fall back to {true,true} so caller stays on 503")
}

func TestDiagnoseModelAvailabilityForPlatform_NilReceiver(t *testing.T) {
	var svc *GatewayService

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityForPlatform_NoAccountsInPool(t *testing.T) {
	repo := newModelAvailabilityAccountRepo(nil)
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5", PlatformOpenAI)

	require.False(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport, "no accounts means no support; caller stays on 503 (empty-pool branch)")
}

func TestDiagnoseModelAvailabilityForPlatform_SimpleModeUsesPlatformScopedPool(t *testing.T) {
	groupID := int64(7)
	cfg := testConfig()
	cfg.RunMode = config.RunModeSimple
	svc := &GatewayService{
		accountRepo: newModelAvailabilityAccountRepo([]Account{{
			ID:          1,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5": "gpt-5"}},
		}}),
		cfg: cfg,
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5-mini", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport, "simple mode must use the requested platform's configured pool")
}

func TestDiagnoseModelAvailabilityForPlatform_ExplicitMappingMatches(t *testing.T) {
	repo := newModelAvailabilityAccountRepo([]Account{
		{
			ID:          1,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"gpt-5.1-codex-mini": "gpt-5.1-codex-mini"},
			},
		},
	})
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5.1-codex-mini", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityForPlatform_EmptyMappingAllowsAll(t *testing.T) {
	repo := newModelAvailabilityAccountRepo([]Account{
		{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true /* no ModelMapping = allow all */},
	})
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5.1-codex-mini", PlatformOpenAI)

	require.True(t, diag.HasModelSupport, "empty model_mapping must be treated as 'allow all' (Account.IsModelSupported semantics)")
}

func TestDiagnoseModelAvailabilityForPlatform_WildcardMappingMatches(t *testing.T) {
	repo := newModelAvailabilityAccountRepo([]Account{
		{
			ID:          1,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"*": "gpt-5"},
			},
		},
	})
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5.1-codex-mini", PlatformOpenAI)

	require.True(t, diag.HasModelSupport, "wildcard mapping must classify the request as 'serviceable'")
}

func TestDiagnoseModelAvailabilityForPlatform_NoMatchingModel_ReturnsNotFoundSignal(t *testing.T) {
	repo := newModelAvailabilityAccountRepo([]Account{
		{
			ID:          1,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5": "gpt-5"}},
		},
		{
			ID:          2,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5-mini": "gpt-5-mini"}},
		},
	})
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5.1-codex-mini", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool, "group has OpenAI accounts")
	require.False(t, diag.HasModelSupport, "no account mapping admits the requested model — handler should return 404")
}

func TestDiagnoseModelAvailabilityForPlatform_TemporarilyUnschedulableSupportedAccountStays503(t *testing.T) {
	repo := newModelAvailabilityAccountRepo([]Account{
		{
			ID:          1,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5": "gpt-5"}},
		},
		{
			ID:          2,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: false,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5-mini": "gpt-5-mini"}},
		},
	})
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5-mini", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport, "a configured account that is temporarily unavailable must preserve the 503 path")
}

func TestDiagnoseModelAvailabilityForPlatform_WrongPlatformFiltersOut(t *testing.T) {
	// Group has only Anthropic accounts; user routes to OpenAI gateway.
	// Diagnosis must NOT see Anthropic accounts (listSchedulableAccounts filters
	// by platform), so HasAccountsInPool is false and the caller stays on 503.
	repo := newModelAvailabilityAccountRepo([]Account{
		{
			ID:          1,
			Platform:    PlatformAnthropic,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"}},
		},
	})
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5", PlatformOpenAI)

	require.False(t, diag.HasAccountsInPool, "OpenAI route must not see Anthropic accounts in pool")
	require.False(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityForPlatform_ForcePlatformDisablesMixedScheduling(t *testing.T) {
	// The gateway normally mixes Antigravity accounts into an Anthropic pool.
	// A forced platform must not do so: selection only considers the forced
	// platform, so diagnosis must use the same pool before it chooses 404/503.
	repo := newModelAvailabilityAccountRepo([]Account{
		{
			ID:          1,
			Platform:    PlatformAnthropic,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"claude-sonnet": "claude-sonnet"}},
		},
		{
			ID:          2,
			Platform:    PlatformAntigravity,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"mixed_scheduling": true,
				"model_mapping":    map[string]any{"claude-opus": "claude-opus"},
			},
		},
	})
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAnthropic)

	diag := svc.DiagnoseModelAvailabilityForPlatform(ctx, nil, "claude-opus", PlatformGemini)

	require.True(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport, "forced Anthropic routing must not borrow an Antigravity-only mapping")
}
