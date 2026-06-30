//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// createPaidLifecycleOrderForIntegration 造一张「已支付」的生命周期订单（续费/转套餐），
// 冻结快照含 intent + target_subscription_id + 新 D/T + charge_amount，模拟法币网关回调到 PAID
// 之后的待履约态。履约由 ExecuteSubscriptionFulfillment → doSub → doSubLifecycle 接手。
func createPaidLifecycleOrderForIntegration(t *testing.T, client *dbent.Client, user *service.User, intent string, targetSubID int64, dailyAmount float64, days int, charge float64) int64 {
	t.Helper()
	now := time.Now()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(charge).
		SetPayAmount(charge).
		SetFeeRate(0).
		SetRechargeCode("PAY-LIFECYCLE-" + uuid.NewString()).
		SetOutTradeNo("lifecycle_" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("provider-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionDays(days).
		SetProviderSnapshot(map[string]any{
			"subscription": map[string]any{
				"daily_amount_usd":       dailyAmount,
				"validity_days":          float64(days),
				"unit_price":             1.0,
				"price":                  charge,
				"formula_version":        service.SubscriptionFormulaVersion,
				"currency":               payment.DefaultPaymentCurrency,
				"intent":                 intent,
				"target_subscription_id": targetSubID,
				"charge_amount":          charge,
			},
		}).
		SetStatus(service.OrderStatusPaid).
		SetPaidAt(now).
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(context.Background())
	require.NoError(t, err)
	return order.ID
}

// 续费履约（money-path：下单→支付→履约）：按快照目标卡 + 续费天数延长有效期，
// 同一张卡 ID 不变、D/限额不变、不动余额（续费价已网关收取）。
func TestPaymentSubscriptionFulfillment_RenewIntentExtendsCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("lifecycle-renew-%s@example.com", uuid.NewString()),
		Balance: 500,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "lifecycle-renew-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	dDaily := 10.0
	wLimit, mLimit := service.DeriveWindowCaps(dDaily, 30)
	originalExpireDay := today + 5
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dDaily,
		DailyLimitUSD:   &dDaily,
		WeeklyLimitUSD:  &wLimit,
		MonthlyLimitUSD: &mLimit,
		TodayRemaining:  dDaily,
		TodayDay:        today,
		StartDay:        today - 5,
		ExpireDay:       originalExpireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(originalExpireDay),
		Status:          service.SubscriptionStatusActive,
	})

	orderID := createPaidLifecycleOrderForIntegration(t, client, user, service.SubscriptionIntentRenew, card.ID, dDaily, 30, 100)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	// 续费只延长同一张卡，绝不新建第二张。
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	// GrantSubscriptionDays：expire_day = max(原, today−1) + addDays = (today+5)+30。
	require.Equal(t, originalExpireDay+30, got.ExpireDay)
	require.True(t, got.ExpiresAt.Equal(service.ExpireDayToExpiresAt(originalExpireDay+30)))
	require.NotNil(t, got.DailyLimitUSD)
	require.InDelta(t, dDaily, *got.DailyLimitUSD, 1e-9, "续费不改每日额度 D")

	gotOrder, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, gotOrder.Status)

	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 500, gotUser.Balance, 1e-9, "续费走网关，履约不动钱包余额")
}

