//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Kyren order.refunded webhook 的 money-path(真实 DB + 真实 SubscriptionService):
// 找订单 → 订阅单退订关卡(PARTIAL 也关)→ 标订单 refunded → evt_id 幂等去重。
// 验签是 handler 层职责(已在 payment 包单测覆盖),此处直接喂已验签载荷给 service。

func TestHandleKyrenRefundWebhook_PartialStillClosesCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paySvc := makeRefundPaymentServiceForIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("kyren-refund-%s@example.com", uuid.NewString()),
		Username: "kyren-refund",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "kyren-refund-" + uuid.NewString()})
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
		StartDay:        today - 5,
		ExpireDay:       today + 25,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 25),
		Status:          service.SubscriptionStatusActive,
	})

	outTradeNo := "kyren_" + uuid.NewString()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("KYREN-" + uuid.NewString()).
		SetOutTradeNo(outTradeNo).
		SetPaymentType(payment.TypeEasyPay).
		SetPaymentTradeNo("trade-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderKey(payment.TypeEasyPay).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"schema_version": 2,
			"provider_key":   payment.TypeEasyPay,
			"subscription": map[string]any{
				"daily_amount_usd": d,
				"validity_days":    30.0,
				"subscription_id":  card.ID,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	// 关键:refund_status=PARTIAL,data.order_id=out_trade_no。退订关卡须无条件执行。
	data := &payment.KyrenRefundData{
		OrderID:        outTradeNo,
		RefundID:       "refund_" + uuid.NewString(),
		RefundStatus:   payment.KyrenRefundStatusPartial,
		Amount:         "180.00",
		RefundedAmount: "180.00",
		OriginalAmount: "300.00",
		Reason:         "customer_request",
	}
	require.NoError(t, paySvc.HandleKyrenRefundWebhook(ctx, data, "evt_kyren_1"))

	// 卡被关(status=expired,不再 active)。
	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.NotEqual(t, service.SubscriptionStatusActive, got.Status, "PARTIAL 退款也必须关卡(退订)")
	_, activeErr := NewUserSubscriptionRepository(client).GetActiveByUserID(ctx, user.ID)
	require.Error(t, activeErr, "关卡后不应再有生效卡")

	// 订单标记 refunded。
	gotOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRefunded, gotOrder.Status)

	// 幂等:同一 evt 再推一次 → 不报错、状态不变。
	require.NoError(t, paySvc.HandleKyrenRefundWebhook(ctx, data, "evt_kyren_1"))
	gotOrder2, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRefunded, gotOrder2.Status)
}

// #4:easypay 订单的后台「退款」按钮 → 只置「待退款」(REFUND_REQUESTED),不调网关、不关卡。
// 实退在 Kyren 控制台做,order.refunded webhook 回来才关卡(见上面的 webhook 测试)。
func TestPrepareRefund_EasyPayMarksRefundRequestedNotGatewayPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paySvc := makeRefundPaymentServiceForIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("easypay-pending-%s@example.com", uuid.NewString()),
		Username: "easypay-pending",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "easypay-pending-" + uuid.NewString()})
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
		StartDay:        today - 5,
		ExpireDay:       today + 25,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 25),
		Status:          service.SubscriptionStatusActive,
	})

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("EASYPAY-" + uuid.NewString()).
		SetOutTradeNo("easypay_" + uuid.NewString()).
		SetPaymentType(payment.TypeEasyPay).
		SetPaymentTradeNo("trade-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderKey(payment.TypeEasyPay).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"schema_version": 2,
			"provider_key":   payment.TypeEasyPay,
			"subscription":   map[string]any{"daily_amount_usd": d, "validity_days": 30.0, "subscription_id": card.ID},
		}).
		Save(ctx)
	require.NoError(t, err)

	plan, result, err := paySvc.PrepareRefund(ctx, order.ID, 0, "user asked", false, true)
	require.NoError(t, err)
	require.Nil(t, plan, "easypay 不产出退款计划(不走网关)")
	require.NotNil(t, result)
	require.True(t, result.RefundRequested, "easypay 退款应标记为待退款")

	// 订单置 REFUND_REQUESTED;卡仍 active(webhook 回来才关)。
	gotOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRefundRequested, gotOrder.Status)
	gotCard, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotCard.Status, "待退款阶段不关卡(由 webhook 关)")
}

// 未知订单 → ErrOrderNotFound(handler 据此 ack 2xx 停止 Kyren 重推)。
func TestHandleKyrenRefundWebhook_UnknownOrderPostgres(t *testing.T) {
	ctx := context.Background()
	paySvc := makeRefundPaymentServiceForIntegration(t)
	data := &payment.KyrenRefundData{
		OrderID:      "nonexistent_" + uuid.NewString(),
		RefundID:     "refund_x",
		RefundStatus: payment.KyrenRefundStatusFull,
	}
	err := paySvc.HandleKyrenRefundWebhook(ctx, data, "evt_unknown")
	require.ErrorIs(t, err, service.ErrOrderNotFound)
}
