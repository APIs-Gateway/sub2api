//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 续费（规格第 5 节，统一 D+T-based）：只传续费天数 T'，D 取自当前卡；未到期顺延 + 扣费 + D 不变。
func TestSubscriptionServiceRenew_NotExpiredExtendsAndChargesPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	cfg := service.DefaultSubscriptionPricingConfig()
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("renew-up-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-" + uuid.NewString()})
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 10, // 未到期
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 10),
		Status:          service.SubscriptionStatusActive,
	})

	res, err := svc.RenewSubscription(ctx, user.ID, 30) // 续 30 天，D 取自卡(=10)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, card.ID, res.SubscriptionID)
	require.Equal(t, 30, res.AddedDays)
	require.InDelta(t, cfg.Price(10, 30), res.Price, 1e-6)
	require.Equal(t, today+40, res.NewExpireDay, "未到期顺延：today+10+30")

	// 卡 expire_day 延长、D 不变。
	subRepo := NewUserSubscriptionRepository(client)
	got, err := subRepo.GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+40, got.ExpireDay)
	require.InDelta(t, 10, got.DailyAmountUSD, 1e-9)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)

	// 余额扣了续费价。
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000-cfg.Price(10, 30), gotUser.Balance, 1e-6)
}

// 续费已到期卡：从今天起算 T'（today−1+T'），中间断档不补。
func TestSubscriptionServiceRenew_ExpiredRestartsFromTodayPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("renew-exp-%s@example.com", uuid.NewString()), Balance: 100000})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-exp-" + uuid.NewString()})
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  0,
		TodayDay:        today - 5,
		StartDay:        today - 35,
		ExpireDay:       today - 5, // 惰性过期但 status 仍 active
		ExpiresAt:       service.ExpireDayToExpiresAt(today - 5),
		Status:          service.SubscriptionStatusActive,
	})

	res, err := svc.RenewSubscription(ctx, user.ID, 30)
	require.NoError(t, err)
	require.Equal(t, today+29, res.NewExpireDay, "已到期从今天起算：today−1+30")

	subRepo := NewUserSubscriptionRepository(client)
	got, err := subRepo.GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+29, got.ExpireDay)
}

// 自定义卡（group_id NULL、无 plan）也能续费：D 取自卡，统一 D+T-based 不依赖 plan。
func TestSubscriptionServiceRenew_CustomNoGroupCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	cfg := service.DefaultSubscriptionPricingConfig()
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("renew-custom-%s@example.com", uuid.NewString()), Balance: 100000})
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         0, // 无 group 自定义卡
		DailyAmountUSD:  12,
		GrantedTotalUSD: 360,
		TodayRemaining:  12,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 10,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 10),
		Status:          service.SubscriptionStatusActive,
	})

	res, err := svc.RenewSubscription(ctx, user.ID, 60)
	require.NoError(t, err)
	require.Equal(t, card.ID, res.SubscriptionID)
	require.Equal(t, 60, res.AddedDays)
	require.InDelta(t, cfg.Price(12, 60), res.Price, 1e-6)
	require.Equal(t, today+70, res.NewExpireDay)

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, got.GroupID, "续费不应给自定义卡补 group")
	require.Equal(t, today+70, got.ExpireDay)
}

// 续费天数非整月（45 不是 30 的倍数）→ 收款前拒，且不扣余额、不延长卡。
func TestSubscriptionServiceRenew_InvalidValidityRollsBackPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("renew-bad-validity-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-bad-validity-" + uuid.NewString()})
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 10,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 10),
		Status:          service.SubscriptionStatusActive,
	})

	_, err := svc.RenewSubscription(ctx, user.ID, 45) // 非整月
	require.Error(t, err)
	require.Equal(t, "INVALID_SUBSCRIPTION_PARAMS", infraerrors.Reason(err))

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+10, got.ExpireDay)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, gotUser.Balance, 1e-9)
}

// 余额不足 → 拒且回滚（卡未延长、余额未动）。
func TestSubscriptionServiceRenew_InsufficientBalanceRollsBackPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("renew-poor-%s@example.com", uuid.NewString()), Balance: 1})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-poor-" + uuid.NewString()})
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		DailyAmountUSD: 10, GrantedTotalUSD: 300,
		TodayRemaining: 10, TodayDay: today, StartDay: today, ExpireDay: today + 10,
		ExpiresAt: service.ExpireDayToExpiresAt(today + 10), Status: service.SubscriptionStatusActive,
	})

	_, err := svc.RenewSubscription(ctx, user.ID, 30)
	require.Error(t, err)
	require.Equal(t, "INSUFFICIENT_BALANCE_FOR_RENEW", infraerrors.Reason(err))

	subRepo := NewUserSubscriptionRepository(client)
	got, err := subRepo.GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+10, got.ExpireDay, "失败回滚：expire_day 不变")
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 1, gotUser.Balance, 1e-9)
}

// 无生效卡 → 拒（应购买）。
func TestSubscriptionServiceRenew_NoActiveCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("renew-none-%s@example.com", uuid.NewString()), Balance: 100000})

	_, err := svc.RenewSubscription(ctx, user.ID, 30)
	require.Error(t, err)
	require.Equal(t, "NO_ACTIVE_SUBSCRIPTION", infraerrors.Reason(err))
}

// 续费会改变 expire_day/expires_at，必须清掉 Redis 订阅缓存，避免服务层短时间读旧 expires_at。
func TestSubscriptionServiceRenew_InvalidatesRedisSubscriptionCachePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	rdb := testRedis(t)
	cache := NewBillingCache(rdb)
	billingCacheSvc := service.NewBillingCacheService(cache, NewUserRepository(client, integrationDB), NewUserSubscriptionRepository(client), nil, nil, nil, &config.Config{}, nil, nil)
	t.Cleanup(billingCacheSvc.Stop)
	svc := service.NewSubscriptionService(
		NewGroupRepository(client, integrationDB),
		NewUserSubscriptionRepository(client),
		nil,
		billingCacheSvc,
		nil,
		client,
		nil,
		nil,
	)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("renew-cache-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-cache-" + uuid.NewString()})
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 10,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 10),
		Status:          service.SubscriptionStatusActive,
	})

	require.NoError(t, cache.SetSubscriptionCache(ctx, user.ID, group.ID, &service.SubscriptionCacheData{
		Status:     service.SubscriptionStatusActive,
		ExpiresAt:  service.ExpireDayToExpiresAt(today + 10),
		Version:    1,
		DailyUsage: 1,
	}))

	_, err := svc.RenewSubscription(ctx, user.ID, 30)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, cerr := cache.GetSubscriptionCache(ctx, user.ID, group.ID)
		return cerr == redis.Nil
	}, 2*time.Second, 20*time.Millisecond, "Redis 订阅缓存应随续费失效")
}
