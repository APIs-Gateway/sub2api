//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// CreateOrder 续费/转套餐下单链路（money-path 的下单 leg）：走 validateSubOrder → validateSubRenewOrder/
// validateSubChangePlanOrder 解析后端权威 spec → createOrderInTx 冻结快照并落 PENDING 单 → invokeProvider。
//
// 集成环境没有注册真实支付 provider（invokeProvider 用 provider.CreateProvider 工厂按配置造 provider，
// 空配置必然 misconfigured 报错——见 subscription_lifecycle_fulfillment 测试注释里的同一限制），
// 因此 CreateOrder 会在【建单/快照冻结之后】于 provider 步骤失败。这恰好让我们断言下单 leg 的权威契约：
// 订单已按权威 spec 落库（intent/charge_amount 冻结进快照）、随后被标记 failed（不丢单）。

// findSingleSubscriptionOrder 取用户唯一的订阅订单（断言下单确实落库）。
func findSingleSubscriptionOrder(t *testing.T, client *dbent.Client, userID int64) *dbent.PaymentOrder {
	t.Helper()
	order, err := client.PaymentOrder.Query().
		Where(paymentorder.UserIDEQ(userID), paymentorder.OrderTypeEQ(payment.OrderTypeSubscription)).
		Only(context.Background())
	require.NoError(t, err)
	return order
}

func snapshotIntent(t *testing.T, order *dbent.PaymentOrder) (string, float64) {
	t.Helper()
	require.NotNil(t, order.ProviderSnapshot)
	sub, ok := order.ProviderSnapshot["subscription"].(map[string]any)
	require.True(t, ok, "订单快照必须含 subscription 段")
	intent, _ := sub["intent"].(string)
	charge, _ := sub["charge_amount"].(float64)
	return intent, charge
}

func TestPaymentCreateOrder_RenewIntentFreezesSpecThenPersistsPendingPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("create-renew-%s@example.com", uuid.NewString()),
		Username: "create-renew",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "create-renew-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	dDaily := 10.0
	wLimit, mLimit := service.DeriveWindowCaps(dDaily, 30)
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dDaily,
		DailyLimitUSD:   &dDaily,
		WeeklyLimitUSD:  &wLimit,
		MonthlyLimitUSD: &mLimit,
		TodayRemaining:  dDaily,
		TodayDay:        today,
		StartDay:        today - 3,
		ExpireDay:       today + 5,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 5),
		Status:          service.SubscriptionStatusActive,
	})

	// 走到 provider 才失败 → 证明 validateSubRenewOrder + createOrderInTx 已执行。
	_, err := paymentSvc.CreateOrder(ctx, service.CreateOrderRequest{
		UserID:             user.ID,
		OrderType:          payment.OrderTypeSubscription,
		SubscriptionIntent: service.SubscriptionIntentRenew,
		ValidityDays:       30,
		// 用非可见方法(easypay)绕开 visibleMethodLoadBalancer 的方法配置闸，让选实例直达测试 LB →
		// 走完 createOrderInTx 落单，再于 invokeProvider(provider 空配置)失败。
		PaymentType: payment.TypeEasyPay,
		ClientIP:    "127.0.0.1",
		SrcHost:     "api.example.com",
	})
	require.Error(t, err, "集成环境无真实 provider，应在建单后于 provider 步骤失败")

	order := findSingleSubscriptionOrder(t, client, user.ID)
	require.Equal(t, service.OrderStatusFailed, order.Status, "provider 失败后订单被标 failed，不丢单")
	intent, charge := snapshotIntent(t, order)
	require.Equal(t, service.SubscriptionIntentRenew, intent)
	cfg := service.DefaultSubscriptionPricingConfig()
	require.InDelta(t, cfg.Price(dDaily, 30), charge, 1e-9, "续费实收=后端权威全价 P(D,T')，冻结进快照")
	require.NotNil(t, order.SubscriptionDays)
	require.Equal(t, 30, *order.SubscriptionDays)
}

