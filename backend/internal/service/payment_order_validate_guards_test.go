//go:build unit

package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 订阅下单解析（validateSubOrder 及其 intent 分发 / errPendingSubscriptionOrder / 快照回写）的
// fail-fast guard 单测：均在触达 DB / subscriptionSvc 之前返回，可用零值 / sqlite client 覆盖。

func TestValidateSubOrder_GuardBranches(t *testing.T) {
	ctx := context.Background()
	svc := &PaymentService{} // 无 subscriptionSvc

	// 未知 intent → INVALID_SUBSCRIPTION_INTENT。
	_, err := svc.validateSubOrder(ctx, CreateOrderRequest{SubscriptionIntent: "bogus"})
	require.Equal(t, "INVALID_SUBSCRIPTION_INTENT", infraerrors.Reason(err))

	// 自定义购买(PlanID=0 + D/T>0)但 subscriptionSvc 未配 → 报错。
	_, err = svc.validateSubOrder(ctx, CreateOrderRequest{DailyAmountUSD: 10, ValidityDays: 30})
	require.Error(t, err)

	// 购买但既无 PlanID 也无自定义 D/T → INVALID_INPUT（需要套餐）。
	_, err = svc.validateSubOrder(ctx, CreateOrderRequest{})
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))

	// renew intent 但 subscriptionSvc 未配 → 报错（分发到 validateSubRenewOrder 的 nil 闸）。
	_, err = svc.validateSubOrder(ctx, CreateOrderRequest{SubscriptionIntent: SubscriptionIntentRenew, ValidityDays: 30})
	require.Error(t, err)

	// change_plan intent 但 subscriptionSvc 未配 → 报错（分发到 validateSubChangePlanOrder 的 nil 闸）。
	_, err = svc.validateSubOrder(ctx, CreateOrderRequest{SubscriptionIntent: SubscriptionIntentChangePlan, DailyAmountUSD: 20, ValidityDays: 30})
	require.Error(t, err)
}

func TestErrPendingSubscriptionOrder(t *testing.T) {
	require.Equal(t, "PENDING_SUBSCRIPTION_ORDER", infraerrors.Reason(errPendingSubscriptionOrder()))
}

func TestEnsureCanCreateSubscriptionOrder_DependencyGuards(t *testing.T) {
	ctx := context.Background()

	// subscriptionSvc 未配 → 配置错误。
	require.Error(t, (&PaymentService{}).ensureCanCreateSubscriptionOrder(ctx, 1))

	// subscriptionSvc 已配但 entClient 未配 → 配置错误（开事务前返回）。
	require.Error(t, (&PaymentService{subscriptionSvc: &SubscriptionService{}}).ensureCanCreateSubscriptionOrder(ctx, 1))
}

func TestDoSubLifecycle_SnapshotGuards(t *testing.T) {
	ctx := context.Background()
	svc := &PaymentService{} // 触 DB / subscriptionSvc 前返回

	// 快照损坏 → 失败（不能按回调时配置重算）。
	err := svc.doSubLifecycle(ctx, &dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{subscriptionSnapshotKey: "corrupt"},
	}, SubscriptionIntentRenew, 5, &subscriptionFulfillmentLease{})
	require.Error(t, err)

	// 无定价快照（生命周期单必须有冻结 D/T）→ 失败。
	err = svc.doSubLifecycle(ctx, &dbent.PaymentOrder{ID: 7}, SubscriptionIntentRenew, 5, &subscriptionFulfillmentLease{})
	require.Error(t, err)

	// 有快照但无目标卡 ID → 失败。
	err = svc.doSubLifecycle(ctx, &dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{
			subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 10.0, "validity_days": 30.0},
		},
	}, SubscriptionIntentRenew, 0, &subscriptionFulfillmentLease{})
	require.Error(t, err)
}

func TestWriteSubscriptionIDToOrderSnapshot_NoopBranches(t *testing.T) {
	ctx := context.Background()

	// entClient 未配 → no-op（nil）。
	require.NoError(t, (&PaymentService{}).writeSubscriptionIDToOrderSnapshot(ctx, &dbent.PaymentOrder{ID: 1}, 5))

	// 快照无 subscription 段 → withSubscriptionIDInSnapshot 失败 → no-op（不落库、不报错）。
	client := newSubscriptionGuardsTestClient(t)
	svc := &PaymentService{entClient: client}
	order := &dbent.PaymentOrder{ID: 1, ProviderSnapshot: map[string]any{"x": 1}}
	require.NoError(t, svc.writeSubscriptionIDToOrderSnapshot(ctx, order, 5))

	// subscriptionID<=0 → 同样 no-op。
	order2 := &dbent.PaymentOrder{ID: 1, ProviderSnapshot: map[string]any{subscriptionSnapshotKey: map[string]any{}}}
	require.NoError(t, svc.writeSubscriptionIDToOrderSnapshot(ctx, order2, 0))
}
