//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 转套餐（规格第 7 节）端到端：关旧卡 + 开新卡（防套利当天余额）+ 多退少补 + 每日限频。
func mustCreateChangePlanPlan(t *testing.T, client *dbent.Client, groupID int64, d float64, days int) *dbent.SubscriptionPlan {
	t.Helper()
	plan, err := client.SubscriptionPlan.Create().
		SetGroupID(groupID).
		SetName("changeplan-" + uuid.NewString()).
		SetDailyAmountUsd(d).
		SetPrice(service.DefaultSubscriptionPricingConfig().Price(d, days)).
		SetValidityDays(days).
		SetValidityUnit("day").
		SetForSale(true).
		Save(context.Background())
	require.NoError(t, err)
	return plan
}

func TestSubscriptionServiceChangePlan_UpgradeSettlesDiffAndSwapsCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	cfg := service.DefaultSubscriptionPricingConfig()
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-up-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-" + uuid.NewString()})

	// 旧卡：D=10、T=30（G=300）、今日满额未用、剩 29 天。
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})

	// 升档到 D=20、T=30。
	newPlan := mustCreateChangePlanPlan(t, client, group.ID, 20, 30)

	res, err := svc.ChangeSubscriptionPlan(ctx, user.ID, newPlan.ID)
	require.NoError(t, err)
	require.NotNil(t, res)

	// 期望值用 renew-stable 直算复核：V = cfg.Price(D_旧, refundable)（剩 29 天）；Diff = P_新 − V。
	wantV := cfg.Price(10, 29)
	wantDiff := cfg.Price(20, 30) - wantV
	require.InDelta(t, wantV, res.OldRemainingValue, 1e-6)
	require.InDelta(t, wantDiff, res.Diff, 1e-6)
	require.Greater(t, res.Diff, 0.0, "升档应补差价")

	// 旧卡已关。
	subRepo := NewUserSubscriptionRepository(client)
	gotOld, err := subRepo.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotOld.Status)

	// 新卡：active、D=20、当天余额=max(0,20−0)=20、expire=today+29、唯一 active。
	gotNew, err := subRepo.GetByID(ctx, res.NewSubscriptionID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotNew.Status)
	require.InDelta(t, 20, gotNew.DailyAmountUSD, 1e-9)
	require.InDelta(t, 20, gotNew.TodayRemaining, 1e-9)
	require.Equal(t, today, gotNew.StartDay)
	require.Equal(t, today+29, gotNew.ExpireDay)
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))

	// 余额扣了补差价；限频戳=today。
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000-res.Diff, gotUser.Balance, 1e-6)
	require.Equal(t, today, gotUser.LastChangePlanDay)

	// 同一自然日第二次转 → 拒。
	newPlan2 := mustCreateChangePlanPlan(t, client, group.ID, 15, 30)
	_, err = svc.ChangeSubscriptionPlan(ctx, user.ID, newPlan2.ID)
	require.Error(t, err)
	require.Equal(t, "CHANGE_PLAN_DAILY_LIMIT", infraerrors.Reason(err))
}

// 惰性过期的「假 active」卡（status=active 但 expire_day<today）应被同事务内关闭，转套餐据此
// 视为「无生效卡」→ ErrNoActiveSubscription（应购买新卡，而非按 V=0 全价换新）。
func TestSubscriptionServiceChangePlan_StaleActiveTreatedAsNonePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-stale-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-stale-" + uuid.NewString()})
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today - 1,
		StartDay:        today - 31,
		ExpireDay:       today - 1, // 昨天就该过期，但 status 仍 active
		ExpiresAt:       service.ExpireDayToExpiresAt(today - 1),
		Status:          service.SubscriptionStatusActive,
	})
	newPlan := mustCreateChangePlanPlan(t, client, group.ID, 20, 30)

	_, err := svc.ChangeSubscriptionPlan(ctx, user.ID, newPlan.ID)
	require.Error(t, err)
	require.Equal(t, "NO_ACTIVE_SUBSCRIPTION", infraerrors.Reason(err))

	subRepo := NewUserSubscriptionRepository(client)
	gotOld, err := subRepo.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotOld.Status, "假 active 卡应被惰性关闭")
}

func TestSubscriptionServiceChangePlan_NoActiveCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("changeplan-none-%s@example.com", uuid.NewString())})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-none-" + uuid.NewString()})
	newPlan := mustCreateChangePlanPlan(t, client, group.ID, 10, 30)

	_, err := svc.ChangeSubscriptionPlan(ctx, user.ID, newPlan.ID)
	require.Error(t, err)
	require.Equal(t, "NO_ACTIVE_SUBSCRIPTION", infraerrors.Reason(err))
}

