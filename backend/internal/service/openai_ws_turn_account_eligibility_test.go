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

func (r *openAIWSTurnAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, r.err
}

func newOpenAIWSTurnEligibilityTestService(account *Account, cache *stubGatewayCache) (*OpenAIGatewayService, *openAIWSTurnAccountRepo) {
	repo := &openAIWSTurnAccountRepo{account: account}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	return &OpenAIGatewayService{accountRepo: repo, cache: cache, cfg: cfg}, repo
}

func TestOpenAIWSTurnAccountEligibilityRefreshesBoundAccount(t *testing.T) {
	groupID := int64(10)
	bound := &Account{ID: 50, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	current := *bound
	cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:session": bound.ID}}
	svc, repo := newOpenAIWSTurnEligibilityTestService(&current, cache)
	ctx := context.Background()

	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(ctx, bound, &groupID, "session", "gpt-5")
	require.Empty(t, reason)
	require.NoError(t, err)
	require.Equal(t, bound.ID, cache.sessionBindings["openai:session"])

	current.Status = StatusDisabled
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, bound, &groupID, "session", "gpt-5")
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	require.Equal(t, 1, cache.deletedSessions["openai:session"])

	cache.sessionBindings["openai:session"] = 999 // A newer binding must survive stale connection teardown.
	repo.err = ErrAccountNotFound
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, bound, &groupID, "session", "gpt-5")
	require.Equal(t, OpenAIWSTurnAccountIneligibleAccountMissing, reason)
	require.Error(t, err)
	require.Equal(t, int64(999), cache.sessionBindings["openai:session"])
	require.Equal(t, 1, cache.deletedSessions["openai:session"])
}

func TestOpenAIWSTurnAccountEligibilityKeepsConnectionOnRefreshFailure(t *testing.T) {
	bound := &Account{ID: 51, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	svc, repo := newOpenAIWSTurnEligibilityTestService(bound, nil)
	repo.err = errors.New("temporary account read failure")
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(context.Background(), bound, nil, "", "gpt-5")
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
	require.Equal(t, []string{"alias-fast", "provider-model"}, openAIWSTurnModelKeys(account, "alias-fast"))
	svc, _ := newOpenAIWSTurnEligibilityTestService(account, nil)
	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(context.Background(), account, nil, "", "alias-fast")
	require.Equal(t, OpenAIWSTurnAccountIneligibleModelRateLimited, reason)
	require.Error(t, err)
	require.False(t, account.isModelRateLimitedForFinalKeyWithContext(context.Background(), "unrelated-model"))

	delete(account.Extra, modelRateLimitsKey)
	state := svc.getOpenAIAccountModelTransientState()
	now := time.Now()
	state.recordFailure(account.ID, "provider-model", now)
	state.recordFailure(account.ID, "provider-model", now.Add(time.Millisecond))
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(context.Background(), account, nil, "", "alias-fast")
	require.Equal(t, OpenAIWSTurnAccountIneligibleRuntimeBlocked, reason)
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

	reason, err := svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, &groupID, "", "gpt-5.1")
	require.Empty(t, reason)
	require.NoError(t, err)
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, &groupID, "", "gpt-5.2")
	require.Equal(t, OpenAIWSTurnAccountIneligibleModelRateLimited, reason)
	require.Error(t, err)

	delete(account.Extra, modelRateLimitsKey)
	account.GroupIDs = []int64{11}
	reason, err = svc.EnforceOpenAIWSTurnAccountEligibility(ctx, account, &groupID, "", "gpt-5.1")
	require.Equal(t, OpenAIWSTurnAccountIneligibleNotSchedulable, reason)
	require.Error(t, err)
}
