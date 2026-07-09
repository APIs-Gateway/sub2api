//go:build integration

package repository

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 订阅订单退款的 money-path（真实 DB + 真实 SubscriptionService）:PrepareRefund 解析权威退款额/天数 →
// ExecuteRefund 关卡(保留行) → gwRefund 因 provider 空配置失败 → handleGwFail → RollbackRefund 还原卡。
// 一条链覆盖 calculateSubscriptionRefundAmount / subscriptionForRefund / prepDeduct /
// closeSubscriptionForRefund / restoreSubscriptionForRefund 的真实事务路径。

func makeRefundPaymentServiceForIntegration(t *testing.T) *service.PaymentService {
	t.Helper()
	client := testEntClient(t)
	groupRepo := NewGroupRepository(client, integrationDB)
	subSvc := service.NewSubscriptionService(
		groupRepo,
		NewUserSubscriptionRepository(client),
		nil, nil, nil,
		client,
		nil, nil,
	)
	return service.NewPaymentService(
		client, nil,
		subscriptionOrderTestLoadBalancer{}, // GetInstanceConfig 返回空配置 → 退款 provider 构造失败 → 触发回滚
		nil, subSvc,
		service.NewPaymentConfigService(client, nil, nil),
		NewUserRepository(client, integrationDB),
		groupRepo, nil,
	)
}

func TestPaymentRefundSubscription_GatewayFailureRollsBackCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paySvc := makeRefundPaymentServiceForIntegration(t)

	// 退款可用的 provider 实例(空配置 → gwRefund 必失败 → 触发回滚还原)。
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("refund-" + uuid.NewString()).
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	instID := strconv.FormatInt(inst.ID, 10)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("refund-sub-%s@example.com", uuid.NewString()),
		Username: "refund-sub",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "refund-sub-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	originalExpireDay := today + 15
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  d,
		DailyLimitUSD:   &d,
		WeeklyLimitUSD:  &w,
		MonthlyLimitUSD: &m,
		TodayRemaining:  d,
		TodayDay:        today,
		StartDay:        today - 15,
		ExpireDay:       originalExpireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(originalExpireDay),
		Status:          service.SubscriptionStatusActive,
	})

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("REFUND-" + uuid.NewString()).
		SetOutTradeNo("refund_" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			"subscription": map[string]any{
				"daily_amount_usd": d,
				"validity_days":    30.0,
				"subscription_id":  card.ID, // 让 subscriptionForRefund 走 GetByID 命中真卡
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	// PrepareRefund(deduct=true):订阅默认退款额按剩余天数算,扣减计划锁定到该卡。
	plan, result, err := paySvc.PrepareRefund(ctx, order.ID, 0, "test refund", false, true)
	require.NoError(t, err)
	require.Nil(t, result, "prepare 阶段不应直接产出失败结果")
	require.NotNil(t, plan)
	require.Equal(t, payment.DeductionTypeSubscription, plan.DeductionType)
	require.Equal(t, card.ID, plan.SubscriptionID)

	// ExecuteRefund:关卡 → gwRefund(空配置)失败 → 回滚还原卡。
	execResult, execErr := paySvc.ExecuteRefund(ctx, plan)
	// 网关失败属预期:要么返回 error,要么 result.Success=false——无论哪种,卡必须被还原。
	if execErr == nil {
		require.NotNil(t, execResult)
		require.False(t, execResult.Success, "provider 空配置应导致退款网关失败")
	}

	// 回滚后卡恢复 active + 还原到原 expire_day(close→restore 事务路径都已执行)。
	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status, "网关失败回滚后卡必须还原为 active")
	require.Equal(t, originalExpireDay, got.ExpireDay, "回滚还原原始到期日")
}

