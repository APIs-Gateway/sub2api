//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// seedAdjustLockSub creates a user + group + one active per-day card whose last
// service day is expireDay, and removes them (plus any redeem codes the test
// registers via the returned cleanup list) after the test. Services under test
// commit their own transactions, so fixtures must be cleaned up explicitly to
// keep list/count integration tests isolated.
func seedAdjustLockSub(t *testing.T, client *dbent.Client, expireDay int, notes string) (*dbent.User, *dbent.UserSubscription, *[]int64) {
	t.Helper()
	ctx := context.Background()
	user := reclaimSeedUser(t, client, 100)
	group := reclaimSeedGroup(t, client)
	now := time.Now()
	today := service.EastDayNumber(now)
	sub, err := client.UserSubscription.Create().
		SetUserID(user.ID).
		SetGroupID(group.ID).
		SetStartsAt(now.Add(-time.Hour)).
		SetExpiresAt(service.ExpireDayToExpiresAt(expireDay)).
		SetStatus(service.SubscriptionStatusActive).
		SetAssignedAt(now).
		SetActivatedAt(now.Add(-time.Hour)).
		SetDailyAmountUsd(10).
		SetTodayRemaining(10).
		SetTodayDay(today).
		SetStartDay(today).
		SetExpireDay(expireDay).
		SetNotes(notes).
		Save(ctx)
	require.NoError(t, err)

	codeIDs := &[]int64{}
	t.Cleanup(func() {
		bg := context.Background()
		for _, id := range *codeIDs {
			_, err := integrationDB.ExecContext(bg, `DELETE FROM redeem_codes WHERE id=$1`, id)
			require.NoError(t, err)
		}
		_, err := integrationDB.ExecContext(bg, `DELETE FROM user_subscriptions WHERE user_id=$1`, user.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(bg, `DELETE FROM groups WHERE id=$1`, group.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(bg, `DELETE FROM users WHERE id=$1`, user.ID)
		require.NoError(t, err)
	})
	return user, sub, codeIDs
}

// 管理员并发缩短同一张卡：卡服务到 today+7（剩 8 个自然日），两次 -5 并发。
// 锁内判定后只能有一次成功（expire_day → today+2），另一次基于最新行判定会缩穿，
// 必须返回 ErrAdjustWouldExpire；锁外判定时两次都会通过校验，把卡缩到 today−1。
func TestExtendSubscriptionConcurrentShortenDoesNotOvershoot(t *testing.T) {
	client := testEntClient(t)
	today := service.EastDayNumber(time.Now())
	_, sub, _ := seedAdjustLockSub(t, client, today+7, "")
	svc := service.NewSubscriptionService(nil, NewUserSubscriptionRepository(client), nil, nil, nil, client, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.ExtendSubscription(ctx, sub.ID, -5)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, rejected int
	for err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		require.ErrorIs(t, err, service.ErrAdjustWouldExpire)
		rejected++
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, rejected)

	got, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, today+2, got.ExpireDay)
	require.True(t, service.ExpireDayToExpiresAt(today+2).Equal(got.ExpiresAt))
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
}

// 延长与缩短并发：+10 与 -3 无论谁先拿到行锁，结果都是 today+7+10−3 = today+14。
func TestExtendSubscriptionConcurrentGrantAndShortenBothApply(t *testing.T) {
	client := testEntClient(t)
	today := service.EastDayNumber(time.Now())
	_, sub, _ := seedAdjustLockSub(t, client, today+7, "")
	svc := service.NewSubscriptionService(nil, NewUserSubscriptionRepository(client), nil, nil, nil, client, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, days := range []int{10, -3} {
		wg.Add(1)
		go func(days int) {
			defer wg.Done()
			<-start
			_, err := svc.ExtendSubscription(ctx, sub.ID, days)
			errs <- err
		}(days)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	got, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, today+14, got.ExpireDay)
	require.True(t, service.ExpireDayToExpiresAt(today+14).Equal(got.ExpiresAt))
}
