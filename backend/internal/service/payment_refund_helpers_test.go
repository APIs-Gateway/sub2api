//go:build unit

package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// 退款链路的纯函数 / nil-依赖 guard 单测（触库前返回，无需 DB）。
// 退款金额/天数解析若回归会直接导致多退/少退（资损），分支必须钉死。

func TestSubscriptionOrderOriginalDays_Branches(t *testing.T) {
	// 1) 快照含合法 D/T → 取快照天数（权威，不按订单 subscription_days）。
	days, err := subscriptionOrderOriginalDays(&dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{
			subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 10.0, "validity_days": 30.0},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 30, days)

	// 2) 快照损坏（subscription 段非对象）→ present=true + err，必须失败而非回退。
	_, err = subscriptionOrderOriginalDays(&dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{subscriptionSnapshotKey: "corrupt"},
	})
	require.Error(t, err)

	// 3) 无快照但订单 subscription_days 有值（老订单兼容）→ 回退取之。
	d := 15
	days, err = subscriptionOrderOriginalDays(&dbent.PaymentOrder{SubscriptionDays: &d})
	require.NoError(t, err)
	require.Equal(t, 15, days)

	// 4) 既无快照也无 subscription_days → 报错（无法确定原始有效期）。
	_, err = subscriptionOrderOriginalDays(&dbent.PaymentOrder{})
	require.Error(t, err)
}

func TestSubscriptionForRefund_NilServiceGuard(t *testing.T) {
	_, err := (&PaymentService{}).subscriptionForRefund(context.Background(), &dbent.PaymentOrder{UserID: 1})
	require.Error(t, err)
}

func TestPrepDeduct_SubscriptionOrderWithoutServiceRequiresForce(t *testing.T) {
	ctx := context.Background()
	svc := &PaymentService{} // subscriptionSvc 未配
	order := &dbent.PaymentOrder{OrderType: payment.OrderTypeSubscription, UserID: 1}

	// 非 force：要求 force（不可贸然扣减一个无法定位的订阅）。
	p := &RefundPlan{OrderID: 1, Order: order}
	res := svc.prepDeduct(ctx, order, p, false)
	require.NotNil(t, res)
	require.True(t, res.RequireForce)
	require.Equal(t, payment.DeductionTypeSubscription, p.DeductionType)

	// force：放行（res=nil），扣减类型仍标记为 subscription。
	p2 := &RefundPlan{OrderID: 1, Order: order}
	require.Nil(t, svc.prepDeduct(ctx, order, p2, true))
	require.Equal(t, payment.DeductionTypeSubscription, p2.DeductionType)
}