func TestPaymentRefundSubscription_RenewRefundDeductsOnlyRenewalDaysPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paySvc := makeRefundPaymentServiceForIntegration(t)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("refund-renew-" + uuid.NewString()).
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	instID := strconv.FormatInt(inst.ID, 10)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("refund-renew-%s@example.com", uuid.NewString()),
		Username: "refund-renew",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "refund-renew-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	originalExpireDay := today + 15
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
		ExpireDay:       originalExpireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(originalExpireDay),
		Status:          service.SubscriptionStatusActive,
	})

	const renewDays = 60
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(600).
		SetPayAmount(600).
		SetFeeRate(0).
		SetRechargeCode("REFUND-RENEW-" + uuid.NewString()).
		SetOutTradeNo("refund_renew_" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(""). // ExecuteRefund skips gateway call; this test focuses on local deduction.
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(service.OrderStatusPaid).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetSubscriptionDays(renewDays).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			"subscription": map[string]any{
				"daily_amount_usd":       d,
				"validity_days":          float64(renewDays),
				"intent":                 service.SubscriptionIntentRenew,
				"target_subscription_id": card.ID,
				"charge_amount":          600.0,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, paySvc.ExecuteSubscriptionFulfillment(ctx, order.ID))
	renewed, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, originalExpireDay+renewDays, renewed.ExpireDay)
	require.Equal(t, service.SubscriptionStatusActive, renewed.Status)

	plan, result, err := paySvc.PrepareRefund(ctx, order.ID, 0, "refund renewal", false, false)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, payment.DeductionTypeSubscription, plan.DeductionType)
	require.Equal(t, card.ID, plan.SubscriptionID)
	require.Equal(t, renewDays, plan.SubDaysToDeduct)

	execResult, execErr := paySvc.ExecuteRefund(ctx, plan)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.True(t, execResult.Success)

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status, "续费退款只扣回本次续费天数,原服务期仍 active")
	require.Equal(t, originalExpireDay, got.ExpireDay, "续费退款后恢复到续费前的最后服务日")
	require.True(t, got.ExpiresAt.Equal(service.ExpireDayToExpiresAt(originalExpireDay)))
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))

	gotOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRefunded, gotOrder.Status)
}

// 订阅订单退款即使 deduct_balance=false 也必须规划关卡(P1 资损回归,规格 §6/§8#20「退款即关卡,无条件」):
// 修复前 deduct=false → DeductionType 恒 None → ExecuteRefund 关卡门不成立、gwRefund 仍无条件退法币
// → 用户拿到现金退款却仍持 active 卡继续用。此测试断言 deduct=false 时 PrepareRefund 仍产出
// DeductionType=Subscription + 锁定到该卡的关卡计划。
func TestPaymentRefundSubscription_DeductFalseStillPlansCardClosePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paySvc := makeRefundPaymentServiceForIntegration(t)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("refund-nodeduct-" + uuid.NewString()).
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	instID := strconv.FormatInt(inst.ID, 10)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("refund-nodeduct-%s@example.com", uuid.NewString()),
		Username: "refund-nodeduct",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "refund-nodeduct-" + uuid.NewString()})
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
		ExpireDay:       today + 15,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 15),
		Status:          service.SubscriptionStatusActive,
	})

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("REFUND-ND-" + uuid.NewString()).
		SetOutTradeNo("refund_nd_" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-nd-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			"subscription": map[string]any{
				"daily_amount_usd": d,
				"validity_days":    30.0,
				"subscription_id":  card.ID,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	// 关键:deduct=false。修复后订阅单仍须无条件规划关卡。
	plan, result, err := paySvc.PrepareRefund(ctx, order.ID, 0, "test refund", false, false)
	require.NoError(t, err)
	require.Nil(t, result, "prepare 阶段不应直接产出失败结果")
	require.NotNil(t, plan)
	require.Equal(t, payment.DeductionTypeSubscription, plan.DeductionType,
		"deduct_balance=false 时订阅单仍须 DeductionType=Subscription(关卡),否则退现金不关卡")
	require.Equal(t, card.ID, plan.SubscriptionID, "关卡计划须锁定到该订单对应的卡")
	require.Greater(t, plan.SubDaysToDeduct, 0, "须规划扣减剩余天数(关卡)")
}
