//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 续费履约必须复活惰性过期卡(P1 资损/功能破坏回归):
// 续费可作用于「status=active 但已惰性过期」的卡(QuoteRenewOrder 用 GetLatestActiveStatusByUserID),
// 而后台 SubscriptionExpiryService 可能在「下单→支付成功」窗口(扫码常 >60s)内把它翻成 expired。
// GrantSubscriptionDays 只延 expires_at/expire_day、不动 status → 若不补复活,履约后 status 仍 expired,
// 被准入/结算的 status=active 过滤掉 → 用户付了钱却拿到一张不可见的死卡、无自动退款。
func TestApplyRenewFromOrder_RevivesLazilyExpiredCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{Email: "renew-revive-" + uuid.NewString() + "@example.com"})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-revive-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)

	// 模拟已被后台 expiry 任务翻成 expired 的卡:status=expired、到期日在过去。
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  d,
		DailyLimitUSD:   &d,
		WeeklyLimitUSD:  &w,
		MonthlyLimitUSD: &m,
		TodayRemaining:  d,
		TodayDay:        today,
		StartDay:        today - 40,
		ExpireDay:       today - 1,
		ExpiresAt:       service.ExpireDayToExpiresAt(today - 1),
		Status:          service.SubscriptionStatusExpired,
	})

	got, err := svc.ApplyRenewFromOrder(ctx, card.ID, 30)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status, "续费履约必须复活惰性过期卡为 active")
	require.Greater(t, got.ExpireDay, today, "有效期须延到未来(从今天起算续 30 天)")

	// 复核 DB:卡确实 active 且可被生效查询命中。
	active, err := NewUserSubscriptionRepository(client).GetActiveByUserID(ctx, user.ID)
	require.NoError(t, err, "复活后应能按生效卡查询命中")
	require.Equal(t, card.ID, active.ID)
	require.Equal(t, service.SubscriptionStatusActive, active.Status)
}

// 对照:正常未过期(status=active)的卡续费,status 保持 active、不被误改。
func TestApplyRenewFromOrder_KeepsActiveCardActivePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{Email: "renew-active-" + uuid.NewString() + "@example.com"})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-active-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  d,
		DailyLimitUSD:   &d,
		WeeklyLimitUSD:  &w,
		MonthlyLimitUSD: &m,
		TodayRemaining:  d,
		TodayDay:        today,
		StartDay:        today - 3,
		ExpireDay:       today + 5,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 5),
		Status:          service.SubscriptionStatusActive,
	})

	got, err := svc.ApplyRenewFromOrder(ctx, card.ID, 30)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	require.Equal(t, today+5+30, got.ExpireDay, "未过期卡从原到期日顺延 30 天")
}
