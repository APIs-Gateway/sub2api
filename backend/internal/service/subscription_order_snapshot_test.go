//go:build unit

package service

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

// 下单冻结订阅定价快照 → 回调按快照回读发卡（D/T 不重算）。覆盖正常冻结/回读 + 老订单/非法值回退。
func TestSubscriptionOrderSnapshot_FreezeAndRead(t *testing.T) {
	plan := &dbent.SubscriptionPlan{DailyAmountUsd: 10, ValidityDays: 30, ValidityUnit: "day", Price: 545}
	base := map[string]any{"currency": "CNY"}
	snap := buildSubscriptionOrderSnapshot(plan, base)

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
	d, days, ok := readSubscriptionSnapshotDT(order)
	require.True(t, ok)
	require.InDelta(t, 10, d, 1e-9)
	require.Equal(t, 30, days)

	// 老订单（无 subscription 快照）→ ok=false，调用方走 group 回退。
	_, _, ok = readSubscriptionSnapshotDT(&dbent.PaymentOrder{})
	require.False(t, ok)
	_, _, ok = readSubscriptionSnapshotDT(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{"currency": "CNY"}})
	require.False(t, ok)

	// 非法值（D<=0）→ ok=false。
	bad := &dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 0.0, "validity_days": 30.0},
	}}
	_, _, ok = readSubscriptionSnapshotDT(bad)
	require.False(t, ok)
}
