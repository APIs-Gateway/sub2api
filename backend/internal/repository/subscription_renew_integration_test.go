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

// 续费走法币支付网关（规格第 5 节，统一 D+T-based）：报价(QuoteRenewOrder) 算价、不改状态；
// 履约(ApplyRenewFromOrder) 在支付成功后延长有效期、**不扣余额**。这里分别覆盖二者。

// 报价：未到期卡续 30 天，D 取自卡(=10)，价 = cfg.Price(10,30)，不改任何状态。
func TestSubscriptionServiceRenewQuote_NotExpiredPostgres(t *testing.T) {
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

	q, err := svc.QuoteRenewOrder(ctx, user.ID, 30)
	require.NoError(t, err)
	require.NotNil(t, q)
	require.Equal(t, card.ID, q.SubscriptionID)
	require.Equal(t, 30, q.AddedDays)
	require.InDelta(t, 10, q.DailyAmountUSD, 1e-9)
	require.InDelta(t, cfg.Price(10, 30), q.Price, 1e-6)

	// 报价不改任何状态：卡未延长、余额未动。
	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+10, got.ExpireDay)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, gotUser.Balance, 1e-9)
}

// 履约：未到期卡续 30 天 → 顺延 today+40，D 不变，**不扣余额**。
func TestSubscriptionServiceRenewApply_NotExpiredExtendsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("renew-apply-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-apply-" + uuid.NewString()})
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

	sub, err := svc.ApplyRenewFromOrder(ctx, card.ID, 30)
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Equal(t, today+40, sub.ExpireDay, "未到期顺延：today+10+30")

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+40, got.ExpireDay)
	require.InDelta(t, 10, got.DailyAmountUSD, 1e-9)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)

	// 履约不扣余额（续费价已由网关收取）。
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, gotUser.Balance, 1e-9, "履约不动钱包余额")
}

// 履约已到期卡：从今天起算 T'（today−1+T'），中间断档不补。
func TestSubscriptionServiceRenewApply_ExpiredRestartsFromTodayPostgres(t *testing.T) {
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

	sub, err := svc.ApplyRenewFromOrder(ctx, card.ID, 30)
	require.NoError(t, err)
	require.Equal(t, today+29, sub.ExpireDay, "已到期从今天起算：today−1+30")

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+29, got.ExpireDay)
}

// 自定义卡（group_id NULL、无 plan）也能续费：D 取自卡，统一 D+T-based 不依赖 plan。
func TestSubscriptionServiceRenewApply_CustomNoGroupCardPostgres(t *testing.T) {
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

	// 报价：D 取自卡(=12)，价 cfg.Price(12,60)。
	q, err := svc.QuoteRenewOrder(ctx, user.ID, 60)
	require.NoError(t, err)
	require.Equal(t, card.ID, q.SubscriptionID)
	require.Equal(t, 60, q.AddedDays)
	require.InDelta(t, cfg.Price(12, 60), q.Price, 1e-6)

	// 履约：延长，且不给自定义卡补 group。
	_, err = svc.ApplyRenewFromOrder(ctx, card.ID, 60)
	require.NoError(t, err)
	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, got.GroupID, "续费不应给自定义卡补 group")
	require.Equal(t, today+70, got.ExpireDay)
}

// 续费天数非整月（45 不是 30 的倍数）→ 报价即拒（收款前），不改任何状态。
func TestSubscriptionServiceRenewQuote_InvalidValidityPostgres(t *testing.T) {
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

	_, err := svc.QuoteRenewOrder(ctx, user.ID, 45) // 非整月
	require.Error(t, err)
	require.Equal(t, "INVALID_SUBSCRIPTION_PARAMS", infraerrors.Reason(err))

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+10, got.ExpireDay)
}

// 无生效卡 → 报价拒（应购买）。
func TestSubscriptionServiceRenewQuote_NoActiveCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("renew-none-%s@example.com", uuid.NewString()), Balance: 100000})

	_, err := svc.QuoteRenewOrder(ctx, user.ID, 30)
	require.Error(t, err)
	require.Equal(t, "NO_ACTIVE_SUBSCRIPTION", infraerrors.Reason(err))
}

// 履约会改变 expire_day/expires_at，必须清掉 Redis 订阅缓存，避免服务层短时间读旧 expires_at。
func TestSubscriptionServiceRenewApply_InvalidatesRedisSubscriptionCachePostgres(t *testing.T) {
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

	require.NoError(t, cache.SetSubscriptionCache(ctx, user.ID, group.ID, &service.SubscriptionCacheData{
		Status:     service.SubscriptionStatusActive,
		ExpiresAt:  service.ExpireDayToExpiresAt(today + 10),
		Version:    1,
		DailyUsage: 1,
	}))

	_, err := svc.ApplyRenewFromOrder(ctx, card.ID, 30)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, cerr := cache.GetSubscriptionCache(ctx, user.ID, group.ID)
		return cerr == redis.Nil
	}, 2*time.Second, 20*time.Millisecond, "Redis 订阅缓存应随续费履约失效")
}
