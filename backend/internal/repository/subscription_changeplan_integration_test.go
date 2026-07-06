//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 转套餐走法币支付网关（规格第 7 节）：报价(QuoteChangePlanOrder) 算 diff、不改状态；
// 履约(ApplyChangePlanFromOrder) 在支付成功后关旧开新、**不扣余额**。降档(diff<0)报价即拒。

// mustCreateChangePlanPlan 仅用于产生一组合法 D/T 输入（不再参与扣费/折价；转套餐统一 D+T-based）。
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

// 升档：报价算 diff>0；履约关旧开新、继承三窗口用量、不扣余额；履约后再报价撞每日限频。
func TestSubscriptionServiceChangePlan_QuoteThenApplyUpgradePostgres(t *testing.T) {
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
	now := time.Now()
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	oldDaily, oldWeekly, oldMonthly := 30.0, 210.0, 900.0

	// 旧卡：D=30、剩 29 天、今日满额未用（TodayRemaining=30）。
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     30,
		GrantedTotalUSD:    900,
		TodayRemaining:     30,
		TodayDay:           today,
		StartDay:           today,
		ExpireDay:          today + 29,
		ExpiresAt:          service.ExpireDayToExpiresAt(today + 29),
		Status:             service.SubscriptionStatusActive,
		DailyLimitUSD:      &oldDaily,
		WeeklyLimitUSD:     &oldWeekly,
		MonthlyLimitUSD:    &oldMonthly,
		DailyUsageUSD:      4,
		WeeklyUsageUSD:     14,
		MonthlyUsageUSD:    24,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})

	// 报价：升档到 D=60、T=30。
	q, err := svc.QuoteChangePlanOrder(ctx, user.ID, 60, 30)
	require.NoError(t, err)
	require.NotNil(t, q)
	require.Equal(t, old.ID, q.OldSubscriptionID)
	wantV := cfg.Price(30, 29) // 剩 29 天
	wantDiff := cfg.Price(60, 30) - wantV
	require.InDelta(t, wantV, q.OldRemainingValue, 1e-6)
	require.InDelta(t, cfg.Price(60, 30), q.NewPlanPrice, 1e-6)
	require.InDelta(t, wantDiff, q.Diff, 1e-6)
	require.Greater(t, q.Diff, 0.0, "升档应 diff>0（走网关补差价）")

	// 报价不改状态：旧卡仍 active、余额未动。
	subRepo := NewUserSubscriptionRepository(client)
	stillOld, err := subRepo.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, stillOld.Status)
	u0, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, u0.Balance, 1e-9)

	// 履约（支付成功后）：关旧、开新。
	res, err := svc.ApplyChangePlanFromOrder(ctx, old.ID, 60, 30)
	require.NoError(t, err)
	require.NotNil(t, res)

	gotOld, err := subRepo.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotOld.Status)

	gotNew, err := subRepo.GetByID(ctx, res.NewSubscriptionID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotNew.Status)
	require.InDelta(t, 60, gotNew.DailyAmountUSD, 1e-9)
	require.InDelta(t, 60, gotNew.TodayRemaining, 1e-9)
	require.Equal(t, today, gotNew.StartDay)
	require.Equal(t, today+29, gotNew.ExpireDay)
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	require.NotNil(t, gotNew.DailyLimitUSD)
	require.NotNil(t, gotNew.WeeklyLimitUSD)
	require.NotNil(t, gotNew.MonthlyLimitUSD)
	require.InDelta(t, 60, *gotNew.DailyLimitUSD, 1e-9)
	require.InDelta(t, 420, *gotNew.WeeklyLimitUSD, 1e-9)
	require.InDelta(t, 1800, *gotNew.MonthlyLimitUSD, 1e-9)
	require.InDelta(t, 4, gotNew.DailyUsageUSD, 1e-9, "转套餐新卡必须继承旧卡日窗口 usage，防当天双领")
	require.InDelta(t, 14, gotNew.WeeklyUsageUSD, 1e-9, "转套餐新卡必须继承旧卡周窗口 usage，防本周双领")
	require.InDelta(t, 24, gotNew.MonthlyUsageUSD, 1e-9, "转套餐新卡必须继承旧卡月窗口 usage，防本月双领")
	require.NotNil(t, gotNew.DailyWindowStart)
	require.NotNil(t, gotNew.WeeklyWindowStart)
	require.NotNil(t, gotNew.MonthlyWindowStart)
	require.Equal(t, dayStart.Unix(), gotNew.DailyWindowStart.Unix())
	require.Equal(t, weekStart.Unix(), gotNew.WeeklyWindowStart.Unix())
	require.Equal(t, monthStart.Unix(), gotNew.MonthlyWindowStart.Unix())

	// 履约不扣余额（补差价已网关收取）；限频戳=today。
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, gotUser.Balance, 1e-9, "履约不动钱包余额")
	require.Equal(t, today, gotUser.LastChangePlanDay)

	// 同一自然日第二次报价 → 撞限频拒。
	_, err = svc.QuoteChangePlanOrder(ctx, user.ID, 90, 30)
	require.Error(t, err)
	require.Equal(t, "CHANGE_PLAN_DAILY_LIMIT", infraerrors.Reason(err))
}

