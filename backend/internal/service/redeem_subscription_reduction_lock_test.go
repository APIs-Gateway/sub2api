package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func newReductionTestRedeemService(repo UserSubscriptionRepository) *RedeemService {
	return &RedeemService{subscriptionService: newAdjustTestService(repo)}
}

// 锁外读到的旧行只剩今天（扣 1 天就会整卡取消），但在拿锁前另一事务已续费 10 天。
// 必须按锁内最新行判定：缩短为 today+9 并保留续费写入的备注，而不是把刚续费的卡取消。
func TestRedeemReductionUsesLockedSubscription(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	stale := perDaySub(7, 11, today, SubscriptionStatusActive)
	stale.Notes = "old"
	current := perDaySub(7, 11, today+10, SubscriptionStatusActive)
	current.Notes = "renewed"
	repo := &lockingAdjustRepo{stale: stale, current: current}
	svc := newReductionTestRedeemService(repo)

	require.NoError(t, svc.reduceOrCancelSubscription(context.Background(), 11, 1, "minus-one-day"))

	require.Equal(t, 1, repo.lockReads)
	require.Zero(t, repo.closeCalls)
	require.Equal(t, 1, repo.shortenCalls)
	require.Equal(t, 1, repo.lastReduceDays)
	require.Equal(t, today+9, repo.current.ExpireDay)
	require.True(t, ExpireDayToExpiresAt(today+9).Equal(repo.current.ExpiresAt))
	require.Equal(t, SubscriptionStatusActive, repo.current.Status)
	require.Equal(t, "renewed\n通过兑换码 minus-one-day 退款扣减 1 天", repo.current.Notes)
}

func TestRedeemReductionLockFailureDoesNotWrite(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	sub := perDaySub(7, 11, today+10, SubscriptionStatusActive)
	sub.Notes = "unchanged"
	repo := &lockingAdjustRepo{stale: sub, current: sub, lockErr: errors.New("lock failed")}
	svc := newReductionTestRedeemService(repo)

	err := svc.reduceOrCancelSubscription(context.Background(), 11, 1, "deduct")

	require.ErrorIs(t, err, repo.lockErr)
	require.Zero(t, repo.writeCalls())
	require.Equal(t, sub, repo.current)
}

// 锁内发现卡已不再生效（并发取消 / 撤销删行）：与锁外未找到生效卡同口径返回
// ErrSubscriptionNotFound，由兑换事务整体回滚（兑换码不被消耗），且不对该行做任何写入。
func TestRedeemReductionLockedRowNoLongerActive(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	stale := perDaySub(7, 11, today+10, SubscriptionStatusActive)
	for _, tc := range []struct {
		name    string
		current UserSubscription
	}{
		{name: "cancelled concurrently", current: perDaySub(7, 11, today-1, SubscriptionStatusExpired)},
		{name: "revoked (row deleted)", current: perDaySub(99, 11, today+10, SubscriptionStatusActive)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &lockingAdjustRepo{stale: stale, current: tc.current}
			svc := newReductionTestRedeemService(repo)

			err := svc.reduceOrCancelSubscription(context.Background(), 11, 1, "deduct")

			require.ErrorIs(t, err, ErrSubscriptionNotFound)
			require.Zero(t, repo.writeCalls())
			require.Equal(t, tc.current, repo.current)
		})
	}
}