func TestPaymentCreateOrder_ChangePlanIntentFreezesDiffThenPersistsPendingPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("create-change-%s@example.com", uuid.NewString()),
		Username: "create-change",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "create-change-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	dOld := 10.0
	wOld, mOld := service.DeriveWindowCaps(dOld, 30)
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dOld,
		DailyLimitUSD:   &dOld,
		WeeklyLimitUSD:  &wOld,
		MonthlyLimitUSD: &mOld,
		TodayRemaining:  dOld,
		TodayDay:        today,
		StartDay:        today - 3,
		ExpireDay:       today + 5, // 旧卡剩余天数小 → 升档 diff>0
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 5),
		Status:          service.SubscriptionStatusActive,
	})

	dNew := 20.0
	_, err := paymentSvc.CreateOrder(ctx, service.CreateOrderRequest{
		UserID:             user.ID,
		OrderType:          payment.OrderTypeSubscription,
		SubscriptionIntent: service.SubscriptionIntentChangePlan,
		DailyAmountUSD:     dNew,
		ValidityDays:       30,
		PaymentType:        payment.TypeEasyPay, // 绕开可见方法闸（见 renew 测试注释）
		ClientIP:           "127.0.0.1",
		SrcHost:            "api.example.com",
	})
	require.Error(t, err)

	order := findSingleSubscriptionOrder(t, client, user.ID)
	require.Equal(t, service.OrderStatusFailed, order.Status)
	intent, charge := snapshotIntent(t, order)
	require.Equal(t, service.SubscriptionIntentChangePlan, intent)
	require.Greater(t, charge, 0.0, "转套餐实收=补差价 diff>0，冻结进快照")
}

// 续费下单：无生效卡 → ErrNoActiveSubscription（在 validate 阶段干净拒，不建单）。
func TestPaymentCreateOrder_RenewWithoutActiveCardRejectedPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("create-renew-noactive-%s@example.com", uuid.NewString()),
		Username: "create-renew-noactive",
	})

	_, err := paymentSvc.CreateOrder(ctx, service.CreateOrderRequest{
		UserID:             user.ID,
		OrderType:          payment.OrderTypeSubscription,
		SubscriptionIntent: service.SubscriptionIntentRenew,
		ValidityDays:       30,
		PaymentType:        payment.TypeAlipay,
	})
	require.ErrorIs(t, err, service.ErrNoActiveSubscription)

	count, err := client.PaymentOrder.Query().Where(paymentorder.UserIDEQ(user.ID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "validate 阶段拒单，绝不落库")
}

// 转套餐下单：降档（新档剩余价值 < 旧卡剩余价值 → diff<0）→ ErrChangePlanDowngradeNotAllowed。
func TestPaymentCreateOrder_ChangePlanDowngradeRejectedPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("create-change-down-%s@example.com", uuid.NewString()),
		Username: "create-change-down",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "create-change-down-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	dOld := 30.0
	wOld, mOld := service.DeriveWindowCaps(dOld, 30)
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dOld,
		DailyLimitUSD:   &dOld,
		WeeklyLimitUSD:  &wOld,
		MonthlyLimitUSD: &mOld,
		TodayRemaining:  dOld,
		TodayDay:        today,
		StartDay:        today - 1,
		ExpireDay:       today + 60, // 旧卡剩余价值高 → 降到便宜档 diff<0
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 60),
		Status:          service.SubscriptionStatusActive,
	})

	_, err := paymentSvc.CreateOrder(ctx, service.CreateOrderRequest{
		UserID:             user.ID,
		OrderType:          payment.OrderTypeSubscription,
		SubscriptionIntent: service.SubscriptionIntentChangePlan,
		DailyAmountUSD:     5.0,
		ValidityDays:       30,
		PaymentType:        payment.TypeAlipay,
	})
	require.ErrorIs(t, err, service.ErrChangePlanDowngradeNotAllowed)
}