// 降档（新档折价后 diff<0：旧卡剩余价值 > 新档价）→ 报价即拒（禁止赔钱降档），不改状态。
func TestSubscriptionServiceChangePlanQuote_DowngradeRejectedPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-down-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-down-" + uuid.NewString()})
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  90, // 大 D 高价值
		GrantedTotalUSD: 2700,
		TodayRemaining:  90,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29, // 剩余价值高
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})

	// 降到 D=30、T=30：V=cfg.Price(90,29) > P_新=cfg.Price(30,30) → diff<0 → 拒。
	_, err := svc.QuoteChangePlanOrder(ctx, user.ID, 30, 30)
	require.Error(t, err)
	require.Equal(t, "CHANGE_PLAN_DOWNGRADE_NOT_ALLOWED", infraerrors.Reason(err))

	gotOld, err := NewUserSubscriptionRepository(client).GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotOld.Status, "报价被拒不改任何状态")
}

// 假 active 卡（status=active 但 expires_at 已过）按 GetActiveByUserID 过滤排除 → 报价视为无生效卡。
func TestSubscriptionServiceChangePlanQuote_StaleActiveTreatedAsNonePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-stale-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-stale-" + uuid.NewString()})
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  30,
		GrantedTotalUSD: 900,
		TodayRemaining:  30,
		TodayDay:        today - 1,
		StartDay:        today - 31,
		ExpireDay:       today - 1, // 昨天就该过期，但 status 仍 active
		ExpiresAt:       service.ExpireDayToExpiresAt(today - 1),
		Status:          service.SubscriptionStatusActive,
	})

	_, err := svc.QuoteChangePlanOrder(ctx, user.ID, 60, 30)
	require.Error(t, err)
	require.Equal(t, "NO_ACTIVE_SUBSCRIPTION", infraerrors.Reason(err))
}

func TestSubscriptionServiceChangePlanQuote_NoActiveCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("changeplan-none-%s@example.com", uuid.NewString())})

	_, err := svc.QuoteChangePlanOrder(ctx, user.ID, 30, 30)
	require.Error(t, err)
	require.Equal(t, "NO_ACTIVE_SUBSCRIPTION", infraerrors.Reason(err))
}

