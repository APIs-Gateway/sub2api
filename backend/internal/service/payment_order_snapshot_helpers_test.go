//go:build unit

package service

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// 订单订阅快照纯函数（snapshot 读写 / intent 归一化 / 数值强转）的分支穷尽单测。
// 这些是履约前权威参数的"冻结/回读"地基，防御性分支若回归会导致按错误参数发卡（资损）。

func TestNormalizeSubscriptionIntent_AllBranches(t *testing.T) {
	cases := []struct {
		raw      string
		wantNorm string
		wantOK   bool
	}{
		{"", SubscriptionIntentPurchase, true},
		{"  ", SubscriptionIntentPurchase, true}, // TrimSpace 后为空 → purchase
		{SubscriptionIntentPurchase, SubscriptionIntentPurchase, true},
		{SubscriptionIntentRenew, SubscriptionIntentRenew, true},
		{SubscriptionIntentChangePlan, SubscriptionIntentChangePlan, true},
		{"bogus", "", false}, // default 分支
	}
	for _, tc := range cases {
		norm, ok := normalizeSubscriptionIntent(tc.raw)
		require.Equalf(t, tc.wantOK, ok, "raw=%q", tc.raw)
		require.Equalf(t, tc.wantNorm, norm, "raw=%q", tc.raw)
	}
}

func TestReadSubscriptionIntent_DefensiveBranches(t *testing.T) {
	// nil order → purchase, 0
	intent, target := readSubscriptionIntent(nil)
	require.Equal(t, SubscriptionIntentPurchase, intent)
	require.Zero(t, target)

	// 有 snapshot 但无 subscription key → purchase, 0
	intent, target = readSubscriptionIntent(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{"other": 1}})
	require.Equal(t, SubscriptionIntentPurchase, intent)
	require.Zero(t, target)

	// subscription 值不是 map → purchase, 0
	intent, target = readSubscriptionIntent(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{subscriptionSnapshotKey: "not-a-map"}})
	require.Equal(t, SubscriptionIntentPurchase, intent)
	require.Zero(t, target)

	// 非法 intent 字段 → 回退 purchase（但 target 仍解析）
	intent, target = readSubscriptionIntent(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"intent": "garbage", "target_subscription_id": int64(42)},
	}})
	require.Equal(t, SubscriptionIntentPurchase, intent)
	require.Equal(t, int64(42), target)

	// 合法 renew + target
	intent, target = readSubscriptionIntent(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"intent": SubscriptionIntentRenew, "target_subscription_id": float64(7)},
	}})
	require.Equal(t, SubscriptionIntentRenew, intent)
	require.Equal(t, int64(7), target)
}

func TestBuildSubscriptionOrderSnapshot_PurchaseVsRenew(t *testing.T) {
	// purchase：无 target_subscription_id / charge_amount；currency 默认本币
	purchase := buildSubscriptionOrderSnapshot(&subscriptionOrderSpec{
		dailyAmount: 10, validityDays: 30, unitPrice: 1.2, price: 36,
		intent: SubscriptionIntentPurchase,
	}, nil)
	require.Equal(t, payment.DefaultPaymentCurrency, purchase["currency"])
	require.Equal(t, SubscriptionIntentPurchase, purchase["intent"])
	require.Equal(t, SubscriptionFormulaVersion, purchase["formula_version"])
	require.NotContains(t, purchase, "target_subscription_id")
	require.NotContains(t, purchase, "charge_amount")

	// intent 为空 → 归一为 purchase
	empty := buildSubscriptionOrderSnapshot(&subscriptionOrderSpec{dailyAmount: 10, validityDays: 30}, nil)
	require.Equal(t, SubscriptionIntentPurchase, empty["intent"])

	// renew：带 target_subscription_id + charge_amount；currency 取自 base
	renew := buildSubscriptionOrderSnapshot(&subscriptionOrderSpec{
		dailyAmount: 20, validityDays: 30, unitPrice: 1.1, price: 60,
		intent: SubscriptionIntentRenew, targetSubID: 9, chargeAmount: 60,
	}, map[string]any{"currency": "USD"})
	require.Equal(t, "USD", renew["currency"])
	require.Equal(t, int64(9), renew["target_subscription_id"])
	require.Equal(t, 60.0, renew["charge_amount"])
	require.Contains(t, renew, "weekly_limit_usd")
	require.Contains(t, renew, "monthly_limit_usd")
}

func TestReadSubscriptionSnapshotDT_Branches(t *testing.T) {
	// nil → present=false
	_, _, present, err := readSubscriptionSnapshotDT(nil)
	require.False(t, present)
	require.NoError(t, err)

	// 无 key → present=false
	_, _, present, err = readSubscriptionSnapshotDT(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{"x": 1}})
	require.False(t, present)
	require.NoError(t, err)

	// 值非 map → present=true + err
	_, _, present, err = readSubscriptionSnapshotDT(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{subscriptionSnapshotKey: 5}})
	require.True(t, present)
	require.Error(t, err)

	// D/T 非法（D<=0）→ present=true + err
	_, _, present, err = readSubscriptionSnapshotDT(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 0.0, "validity_days": 30.0},
	}})
	require.True(t, present)
	require.Error(t, err)

	// validity_days 非整数 → err
	_, _, present, err = readSubscriptionSnapshotDT(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 10.0, "validity_days": 30.5},
	}})
	require.True(t, present)
	require.Error(t, err)

	// 合法
	d, days, present, err := readSubscriptionSnapshotDT(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"daily_amount_usd": 10.0, "validity_days": 30.0},
	}})
	require.True(t, present)
	require.NoError(t, err)
	require.Equal(t, 10.0, d)
	require.Equal(t, 30, days)
}

