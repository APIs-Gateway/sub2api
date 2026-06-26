//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
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

	// 期望值用同一纯测算复算（验证服务喂了正确入参）。
	wantQuote := service.QuoteChangePlan(cfg, cfg.Price(10, 30), 29, 30, 20, 30, 0, today)
	require.InDelta(t, wantQuote.Diff, res.Diff, 1e-6)
	require.Greater(t, res.Diff, 0.0, "升档应补差价")
	require.InDelta(t, wantQuote.OldRemainingValue, res.OldRemainingValue, 1e-6)

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
