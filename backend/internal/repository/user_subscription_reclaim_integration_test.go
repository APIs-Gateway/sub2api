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

// per-day 重构后这三方法（Close/Shorten/Grant）只改卡的 expire_day/状态，**不再动 users.balance**
// （卡价值在 today_remaining、不在钱包；主动退款另走 payment_refund）。本组测试据此断言：
// 余额恒不变、expire_day 按自然日增减、Close 立即过期、延长按续费口径并 clamp 到上限。

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

// reclaimSeedSub 建一张 per-day 卡：固定 10 天服务窗口（expire_day = today+10），today_remaining=D。
// granted/consumed/clawed 仅为兼容旧列保留，新逻辑不读。
func reclaimSeedSub(t *testing.T, client *dbent.Client, userID, groupID int64, daily float64) *dbent.UserSubscription {
	t.Helper()
	now := time.Now()
	startDay := service.EastDayNumber(now)
	expireDay := startDay + 10
	sub, err := client.UserSubscription.Create().
		SetUserID(userID).
		SetGroupID(groupID).
		SetStartsAt(now.Add(-1 * time.Hour)).
		SetExpiresAt(service.ExpireDayToExpiresAt(expireDay)).
		SetStatus(service.SubscriptionStatusActive).
		SetAssignedAt(now).
		SetActivatedAt(now.Add(-1 * time.Hour)).
		SetDailyAmountUsd(daily).
		SetTodayRemaining(daily).
		SetTodayDay(startDay).
		SetStartDay(startDay).
		SetExpireDay(expireDay).
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

// 撤销（删除行）：不回收钱包，行被删，余额不变。
func TestCloseSubscriptionWithReclaim_DeleteRevokes_NoBalanceChange(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	u := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 10)

	uid, reclaimed, err := repo.CloseSubscriptionWithReclaim(ctx, sub.ID, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, u.ID, uid)
	require.InDelta(t, 0, reclaimed, reclaimEps, "per-day 不回收钱包")

	_, err = client.UserSubscription.Get(ctx, sub.ID)
	require.True(t, dbent.IsNotFound(err), "subscription row should be deleted")

	gotUser, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 100, gotUser.Balance, reclaimEps, "余额不应变动")
}

// 取消（不删行）：立即过期 status=expired + today_remaining=0 + expire_day<today；余额不变。
func TestCloseSubscriptionWithReclaim_ExpireImmediately_NoBalanceChange(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	u := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 10)

	now := time.Now()
	uid, reclaimed, err := repo.CloseSubscriptionWithReclaim(ctx, sub.ID, now, false)
	require.NoError(t, err)
	require.Equal(t, u.ID, uid)
	require.InDelta(t, 0, reclaimed, reclaimEps)

	gotSub, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotSub.Status)
	require.InDelta(t, 0, gotSub.TodayRemaining, reclaimEps, "today_remaining 清零")
	require.Less(t, gotSub.ExpireDay, service.EastDayNumber(now), "expire_day < today（立即过期）")

	gotUser, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 100, gotUser.Balance, reclaimEps, "余额不应变动")
}