func TestReadSubscriptionSnapshotWM_Branches(t *testing.T) {
	_, _, ok := readSubscriptionSnapshotWM(nil)
	require.False(t, ok)

	_, _, ok = readSubscriptionSnapshotWM(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{"x": 1}})
	require.False(t, ok)

	_, _, ok = readSubscriptionSnapshotWM(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{subscriptionSnapshotKey: "str"}})
	require.False(t, ok)

	// W/M 非法（负）→ ok=false
	_, _, ok = readSubscriptionSnapshotWM(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"weekly_limit_usd": -1.0, "monthly_limit_usd": 100.0},
	}})
	require.False(t, ok)

	w, m, ok := readSubscriptionSnapshotWM(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"weekly_limit_usd": 50.0, "monthly_limit_usd": 200.0},
	}})
	require.True(t, ok)
	require.Equal(t, 50.0, w)
	require.Equal(t, 200.0, m)
}

func TestReadSubscriptionSnapshotSubscriptionID_Branches(t *testing.T) {
	_, ok := readSubscriptionSnapshotSubscriptionID(nil)
	require.False(t, ok)

	_, ok = readSubscriptionSnapshotSubscriptionID(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{"x": 1}})
	require.False(t, ok)

	_, ok = readSubscriptionSnapshotSubscriptionID(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{subscriptionSnapshotKey: 9}})
	require.False(t, ok)

	id, ok := readSubscriptionSnapshotSubscriptionID(&dbent.PaymentOrder{ProviderSnapshot: map[string]any{
		subscriptionSnapshotKey: map[string]any{"subscription_id": int64(55)},
	}})
	require.True(t, ok)
	require.Equal(t, int64(55), id)
}

func TestWithSubscriptionIDInSnapshot_Branches(t *testing.T) {
	// subID<=0 → false
	_, ok := withSubscriptionIDInSnapshot(map[string]any{subscriptionSnapshotKey: map[string]any{}}, 0)
	require.False(t, ok)

	// nil snapshot → false
	_, ok = withSubscriptionIDInSnapshot(nil, 1)
	require.False(t, ok)

	// 无 key → false
	_, ok = withSubscriptionIDInSnapshot(map[string]any{"x": 1}, 1)
	require.False(t, ok)

	// 值非 map → false
	_, ok = withSubscriptionIDInSnapshot(map[string]any{subscriptionSnapshotKey: "str"}, 1)
	require.False(t, ok)

	// 合法：返回深拷贝且原 map 不被改写
	orig := map[string]any{
		"currency":              "USD",
		subscriptionSnapshotKey: map[string]any{"intent": SubscriptionIntentRenew},
	}
	next, ok := withSubscriptionIDInSnapshot(orig, 77)
	require.True(t, ok)
	require.Equal(t, int64(77), next[subscriptionSnapshotKey].(map[string]any)["subscription_id"])
	// 原 map 未被污染
	require.NotContains(t, orig[subscriptionSnapshotKey].(map[string]any), "subscription_id")
}

func TestSnapshotFloat64_AllTypes(t *testing.T) {
	cases := []struct {
		v    any
		want float64
		ok   bool
	}{
		{float64(1.5), 1.5, true},
		{float32(2.5), 2.5, true},
		{int(3), 3, true},
		{int64(4), 4, true},
		{int32(5), 5, true},
		{"nope", 0, false},
		{nil, 0, false},
	}
	for _, tc := range cases {
		got, ok := snapshotFloat64(tc.v)
		require.Equalf(t, tc.ok, ok, "v=%v", tc.v)
		if tc.ok {
			require.InDeltaf(t, tc.want, got, 1e-9, "v=%v", tc.v)
		}
	}
}

func TestSnapshotInt64_AllTypes(t *testing.T) {
	cases := []struct {
		v    any
		want int64
		ok   bool
	}{
		{int64(7), 7, true},
		{int(8), 8, true},
		{int32(9), 9, true},
		{float64(10), 10, true},
		{float64(10.5), 10, false}, // 非整 → false
		{float32(11), 11, true},
		{"12", 12, true},
		{" 13 ", 13, true}, // TrimSpace
		{"-1", -1, false},  // i<=0 → false
		{"abc", 0, false},
		{int64(0), 0, false}, // n>0 false
		{nil, 0, false},
	}
	for _, tc := range cases {
		got, ok := snapshotInt64(tc.v)
		require.Equalf(t, tc.ok, ok, "v=%v", tc.v)
		if tc.ok {
			require.Equalf(t, tc.want, got, "v=%v", tc.v)
		}
	}
}
