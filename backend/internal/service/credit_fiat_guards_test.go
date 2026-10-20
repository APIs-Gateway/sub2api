//go:build unit

package service

import "testing"

// 定价配置被改坏时（例如单价上下限都填成 0），UnitPrice 会算出 0。
// 这种卡不能登记进折算器：登记了就会让用量页显示「这笔消费花了 0 元」，
// 比显示一个偏高的估算更糟——用户会以为消费是免费的。
// 正确行为是放弃登记，让该卡的记录回落到钱包单价。
func TestRegisterSubscription_SkipsNonPositiveUnitPrice(t *testing.T) {
	rate := NewCreditFiatRate(13)
	broken := SubscriptionPricingConfig{DMin: 1, DMax: 100, UMin: 0, UMax: 0}

	if unit := broken.UnitPrice(20); unit != 0 {
		t.Fatalf("前置条件不成立：这份坏配置本应算出 0 单价, got %v", unit)
	}

	rate.RegisterSubscription(202, 20, broken)

	got := rate.FiatPerCredit(BillingTypeSubscription, 202)
	if !approx(got, 1.0/13.0, 1e-9) {
		t.Errorf("FiatPerCredit=%v want≈%v（回落到钱包单价）", got, 1.0/13.0)
	}
}

// Convert 在拿不到正单价时返回 0，而不是负数或 NaN。
// 零值 CreditFiatRate（绕过 NewCreditFiatRate 直接 &CreditFiatRate{} 得到的）
// 钱包单价就是 0，这条守卫保证它不会把金额算成奇怪的值渗进接口响应。
func TestConvert_NonPositiveUnitYieldsZero(t *testing.T) {
	var rate CreditFiatRate

	if got := rate.Convert(5, BillingTypeBalance, 0); got != 0 {
		t.Errorf("钱包记录 Convert=%v want 0", got)
	}
	if got := rate.Convert(5, BillingTypeSubscription, 202); got != 0 {
		t.Errorf("订阅记录 Convert=%v want 0", got)
	}
}
