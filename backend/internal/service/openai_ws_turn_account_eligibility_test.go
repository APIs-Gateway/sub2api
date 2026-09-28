package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type openAIWSTurnAccountRepo struct {
	AccountRepository
	account *Account
	err     error
}

type openAIWSTurnCompareDeleteCache struct {
	*stubGatewayCache
	beforeCompare func(string)
}

func (c *openAIWSTurnCompareDeleteCache) CompareAndDeleteSessionAccountID(_ context.Context, _ int64, sessionHash string, accountID int64) (bool, error) {
	if c.beforeCompare != nil {
		c.beforeCompare(sessionHash)
	}
	if c.sessionBindings[sessionHash] != accountID {
		return false, nil
	}
	delete(c.sessionBindings, sessionHash)
	if c.deletedSessions == nil {
		c.deletedSessions = make(map[string]int)
	}
	c.deletedSessions[sessionHash]++
	return true, nil
}

type openAIWSTurnStaleSnapshotCache struct {
	SchedulerCache
	account *Account
}

func (c *openAIWSTurnStaleSnapshotCache) GetAccount(context.Context, int64) (*Account, error) {
	return c.account, nil
}

func (r *openAIWSTurnAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, r.err
}

func newOpenAIWSTurnEligibilityTestService(account *Account, cache GatewayCache) (*OpenAIGatewayService, *openAIWSTurnAccountRepo) {
	repo := &openAIWSTurnAccountRepo{account: account}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	return &OpenAIGatewayService{accountRepo: repo, cache: cache, cfg: cfg}, repo
}

func TestOpenAIWSTurnAccountEligibilityRefreshesBoundAccount(t *testing.T) {
	groupID := int64(10)
	bound := &Account{ID: 50, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	current := *bound
	cache := &openAIWSTurnCompareDeleteCache{stubGatewayCache: &stubGatewayCache{sessionBindings: map[string]int64{"openai:session": bound.ID}}}
	svc, repo := newOpenAIWSTurnEligibilityTestService(&current, cache)
	ctx := context.Background()

	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(ctx, bound, &groupID, "session", "gpt-5", false)
	require.Empty(t, reason)
	require.NoError(t, err)
	require.Equal(t, bound.ID, cache.sessionBindings["openai:session"])

	current.Status = StatusDisabled
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, bound, &groupID, "session", "gpt-5", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	require.Equal(t, 1, cache.deletedSessions["openai:session"])

	cache.sessionBindings["openai:session"] = 999 // A newer binding must survive stale connection teardown.
	repo.err = ErrAccountNotFound
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, bound, &groupID, "session", "gpt-5", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleAccountMissing, reason)
	require.Error(t, err)
	require.Equal(t, int64(999), cache.sessionBindings["openai:session"])
	require.Equal(t, 1, cache.deletedSessions["openai:session"])
}