// 续费履约幂等：重放（订单复位 PAID 再履约）由 SUBSCRIPTION_SUCCESS 审计键拦截，
// 不得二次延长有效期。
func TestPaymentSubscriptionFulfillment_RenewIntentIdempotentOnReplayPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("lifecycle-renew-idem-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "lifecycle-renew-idem-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	dDaily := 12.0
	wLimit, mLimit := service.DeriveWindowCaps(dDaily, 30)
	originalExpireDay := today + 3
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dDaily,
		DailyLimitUSD:   &dDaily,
		WeeklyLimitUSD:  &wLimit,
		MonthlyLimitUSD: &mLimit,
		TodayRemaining:  dDaily,
		TodayDay:        today,
		StartDay:        today - 2,
		ExpireDay:       originalExpireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(originalExpireDay),
		Status:          service.SubscriptionStatusActive,
	})

	orderID := createPaidLifecycleOrderForIntegration(t, client, user, service.SubscriptionIntentRenew, card.ID, dDaily, 30, 120)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	afterFirst, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, originalExpireDay+30, afterFirst.ExpireDay)

	// 模拟 markCompleted 后的重放：把订单复位为 PAID 再次履约。
	_, err = client.PaymentOrder.UpdateOneID(orderID).SetStatus(service.OrderStatusPaid).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	afterReplay, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, originalExpireDay+30, afterReplay.ExpireDay, "重放不得二次延长有效期")
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))

	gotOrder, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, gotOrder.Status)
}

// P2#6 回归:续费履约后订单被置 FAILED(模拟 markCompleted 状态更新瞬时报错的历史窗口——彼时
// apply 已在自有事务提交、但 SUCCESS 审计未写 → 管理员重试会再 apply 一次 = 双倍延期资损),
// 经管理员 RetryFulfillment 重试,不得二次延长有效期。我方修复:apply + 订单完成 + SUCCESS 审计
// 三者同一事务原子提交,故任何「已 apply」的成功履约必带 SUCCESS 审计,重试见审计即跳过。
func TestPaymentSubscriptionFulfillment_RenewNoDoubleApplyOnFailedRetryPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("lifecycle-renew-failretry-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "lifecycle-renew-failretry-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	dDaily := 11.0
	wLimit, mLimit := service.DeriveWindowCaps(dDaily, 30)
	originalExpireDay := today + 4
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dDaily,
		DailyLimitUSD:   &dDaily,
		WeeklyLimitUSD:  &wLimit,
		MonthlyLimitUSD: &mLimit,
		TodayRemaining:  dDaily,
		TodayDay:        today,
		StartDay:        today - 3,
		ExpireDay:       originalExpireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(originalExpireDay),
		Status:          service.SubscriptionStatusActive,
	})

	orderID := createPaidLifecycleOrderForIntegration(t, client, user, service.SubscriptionIntentRenew, card.ID, dDaily, 30, 110)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	afterFirst, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, originalExpireDay+30, afterFirst.ExpireDay)

	// 佐证原子性:成功履约后 SUCCESS 审计必与延期同时存在(同一事务提交)。
	logs, err := paymentSvc.GetOrderAuditLogs(ctx, orderID)
	require.NoError(t, err)
	hasSuccess := false
	for _, l := range logs {
		if l.Action == "SUBSCRIPTION_SUCCESS" {
			hasSuccess = true
		}
	}
	require.True(t, hasSuccess, "apply 成功必带 SUBSCRIPTION_SUCCESS 审计(原子提交)")

	// 模拟历史窗口:订单被 markFailed 置 FAILED(审计在我方修复下已随 apply 落库,故仍在)。
	_, err = client.PaymentOrder.UpdateOneID(orderID).
		SetStatus(service.OrderStatusFailed).SetFailedReason("simulated transient mark-completed error").Save(ctx)
	require.NoError(t, err)

	// 管理员手动重试(真实触发路径):FAILED→PAID→履约 → 见 SUCCESS 审计跳过,不再 apply。
	require.NoError(t, paymentSvc.RetryFulfillment(ctx, orderID))

	afterRetry, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, originalExpireDay+30, afterRetry.ExpireDay, "FAILED→重试不得二次延长有效期")
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))

	gotOrder, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, gotOrder.Status)
}