func TestSubscriptionServiceChangePlan_InsufficientBalancePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	// 余额仅 1，升档补差价远超之 → 拒，且旧卡不被关。
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-poor-%s@example.com", uuid.NewString()),
		Balance: 1,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-poor-" + uuid.NewString()})
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})
	newPlan := mustCreateChangePlanPlan(t, client, group.ID, 50, 90) // 远贵

	_, err := svc.ChangeSubscriptionPlan(ctx, user.ID, newPlan.ID)
	require.Error(t, err)
	require.Equal(t, "INSUFFICIENT_BALANCE_FOR_CHANGE_PLAN", infraerrors.Reason(err))

	// 失败回滚：旧卡仍 active、余额未动。
	subRepo := NewUserSubscriptionRepository(client)
	gotOld, err := subRepo.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotOld.Status)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 1, gotUser.Balance, 1e-9)
}

// 规格第 8.11：转套餐当天已用额度要从新卡当天余额扣掉，防止同一天重复领取 D。
func TestSubscriptionServiceChangePlan_TodaySpentReducesNewCardBalancePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-spent-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-spent-" + uuid.NewString()})
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  2, // 今天已从套餐花掉 8
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})
	newPlan := mustCreateChangePlanPlan(t, client, group.ID, 20, 30)

	res, err := svc.ChangeSubscriptionPlan(ctx, user.ID, newPlan.ID)
	require.NoError(t, err)
	require.InDelta(t, 12, res.NewCardTodayBalance, 1e-9, "D_new=20 减旧卡今日已用 8")

	gotNew, err := NewUserSubscriptionRepository(client).GetByID(ctx, res.NewSubscriptionID)
	require.NoError(t, err)
	require.InDelta(t, 12, gotNew.TodayRemaining, 1e-9)
	require.Equal(t, today, gotNew.TodayDay)
}

// 脏套餐 validity_days<=0 必须在事务副作用前拒绝，不能关旧卡、不能扣/退余额、不能开废卡。
func TestSubscriptionServiceChangePlan_InvalidValidityRollsBackPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-bad-validity-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-bad-validity-" + uuid.NewString()})
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})
	badPlan := mustCreateChangePlanPlan(t, client, group.ID, 20, 30)
	_, err := client.SubscriptionPlan.UpdateOneID(badPlan.ID).SetValidityDays(0).Save(ctx)
	require.NoError(t, err)

	_, err = svc.ChangeSubscriptionPlan(ctx, user.ID, badPlan.ID)
	require.Error(t, err)
	require.Equal(t, "PLAN_VALIDITY_INVALID", infraerrors.Reason(err))

	gotOld, err := NewUserSubscriptionRepository(client).GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotOld.Status)
	require.Equal(t, today+29, gotOld.ExpireDay)
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, gotUser.Balance, 1e-9)
}

// 假 active 卡被惰性关闭后，即使业务返回无生效卡，也必须提交关闭动作并清 Redis 订阅缓存。
func TestSubscriptionServiceChangePlan_StaleActiveInvalidatesRedisSubscriptionCachePostgres(t *testing.T) {
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
		Email:   fmt.Sprintf("changeplan-stale-cache-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-stale-cache-" + uuid.NewString()})
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today - 1,
		StartDay:        today - 31,
		ExpireDay:       today - 1,
		ExpiresAt:       service.ExpireDayToExpiresAt(today - 1),
		Status:          service.SubscriptionStatusActive,
	})
	newPlan := mustCreateChangePlanPlan(t, client, group.ID, 20, 30)

	require.NoError(t, cache.SetSubscriptionCache(ctx, user.ID, group.ID, &service.SubscriptionCacheData{
		Status:     service.SubscriptionStatusActive,
		ExpiresAt:  time.Now().Add(time.Hour),
		Version:    1,
		DailyUsage: 1,
	}))

	_, err := svc.ChangeSubscriptionPlan(ctx, user.ID, newPlan.ID)
	require.Error(t, err)
	require.Equal(t, "NO_ACTIVE_SUBSCRIPTION", infraerrors.Reason(err))

	require.Eventually(t, func() bool {
		_, cerr := cache.GetSubscriptionCache(ctx, user.ID, group.ID)
		return cerr == redis.Nil
	}, 2*time.Second, 20*time.Millisecond, "Redis 订阅缓存应随假 active 关闭一起失效")
}
