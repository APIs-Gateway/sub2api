//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 续费（规格第 5 节）：未到期顺延 + 扣费 + D 不变。
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
	plan := mustCreateChangePlanPlan(t, client, group.ID, 10, 30) // 同 D=10，续 30 天

	res, err := svc.RenewSubscription(ctx, user.ID, plan.ID)
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
	plan := mustCreateChangePlanPlan(t, client, group.ID, 10, 30)

	res, err := svc.RenewSubscription(ctx, user.ID, plan.ID)
	require.NoError(t, err)
	require.Equal(t, today+29, res.NewExpireDay, "已到期从今天起算：today−1+30")

	subRepo := NewUserSubscriptionRepository(client)
	got, err := subRepo.GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+29, got.ExpireDay)
}

// 续费 plan 的 D 与当前卡不同 → 拒（应走转套餐）。
func TestSubscriptionServiceRenew_PlanMismatchPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("renew-mis-%s@example.com", uuid.NewString()), Balance: 100000})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-mis-" + uuid.NewString()})
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		DailyAmountUSD: 10, GrantedTotalUSD: 300,
		TodayRemaining: 10, TodayDay: today, StartDay: today, ExpireDay: today + 10,
		ExpiresAt: service.ExpireDayToExpiresAt(today + 10), Status: service.SubscriptionStatusActive,
	})
	plan := mustCreateChangePlanPlan(t, client, group.ID, 20, 30) // D=20 ≠ 卡 D=10

	_, err := svc.RenewSubscription(ctx, user.ID, plan.ID)
	require.Error(t, err)
	require.Equal(t, "RENEW_PLAN_MISMATCH", infraerrors.Reason(err))
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
	plan := mustCreateChangePlanPlan(t, client, group.ID, 10, 30)

	_, err := svc.RenewSubscription(ctx, user.ID, plan.ID)
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
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-none-" + uuid.NewString()})
	plan := mustCreateChangePlanPlan(t, client, group.ID, 10, 30)

	_, err := svc.RenewSubscription(ctx, user.ID, plan.ID)
	require.Error(t, err)
	require.Equal(t, "NO_ACTIVE_SUBSCRIPTION", infraerrors.Reason(err))
}