// 缩短：expire_day −= reduceDays；余额不变；缩过头夹到 today−1（立即过期）。
func TestShortenSubscriptionWithReclaim_ReducesExpireDay_NoBalanceChange(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	now := time.Now()
	newExp := now.Add(168 * time.Hour) // 入参时间戳被忽略，expires_at 由 expire_day 派生

	// 普通缩短：expire_day 10天窗口 − 3 = today+7
	u1 := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub1 := reclaimSeedSub(t, client, u1.ID, g.ID, 10)
	startExpire := sub1.ExpireDay

	_, reclaimed, err := repo.ShortenSubscriptionWithReclaim(ctx, sub1.ID, 3, newExp, now)
	require.NoError(t, err)
	require.InDelta(t, 0, reclaimed, reclaimEps)

	gotSub1, err := client.UserSubscription.Get(ctx, sub1.ID)
	require.NoError(t, err)
	require.Equal(t, startExpire-3, gotSub1.ExpireDay, "expire_day −3")
	gotUser1, err := client.User.Get(ctx, u1.ID)
	require.NoError(t, err)
	require.InDelta(t, 100, gotUser1.Balance, reclaimEps, "余额不应变动")

	// 缩过头：reduceDays 远超窗口 → expire_day 夹到 today−1
	u2 := reclaimSeedUser(t, client, 100)
	sub2 := reclaimSeedSub(t, client, u2.ID, g.ID, 10)
	_, _, err = repo.ShortenSubscriptionWithReclaim(ctx, sub2.ID, 9999, newExp, now)
	require.NoError(t, err)
	gotSub2, err := client.UserSubscription.Get(ctx, sub2.ID)
	require.NoError(t, err)
	require.Equal(t, service.EastDayNumber(now)-1, gotSub2.ExpireDay, "缩过头夹到 today−1")
}

// 延长：expire_day = max(原, today−1) + addDays；余额不变。
func TestGrantSubscriptionDays_ExtendsExpireDay_NoBalanceChange(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	now := time.Now()
	u := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 10)
	startExpire := sub.ExpireDay

	_, granted, err := repo.GrantSubscriptionDays(ctx, sub.ID, 5, now.Add(360*time.Hour), now)
	require.NoError(t, err)
	require.InDelta(t, 0, granted, reclaimEps, "per-day 不增发钱包")

	gotSub, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, startExpire+5, gotSub.ExpireDay, "expire_day +5")
	require.Equal(t, service.ExpireDayToExpiresAt(startExpire+5).Unix(), gotSub.ExpiresAt.Unix(), "expires_at 从 expire_day 派生")
	gotUser, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 100, gotUser.Balance, reclaimEps, "余额不应变动")
}

// 近上限续费：expire_day 接近 MaxExpireDay 时再延长，必须 clamp，不得写出超过 MaxExpiresAt 的有效期。
func TestGrantSubscriptionDays_NearMaxClampsExpireDay(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	now := time.Now()
	u := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 10)

	// 把卡的 expire_day 顶到接近上限（MaxExpireDay − 2）。
	nearMax := service.MaxExpireDay() - 2
	_, err := client.UserSubscription.UpdateOneID(sub.ID).
		SetExpireDay(nearMax).
		SetExpiresAt(service.ExpireDayToExpiresAt(nearMax)).
		Save(ctx)
	require.NoError(t, err)

	// 再延长 100 天 → 应被 clamp 到 MaxExpireDay，expires_at ≤ MaxExpiresAt。
	_, _, err = repo.GrantSubscriptionDays(ctx, sub.ID, 100, now.Add(1000*time.Hour), now)
	require.NoError(t, err)

	gotSub, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.MaxExpireDay(), gotSub.ExpireDay, "expire_day 夹到 MaxExpireDay")
	require.False(t, gotSub.ExpiresAt.After(service.MaxExpiresAt), "expires_at 不得超过 MaxExpiresAt")
}

// 旧入口 ExtendExpiry 也必须同步 expire_day，不得只改 expires_at。
func TestExtendExpiry_SyncsExpireDay(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	u := reclaimSeedUser(t, client, 100)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 10)

	newExpiresAt := service.ExpireDayToExpiresAt(sub.ExpireDay + 5)
	require.NoError(t, repo.ExtendExpiry(ctx, sub.ID, newExpiresAt))

	gotSub, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, sub.ExpireDay+5, gotSub.ExpireDay)
	require.Equal(t, newExpiresAt.Unix(), gotSub.ExpiresAt.Unix())
}

// D=0 的卡 Close：本就不动余额，状态置 expired。
func TestReclaim_ZeroDailyAmountIsBalanceSafe(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)

	u := reclaimSeedUser(t, client, 42)
	g := reclaimSeedGroup(t, client)
	sub := reclaimSeedSub(t, client, u.ID, g.ID, 0)

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
