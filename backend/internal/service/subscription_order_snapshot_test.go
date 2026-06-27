//go:build unit

package service

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

// 下单冻结订阅定价快照 → 回调按快照回读发卡（D/T 不重算）。覆盖正常冻结/回读 + 老订单兼容 + 坏快照拒绝。
func TestSubscriptionOrderSnapshot_FreezeAndRead(t *testing.T) {
	plan := &dbent.SubscriptionPlan{DailyAmountUsd: 10, ValidityDays: 30, ValidityUnit: "day", Price: 545}
	base := map[string]any{"currency": "CNY"}
	spec := &subscriptionOrderSpec{
		plan:         plan,
		dailyAmount:  plan.DailyAmountUsd,
		validityDays: psComputeValidityDays(plan.ValidityDays, plan.ValidityUnit),
		groupID:      plan.GroupID,
		unitPrice:    DefaultSubscriptionPricingConfig().UnitPrice(plan.DailyAmountUsd),
		price:        plan.Price,
	}
	snap := buildSubscriptionOrderSnapshot(spec, base)

	require.Equal(t, 10.0, snap["daily_amount_usd"])
	require.Equal(t, psComputeValidityDays(30, "day"), snap["validity_days"])
	require.Equal(t, 545.0, snap["price"])
	require.Equal(t, SubscriptionFormulaVersion, snap["formula_version"])
	require.Equal(t, "CNY", snap["currency"])
	require.InDelta(t, DefaultSubscriptionPricingConfig().UnitPrice(10), snap["unit_price"].(float64), 1e-9)

	// JSON 回读：jsonb 数值回读为 float64。
	order := &dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 10.0, "validity_days": 30.0},
	}}
	d, days, present, err := readSubscriptionSnapshotDT(order)
	require.NoError(t, err)
	require.True(t, present)
	require.InDelta(t, 10, d, 1e-9)
	require.Equal(t, 30, days)
	withID, ok := withSubscriptionIDInSnapshot(order.ProviderSnapshot, 123)
	require.True(t, ok)
	order.ProviderSnapshot = withID
	subID, ok := readSubscriptionSnapshotSubscriptionID(order)
	require.True(t, ok)
	require.Equal(t, int64(123), subID)

	// 老订单（无 subscription 快照）→ ok=false，调用方走 group 回退。
	_, _, present, err = readSubscriptionSnapshotDT(&dbent.PaymentOrder{})
	require.NoError(t, err)
	require.False(t, present)
	_, _, present, err = readSubscriptionSnapshotDT(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{"currency": "CNY"}})
	require.NoError(t, err)
	require.False(t, present)

	// 非法值（D<=0）→ present=true + error，不能伪装成老订单回退 group。
	bad := &dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 0.0, "validity_days": 30.0},
	}}
	_, _, present, err = readSubscriptionSnapshotDT(bad)
	require.Error(t, err)
	require.True(t, present)
}

// 坏快照的更多边界：均为 present=true + error（新订单快照损坏必须失败，绝不回退 group）。
func TestSubscriptionOrderSnapshot_CorruptVariants(t *testing.T) {
	cases := []struct {
		name string
		sub  any
		msg  string
	}{
		{"非 map 类型", "not-an-object", "expected object"},
		{"缺 validity_days", map[string]any{"daily_amount_usd": 10.0}, "invalid D/T"},
		{"缺 daily_amount_usd", map[string]any{"validity_days": 30.0}, "invalid D/T"},
		{"validity_days 非整数", map[string]any{"daily_amount_usd": 10.0, "validity_days": 30.5}, "integer"},
		{"T<=0", map[string]any{"daily_amount_usd": 10.0, "validity_days": 0.0}, "invalid D/T"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			order := &dbent.PaymentOrder{ProviderSnapshot: map[string]any{subscriptionSnapshotKey: c.sub}}
			_, _, present, err := readSubscriptionSnapshotDT(order)
			require.True(t, present, "损坏快照必须 present=true，不能伪装成老订单")
			require.Error(t, err)
			require.Contains(t, err.Error(), c.msg)
		})
	}

	// int/float 变体应被容忍（jsonb 多回读 float64，但容忍整型保险）。
	order := &dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 10, "validity_days": int64(30)},
	}}
	d, days, present, err := readSubscriptionSnapshotDT(order)
	require.NoError(t, err)
	require.True(t, present)
	require.InDelta(t, 10, d, 1e-9)
	require.Equal(t, 30, days)
}