// 规格第 8.11：转套餐当天已用额度要从新卡当天余额扣掉，防止同一天重复领取 D。
func TestSubscriptionServiceChangePlanApply_TodaySpentReducesNewCardBalancePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-spent-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "changeplan-spent-" + uuid.NewString()})
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  30,
		GrantedTotalUSD: 900,
		TodayRemaining:  22, // 今天已从套餐花掉 8
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})

	res, err := svc.ApplyChangePlanFromOrder(ctx, old.ID, 60, 30)
	require.NoError(t, err)
	require.InDelta(t, 52, res.NewCardTodayBalance, 1e-9, "D_new=60 减旧卡今日已用 8")

	gotNew, err := NewUserSubscriptionRepository(client).GetByID(ctx, res.NewSubscriptionID)
	require.NoError(t, err)
	require.InDelta(t, 52, gotNew.TodayRemaining, 1e-9)
	require.Equal(t, today, gotNew.TodayDay)
}

// 履约新卡带三窗口限额，后续请求由订阅覆盖、不扣钱包（钱包 1:1，本次完全订阅覆盖 → WalletDebit=0）。
func TestSubscriptionServiceChangePlanApply_NewCardBillsThroughThreeWindowsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)
	billingRepo := NewUsageBillingRepository(client, integrationDB)
	today := service.TodayEastDayNumber()
	now := time.Now()
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("changeplan-window-bill-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "changeplan-window-bill-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-changeplan-window-bill-" + uuid.NewString(),
		Name:    "changeplan-window-bill",
	})
	oldDaily, oldWeekly, oldMonthly := 30.0, 210.0, 900.0
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     30,
		GrantedTotalUSD:    900,
		TodayRemaining:     26,
		TodayDay:           today,
		StartDay:           today,
		ExpireDay:          today + 29,
		ExpiresAt:          service.ExpireDayToExpiresAt(today + 29),
		Status:             service.SubscriptionStatusActive,
		DailyLimitUSD:      &oldDaily,
		WeeklyLimitUSD:     &oldWeekly,
		MonthlyLimitUSD:    &oldMonthly,
		DailyUsageUSD:      4,
		WeeklyUsageUSD:     14,
		MonthlyUsageUSD:    24,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})

	res, err := svc.ApplyChangePlanFromOrder(ctx, old.ID, 60, 30)
	require.NoError(t, err)

	applyRes, err := billingRepo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		SubscriptionID: &res.NewSubscriptionID,
		OfficialCost:   5,
		RateMultiplier: 2, // 本次全由订阅覆盖（1:1），钱包不受倍率影响
	})
	require.NoError(t, err)
	require.True(t, applyRes.Applied)
	require.NotNil(t, applyRes.SubscriptionID)
	require.Equal(t, res.NewSubscriptionID, *applyRes.SubscriptionID)
	require.NotNil(t, applyRes.WalletDebit)
	require.InDelta(t, 0, *applyRes.WalletDebit, 1e-9, "转套餐新卡带三窗口限额，本次完全由订阅覆盖")

	gotNew, err := NewUserSubscriptionRepository(client).GetByID(ctx, res.NewSubscriptionID)
	require.NoError(t, err)
	require.InDelta(t, 9, gotNew.DailyUsageUSD, 1e-9)
	require.InDelta(t, 19, gotNew.WeeklyUsageUSD, 1e-9)
	require.InDelta(t, 29, gotNew.MonthlyUsageUSD, 1e-9)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, gotUser.Balance, 1e-6, "履约 + 订阅覆盖请求均不动钱包余额")
}

// 目标 T 非整月（45 不是 30 的倍数）→ 报价即拒，不改任何状态。
func TestSubscriptionServiceChangePlanQuote_InvalidValidityPostgres(t *testing.T) {
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
		DailyAmountUSD:  30,
		GrantedTotalUSD: 900,
		TodayRemaining:  30,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})

	_, err := svc.QuoteChangePlanOrder(ctx, user.ID, 60, 45) // 45 非整月
	require.Error(t, err)
	require.Equal(t, "INVALID_SUBSCRIPTION_PARAMS", infraerrors.Reason(err))

	gotOld, err := NewUserSubscriptionRepository(client).GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotOld.Status)
	require.Equal(t, today+29, gotOld.ExpireDay)
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
}