func TestOpenAIWSTurnStickyCompareDeletePreservesReplacementAcrossPrimaryAndLegacy(t *testing.T) {
	bound := &Account{ID: 60, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false}
	cache := &openAIWSTurnCompareDeleteCache{stubGatewayCache: &stubGatewayCache{sessionBindings: map[string]int64{
		"openai:session": bound.ID,
		"openai:legacy":  bound.ID,
	}}}
	cache.beforeCompare = func(key string) {
		cache.sessionBindings[key] = 61 // A new WS replaces both bindings before each atomic delete.
	}
	svc, _ := newOpenAIWSTurnEligibilityTestService(bound, cache)
	svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
	svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
	ctx := withOpenAILegacySessionHash(context.Background(), "legacy")
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(ctx, bound, nil, "session", "gpt-5", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	require.Error(t, err)
	require.Equal(t, int64(61), cache.sessionBindings["openai:session"])
	require.Equal(t, int64(61), cache.sessionBindings["openai:legacy"])
	require.Empty(t, cache.deletedSessions)
}

func TestOpenAIWSTurnStickyCompareDeleteRemovesOnlyOldLegacyBinding(t *testing.T) {
	bound := &Account{ID: 63, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false}
	cache := &openAIWSTurnCompareDeleteCache{stubGatewayCache: &stubGatewayCache{sessionBindings: map[string]int64{
		"openai:session": 64,
		"openai:legacy":  bound.ID,
	}}}
	svc, _ := newOpenAIWSTurnEligibilityTestService(bound, cache)
	svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
	ctx := withOpenAILegacySessionHash(context.Background(), "legacy")
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(ctx, bound, nil, "session", "gpt-5", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	require.Error(t, err)
	require.Equal(t, int64(64), cache.sessionBindings["openai:session"])
	require.NotContains(t, cache.sessionBindings, "openai:legacy")
	require.Equal(t, 1, cache.deletedSessions["openai:legacy"])
}

func TestOpenAIWSTurnAccountEligibilityUsesFreshDBWhenSnapshotIsStale(t *testing.T) {
	bound := &Account{ID: 62, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	current := *bound
	current.Schedulable = false
	svc, repo := newOpenAIWSTurnEligibilityTestService(&current, nil)
	svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAIWSTurnStaleSnapshotCache{account: bound}, accountRepo: repo}
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(context.Background(), bound, nil, "", "gpt-5", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	require.Error(t, err)
}

func TestOpenAIWSTurnAccountEligibilityKeepsConnectionOnRefreshFailure(t *testing.T) {
	bound := &Account{ID: 51, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	svc, repo := newOpenAIWSTurnEligibilityTestService(bound, nil)
	repo.err = errors.New("temporary account read failure")
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(context.Background(), bound, nil, "", "gpt-5", false)
	require.Empty(t, reason)
	require.NoError(t, err)
}

func TestOpenAIWSTurnAccountEligibilityUsesFinalModelKeysWithoutRemapping(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	account := &Account{
		ID: 52, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"alias-*": "provider-model", "provider-*": "other-model"}},
		Extra:       map[string]any{modelRateLimitsKey: map[string]any{"provider-model": map[string]any{"rate_limit_reset_at": resetAt}}},
	}
	rateKeys, runtimeKeys := openAIWSTurnModelKeys(account, "alias-fast", false)
	require.Equal(t, []string{"provider-model"}, rateKeys)
	require.Equal(t, []string{"provider-model"}, runtimeKeys)
	svc, _ := newOpenAIWSTurnEligibilityTestService(account, nil)
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(context.Background(), account, nil, "", "alias-fast", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleModelRateLimited, reason)
	require.Error(t, err)
	require.False(t, account.isModelRateLimitedForFinalKeyWithContext(context.Background(), "unrelated-model"))

	delete(account.Extra, modelRateLimitsKey)
	state := svc.getOpenAIAccountModelTransientState()
	now := time.Now()
	state.recordFailure(account.ID, "provider-model", now)
	state.recordFailure(account.ID, "provider-model", now.Add(time.Millisecond))
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(context.Background(), account, nil, "", "alias-fast", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleRuntimeBlocked, reason)
	require.Error(t, err)
}

func TestOpenAIWSTurnAccountEligibilityUsesSelectedIngressWireModel(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	account := &Account{
		ID: 54, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Extra: map[string]any{modelRateLimitsKey: map[string]any{
			"gpt-5.4": map[string]any{"rate_limit_reset_at": resetAt},
		}},
	}
	svc, _ := newOpenAIWSTurnEligibilityTestService(account, nil)
	ctx := context.Background()

	passthroughRate, passthroughRuntime := openAIWSTurnModelKeys(account, "gpt-5.1", true)
	require.Equal(t, []string{"gpt-5.1"}, passthroughRate)
	require.Equal(t, []string{"gpt-5.1"}, passthroughRuntime)
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, nil, "", "gpt-5.1", true)
	require.Empty(t, reason, "passthrough writes gpt-5.1, not normalized gpt-5.4")
	require.NoError(t, err)

	bridgeRate, bridgeRuntime := openAIWSTurnModelKeys(account, "gpt-5.1", false)
	require.Equal(t, []string{"gpt-5.1", "gpt-5.4"}, bridgeRate)
	require.Equal(t, []string{"gpt-5.1", "gpt-5.4"}, bridgeRuntime)
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, nil, "", "gpt-5.1", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleModelRateLimited, reason)
	require.Error(t, err)

	account.Extra[modelRateLimitsKey] = map[string]any{
		"gpt-5.1": map[string]any{"rate_limit_reset_at": resetAt},
	}
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, nil, "", "gpt-5.1", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleModelRateLimited, reason, "model-not-found cooldown uses the pre-normalization key")
	require.Error(t, err)
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, nil, "", "gpt-5.1", true)
	require.Equal(t, OpenAIWSTurnAccountIneligibleModelRateLimited, reason, "passthrough writes the original key")
	require.Error(t, err)
}

func TestOpenAIWSTurnAccountEligibilityReconnectsAfterMappingChange(t *testing.T) {
	bound := &Account{
		ID: 55, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"alias": "old-provider"}},
	}
	current := *bound
	current.Credentials = map[string]any{"model_mapping": map[string]any{"alias": "new-provider"}}
	svc, _ := newOpenAIWSTurnEligibilityTestService(&current, nil)
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(context.Background(), bound, nil, "", "alias", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	require.Error(t, err)
}

func TestOpenAIWSTurnAccountEligibilityChecksCurrentTurnModelAndGroup(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	groupID := int64(10)
	account := &Account{
		ID: 53, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
		Extra: map[string]any{modelRateLimitsKey: map[string]any{
			"gpt-5.2": map[string]any{"rate_limit_reset_at": resetAt},
		}},
	}
	svc, _ := newOpenAIWSTurnEligibilityTestService(account, nil)
	svc.cfg.RunMode = config.RunModeStandard
	ctx := context.Background()

	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, &groupID, "", "gpt-5.1", false)
	require.Empty(t, reason)
	require.NoError(t, err)
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, &groupID, "", "gpt-5.2", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleModelRateLimited, reason)
	require.Error(t, err)

	delete(account.Extra, modelRateLimitsKey)
	account.GroupIDs = []int64{11}
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, &groupID, "", "gpt-5.1", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	require.Error(t, err)
}

func TestOpenAIWSTurnAccountEligibilityRejectsUnsupportedSecondTurnModel(t *testing.T) {
	account := &Account{
		ID: 65, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.1": "provider-model"}},
	}
	svc, _ := newOpenAIWSTurnEligibilityTestService(account, nil)
	ctx := context.Background()
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, nil, "", "gpt-5.1", false)
	require.Empty(t, reason)
	require.NoError(t, err)
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, nil, "", "gpt-5.2", false)
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	require.Error(t, err)
}
