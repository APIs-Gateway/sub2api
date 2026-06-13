//go:build integration

package repository

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 这些测试针对 burn-down 收尾的「未花额度回收」三方法，使用全局 ent client
// （testEntClient）以走真实顶层事务（reclaimTx 自开 tx），用唯一邮箱隔离数据。

const reclaimEps = 1e-6

func reclaimSeedUser(t *testing.T, client *dbent.Client, balance float64) *dbent.User {
	t.Helper()
	u, err := client.User.Create().
		SetEmail(fmt.Sprintf("reclaim-%d-%d@test.com", time.Now().UnixNano(), reclaimSeq())).
		SetPasswordHash("x").
		SetStatus(service.StatusActive).
		SetRole(service.RoleUser).
		SetBalance(balance).
		Save(context.Background())
	require.NoError(t, err, "create user")
	return u
}

func reclaimSeedGroup(t *testing.T, client *dbent.Client) *dbent.Group {
	t.Helper()
	g, err := client.Group.Create().
		SetName(fmt.Sprintf("reclaim-g-%d-%d", time.Now().UnixNano(), reclaimSeq())).
		SetStatus(service.StatusActive).
		Save(context.Background())
	require.NoError(t, err, "create group")
	return g
}

func reclaimSeedSub(t *testing.T, client *dbent.Client, userID, groupID int64, granted, daily, consumed, clawed float64) *dbent.UserSubscription {
	t.Helper()
	now := time.Now()
	sub, err := client.UserSubscription.Create().
		SetUserID(userID).
		SetGroupID(groupID).
		SetStartsAt(now.Add(-1 * time.Hour)).
		SetExpiresAt(now.Add(240 * time.Hour)).
		SetStatus(service.SubscriptionStatusActive).
		SetAssignedAt(now).
		SetActivatedAt(now.Add(-1 * time.Hour)).
		SetGrantedTotalUsd(granted).
		SetDailyAmountUsd(daily).
		SetConsumedUsd(consumed).
		SetClawedUsd(clawed).
		SetNotes("").
		Save(context.Background())
	require.NoError(t, err, "create subscription")
	return sub
}

var (
	reclaimSeqN  uint64
	reclaimSeqMu sync.Mutex
)

func reclaimSeq() uint64 {
	reclaimSeqMu.Lock()
	defer reclaimSeqMu.Unlock()
	reclaimSeqN++
	return reclaimSeqN
}

func TestCloseSubscriptionWithReclaim_DeleteRevokesAndReclaims(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	u := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 100, 10, 30, 0) // remaining = 70

	uid, reclaimed, err := repo.CloseSubscriptionWithReclaim(ctx, sub.ID, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, u.ID, uid)
	require.InDelta(t, 70, reclaimed, reclaimEps)

	// 行已删除
	_, err = client.UserSubscription.Get(ctx, sub.ID)
	require.True(t, dbent.IsNotFound(err), "subscription row should be deleted")

	// 余额扣回 remaining
	gotUser, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 30, gotUser.Balance, reclaimEps)
}

func TestCloseSubscriptionWithReclaim_ExpireCancelsAndReclaims(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	u := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 100, 10, 30, 0) // remaining = 70

	now := time.Now()
	uid, reclaimed, err := repo.CloseSubscriptionWithReclaim(ctx, sub.ID, now, false)
	require.NoError(t, err)
	require.Equal(t, u.ID, uid)
	require.InDelta(t, 70, reclaimed, reclaimEps)

	gotSub, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotSub.Status)
	require.InDelta(t, 70, gotSub.ClawedUsd, reclaimEps) // 回收的 remaining 计入 clawed
	// remaining = granted - consumed - clawed = 100 - 30 - 70 = 0
	svc := userSubscriptionEntityToService(gotSub)
	require.InDelta(t, 0, svc.RemainingUSD(), reclaimEps)

	gotUser, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 30, gotUser.Balance, reclaimEps)
}

func TestShortenSubscriptionWithReclaim_PartialAndCappedByRemaining(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	// 普通缩短：remaining=80, reduceDays=3, D=10 → reclaim=30
	u1 := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub1 := reclaimSeedSub(t, client, u1.ID, g.ID, 100, 10, 20, 0) // remaining=80
	newExp := time.Now().Add(168 * time.Hour)

	_, reclaimed, err := repo.ShortenSubscriptionWithReclaim(ctx, sub1.ID, 3, newExp, time.Now())
	require.NoError(t, err)
	require.InDelta(t, 30, reclaimed, reclaimEps)

	gotSub1, err := client.UserSubscription.Get(ctx, sub1.ID)
	require.NoError(t, err)
	require.InDelta(t, 70, gotSub1.GrantedTotalUsd, reclaimEps) // 100 - 30
	gotUser1, err := client.User.Get(ctx, u1.ID)
	require.NoError(t, err)
	require.InDelta(t, 70, gotUser1.Balance, reclaimEps) // 100 - 30

	// 透支封顶：remaining=10, reduceDays=3, D=10 → cap=30 但 reclaim=min(10,30)=10
	u2 := reclaimSeedUser(t, client, 100)
	sub2 := reclaimSeedSub(t, client, u2.ID, g.ID, 100, 10, 90, 0) // remaining=10
	_, reclaimed2, err := repo.ShortenSubscriptionWithReclaim(ctx, sub2.ID, 3, newExp, time.Now())
	require.NoError(t, err)
	require.InDelta(t, 10, reclaimed2, reclaimEps)
	gotUser2, err := client.User.Get(ctx, u2.ID)
	require.NoError(t, err)
	require.InDelta(t, 90, gotUser2.Balance, reclaimEps) // 100 - 10
}

func TestGrantSubscriptionDays_AddsBalanceAndGranted(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	u := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 100, 10, 0, 0)
	newExp := time.Now().Add(360 * time.Hour)

	_, granted, err := repo.GrantSubscriptionDays(ctx, sub.ID, 5, newExp, time.Now())
	require.NoError(t, err)
	require.InDelta(t, 50, granted, reclaimEps) // 5 × 10

	gotSub, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.InDelta(t, 150, gotSub.GrantedTotalUsd, reclaimEps)
	gotUser, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 150, gotUser.Balance, reclaimEps)
}

func TestReclaim_ZeroDailyAmountIsBalanceSafe(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	// legacy/standard 订阅：D=0、granted=0 → 不动余额，仅改状态/到期。
	u := reclaimSeedUser(t, client, 42)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 0, 0, 0, 0)

	_, reclaimed, err := repo.CloseSubscriptionWithReclaim(ctx, sub.ID, time.Now(), false)
	require.NoError(t, err)
	require.InDelta(t, 0, reclaimed, reclaimEps)

	gotUser, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 42, gotUser.Balance, reclaimEps)
	gotSub, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotSub.Status)
}

func TestReclaim_NotFoundIsNoop(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	uid, reclaimed, err := repo.CloseSubscriptionWithReclaim(ctx, math.MaxInt32, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, uid)
	require.Zero(t, reclaimed)
}
