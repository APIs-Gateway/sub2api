//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type resetAccountQuotaRepoStub struct {
	mockAccountRepoForGemini
	resetErr            error
	resetCalls          int
	clearRateLimitCalls int
	callOrder           []string
	overloaded          bool
}

func (r *resetAccountQuotaRepoStub) ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error {
	r.resetCalls++
	r.callOrder = append(r.callOrder, "reset_quota_and_clear_rate_limit_cooldown")
	return r.resetErr
}

func (r *resetAccountQuotaRepoStub) ClearRateLimit(context.Context, int64) error {
	r.clearRateLimitCalls++
	r.callOrder = append(r.callOrder, "clear_rate_limit")
	r.overloaded = false
	return nil
}

func TestResetAccountQuota_ClearsSchedulerRateLimitWithoutClearingOverload(t *testing.T) {
	repo := &resetAccountQuotaRepoStub{overloaded: true}
	svc := &adminServiceImpl{accountRepo: repo}

	err := svc.ResetAccountQuota(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, 1, repo.resetCalls)
	require.Zero(t, repo.clearRateLimitCalls)
	require.Equal(t, []string{"reset_quota_and_clear_rate_limit_cooldown"}, repo.callOrder)
	require.True(t, repo.overloaded, "quota reset must preserve an unrelated overload block")
}

func TestResetAccountQuota_PropagatesAtomicRepositoryFailure(t *testing.T) {
	resetErr := errors.New("atomic reset failed")
	repo := &resetAccountQuotaRepoStub{resetErr: resetErr}
	svc := &adminServiceImpl{accountRepo: repo}

	err := svc.ResetAccountQuota(context.Background(), 42)

	require.ErrorIs(t, err, resetErr)
	require.Equal(t, 1, repo.resetCalls)
	require.Zero(t, repo.clearRateLimitCalls)
}

func TestResetAccountQuota_MissingAccountReturnsNotFound(t *testing.T) {
	repo := &resetAccountQuotaRepoStub{resetErr: ErrAccountNotFound}
	svc := &adminServiceImpl{accountRepo: repo}

	err := svc.ResetAccountQuota(context.Background(), 404)

	require.ErrorIs(t, err, ErrAccountNotFound)
	require.Equal(t, 1, repo.resetCalls)
}

func TestResetAccountQuota_ClearsInProcessRuntimeBlockOnlyOnSuccess(t *testing.T) {
	t.Run("success clears runtime block", func(t *testing.T) {
		repo := &resetAccountQuotaRepoStub{}
		blocker := &runtimeBlockRecorder{}
		svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

		require.NoError(t, svc.ResetAccountQuota(context.Background(), 42))
		require.Equal(t, []int64{42}, blocker.clearedIDs)
		require.Empty(t, blocker.accounts)
	})

	t.Run("repository error keeps runtime block", func(t *testing.T) {
		repo := &resetAccountQuotaRepoStub{resetErr: errors.New("atomic reset failed")}
		blocker := &runtimeBlockRecorder{}
		svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

		require.Error(t, svc.ResetAccountQuota(context.Background(), 42))
		require.Empty(t, blocker.clearedIDs)
	})

	t.Run("missing account keeps runtime block", func(t *testing.T) {
		repo := &resetAccountQuotaRepoStub{resetErr: ErrAccountNotFound}
		blocker := &runtimeBlockRecorder{}
		svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

		require.ErrorIs(t, svc.ResetAccountQuota(context.Background(), 404), ErrAccountNotFound)
		require.Empty(t, blocker.clearedIDs)
	})

	t.Run("OpenAI gateway runtime block is lifted after reset", func(t *testing.T) {
		gateway := &OpenAIGatewayService{}
		account := &Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		gateway.BlockAccountScheduling(account, time.Now().Add(time.Hour), "429")
		require.True(t, gateway.isOpenAIAccountRuntimeBlocked(account))

		svc := &adminServiceImpl{accountRepo: &resetAccountQuotaRepoStub{}, runtimeBlocker: gateway}
		require.NoError(t, svc.ResetAccountQuota(context.Background(), account.ID))
		require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
	})
}