// 转套餐履约（money-path）：关旧卡、开新卡（无 group 自定义卡），新卡限额 = 新 D/T 派生，
// 三窗口 usage 继承旧卡（堵当天/周/月换档重领），stamp last_change_plan_day，不动余额。
func TestPaymentSubscriptionFulfillment_ChangePlanIntentClosesOldOpensNewPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("lifecycle-change-%s@example.com", uuid.NewString()),
		Balance: 700,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "lifecycle-change-" + uuid.NewString()})
	today := service.TodayEastDayNumber()

	dOld := 10.0
	wOld, mOld := service.DeriveWindowCaps(dOld, 30)
	dUsage, wUsage, mUsage := 3.0, 8.0, 15.0
	winStart := time.Now().Add(-2 * time.Hour)
	oldExpireDay := today + 20
	oldCard := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     dOld,
		DailyLimitUSD:      &dOld,
		WeeklyLimitUSD:     &wOld,
		MonthlyLimitUSD:    &mOld,
		DailyUsageUSD:      dUsage,
		WeeklyUsageUSD:     wUsage,
		MonthlyUsageUSD:    mUsage,
		DailyWindowStart:   &winStart,
		WeeklyWindowStart:  &winStart,
		MonthlyWindowStart: &winStart,
		TodayRemaining:     dOld,
		TodayDay:           today,
		StartDay:           today - 3,
		ExpireDay:          oldExpireDay,
		ExpiresAt:          service.ExpireDayToExpiresAt(oldExpireDay),
		Status:             service.SubscriptionStatusActive,
	})

	dNew := 20.0
	wNew, mNew := service.DeriveWindowCaps(dNew, 30)
	orderID := createPaidLifecycleOrderForIntegration(t, client, user, service.SubscriptionIntentChangePlan, oldCard.ID, dNew, 30, 150)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	// 旧卡关闭（保留行、status=expired）。
	gotOld, err := NewUserSubscriptionRepository(client).GetByID(ctx, oldCard.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotOld.Status)

	// 唯一活跃卡 = 新卡。
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	newSub, err := NewUserSubscriptionRepository(client).GetActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.NotEqual(t, oldCard.ID, newSub.ID)
	require.EqualValues(t, 0, newSub.GroupID, "转套餐新卡为无 group 自定义卡（domain group_id=0）")
	require.NotNil(t, newSub.DailyLimitUSD)
	require.NotNil(t, newSub.WeeklyLimitUSD)
	require.NotNil(t, newSub.MonthlyLimitUSD)
	require.InDelta(t, dNew, *newSub.DailyLimitUSD, 1e-9)
	require.InDelta(t, wNew, *newSub.WeeklyLimitUSD, 1e-9)
	require.InDelta(t, mNew, *newSub.MonthlyLimitUSD, 1e-9)
	require.Equal(t, service.ClampExpireDay(today+29), newSub.ExpireDay)
	// 三窗口 usage 继承旧卡（防当天/本周/本月换档重领）。
	require.InDelta(t, dUsage, newSub.DailyUsageUSD, 1e-9)
	require.InDelta(t, wUsage, newSub.WeeklyUsageUSD, 1e-9)
	require.InDelta(t, mUsage, newSub.MonthlyUsageUSD, 1e-9)

	// DB 层 group_id 必须为 NULL。
	var rawGroupID *int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT group_id FROM user_subscriptions WHERE id = $1`, newSub.ID).Scan(&rawGroupID))
	require.Nil(t, rawGroupID)

	// last_change_plan_day 已戳（每日转套餐限频依据）。
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, today, gotUser.LastChangePlanDay)
	require.InDelta(t, 700, gotUser.Balance, 1e-9, "转套餐补差价走网关，履约不动钱包余额")

	gotOrder, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, gotOrder.Status)
}

// 支付网关成功回调 → 履约建卡（回调→建卡 leg）：直接构造 PENDING 自定义订阅订单（绕开
// CreateOrder 的 provider 调用，那需要 registry 注册真实 provider），驱动 HandlePaymentNotification
// 走 confirmPayment → toPaid → executeFulfillment → doSub(purchase)，断言建出无 group 三窗口卡。
// alipay 无需上游二次确认、无 metadata 约束；registry 为 nil 时 provider key 回退到订单 payment_type。
func TestPaymentSubscriptionWebhook_CustomOrderBuildsThreeWindowCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("webhook-custom-%s@example.com", uuid.NewString()),
		Username: "webhook-custom",
		Balance:  300,
	})

	const dailyAmount = 9.0
	const validityDays = 30
	quote, err := service.DefaultSubscriptionPricingConfig().Quote(dailyAmount, validityDays)
	require.NoError(t, err)
	weeklyLimit, monthlyLimit := service.DeriveWindowCaps(dailyAmount, validityDays)

	outTradeNo := "webhook_custom_" + uuid.NewString()
	now := time.Now()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(quote.Price).
		SetPayAmount(quote.Price).
		SetFeeRate(0).
		SetRechargeCode("PAY-WEBHOOK-" + uuid.NewString()).
		SetOutTradeNo(outTradeNo).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(""). // PENDING 单尚无上游交易号（与真实 CreateOrder 一致：必须显式 set，空串合法）
		SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionDays(validityDays).
		SetProviderSnapshot(map[string]any{
			"subscription": map[string]any{
				"daily_amount_usd": dailyAmount,
				"validity_days":    float64(validityDays),
				"unit_price":       quote.UnitPrice,
				"price":            quote.Price,
				"formula_version":  service.SubscriptionFormulaVersion,
				"currency":         payment.DefaultPaymentCurrency,
				"intent":           service.SubscriptionIntentPurchase,
			},
		}).
		SetStatus(service.OrderStatusPending).
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, paymentSvc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		TradeNo: "provider-" + uuid.NewString(),
		OrderID: outTradeNo,
		Amount:  quote.Price,
		Status:  payment.NotificationStatusSuccess,
	}, payment.TypeAlipay))

	gotOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, gotOrder.Status)

	sub, err := NewUserSubscriptionRepository(client).GetActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, sub.GroupID, "自定义订阅回调建出的卡无 group（domain group_id=0）")
	require.NotNil(t, sub.DailyLimitUSD)
	require.NotNil(t, sub.WeeklyLimitUSD)
	require.NotNil(t, sub.MonthlyLimitUSD)
	require.InDelta(t, dailyAmount, *sub.DailyLimitUSD, 1e-9)
	require.InDelta(t, weeklyLimit, *sub.WeeklyLimitUSD, 1e-9)
	require.InDelta(t, monthlyLimit, *sub.MonthlyLimitUSD, 1e-9)
	require.Equal(t, service.SubscriptionStatusActive, sub.Status)

	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 300, gotUser.Balance, 1e-9, "订阅履约只建卡，不动钱包余额")
}

// 转套餐履约幂等：重放（订单复位 PAID 再履约）不得再关一次卡 / 再开一张新卡。
func TestPaymentSubscriptionFulfillment_ChangePlanIntentIdempotentOnReplayPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("lifecycle-change-idem-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "lifecycle-change-idem-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	dOld := 8.0
	wOld, mOld := service.DeriveWindowCaps(dOld, 30)
	oldExpireDay := today + 10
	oldCard := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dOld,
		DailyLimitUSD:   &dOld,
		WeeklyLimitUSD:  &wOld,
		MonthlyLimitUSD: &mOld,
		TodayRemaining:  dOld,
		TodayDay:        today,
		StartDay:        today - 1,
		ExpireDay:       oldExpireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(oldExpireDay),
		Status:          service.SubscriptionStatusActive,
	})

	dNew := 15.0
	orderID := createPaidLifecycleOrderForIntegration(t, client, user, service.SubscriptionIntentChangePlan, oldCard.ID, dNew, 30, 130)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	firstActive, err := NewUserSubscriptionRepository(client).GetActiveByUserID(ctx, user.ID)
	require.NoError(t, err)

	// 重放：复位 PAID 再次履约 → SUBSCRIPTION_SUCCESS 审计拦截，不得再换一次卡。
	_, err = client.PaymentOrder.UpdateOneID(orderID).SetStatus(service.OrderStatusPaid).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusExpired))
	secondActive, err := NewUserSubscriptionRepository(client).GetActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, firstActive.ID, secondActive.ID, "重放不得再开一张新卡")
}
