package service

import "testing"

// 生产当前的定价端点：D∈[30,420]、D≥210 起吃最低单价、u∈[0.04,0.05]。
func prodPricingConfig() SubscriptionPricingConfig {
	return SubscriptionPricingConfig{
		DMin:   30,
		DFloor: 210,
		DMax:   420,
		UMax:   0.05,
		UMin:   0.04,
		TMin:   30,
		TMax:   360,
		TStep:  30,
	}
}

// 钱包额度按充值倍率折算：付 1 元得 13 额度 ⇒ 1 额度值 1/13 元。
// 这条是整个功能的出发点——用户看到「扣了 5」，真实花费只有约 0.385 元。
func TestCreditFiatRate_WalletUsesRechargeMultiplier(t *testing.T) {
	rate := NewCreditFiatRate(13)

	if got := rate.FiatPerCredit(BillingTypeBalance, 0); !approx(got, 1.0/13.0, 1e-9) {
		t.Errorf("wallet FiatPerCredit=%v want≈%v", got, 1.0/13.0)
	}
	if got := rate.Convert(5, BillingTypeBalance, 0); !approx(got, 0.38461538, 1e-8) {
		t.Errorf("Convert(5, wallet)=%v want≈0.38461538", got)
	}
}

// 订阅额度按该卡的 u(D) 折算，而不是充值价。这是本功能的核心：
// 一律按 1/13 折算会把订阅用户的花费高估 54%~92%。
func TestCreditFiatRate_SubscriptionUsesCardUnitPrice(t *testing.T) {
	cfg := prodPricingConfig()
	rate := NewCreditFiatRate(13)
	rate.RegisterSubscription(101, 30, cfg)  // 最小档 u=0.05
	rate.RegisterSubscription(102, 210, cfg) // 最低单价档 u=0.04

	cases := []struct {
		name    string
		subID   int64
		credits float64
		want    float64
	}{
		{"D=30 档", 101, 5, 0.25},
		{"D=210 档", 102, 5, 0.20},
	}
	for _, tc := range cases {
		got := rate.Convert(tc.credits, BillingTypeSubscription, tc.subID)
		if !approx(got, tc.want, 1e-9) {
			t.Errorf("%s: Convert(%v)=%v want≈%v", tc.name, tc.credits, got, tc.want)
		}
	}

	// 订阅必须比充值便宜，否则「优惠」就名不副实了。
	wallet := rate.Convert(5, BillingTypeBalance, 0)
	if sub := rate.Convert(5, BillingTypeSubscription, 101); sub >= wallet {
		t.Errorf("订阅折算 %v 不应贵于钱包折算 %v", sub, wallet)
	}
}

// 未登记的卡（已删除、查库失败等）回落到钱包单价，而不是折算成 0。
func TestCreditFiatRate_UnknownSubscriptionFallsBackToWallet(t *testing.T) {
	rate := NewCreditFiatRate(13)

	got := rate.Convert(5, BillingTypeSubscription, 999)
	if !approx(got, 0.38461538, 1e-8) {
		t.Errorf("未登记卡应回落到钱包价, got=%v", got)
	}
}

// billing_type 标了订阅但没有卡号的记录同样按钱包处理。
func TestCreditFiatRate_SubscriptionWithoutIDUsesWallet(t *testing.T) {
	cfg := prodPricingConfig()
	rate := NewCreditFiatRate(13)
	rate.RegisterSubscription(101, 30, cfg)

	if got := rate.Convert(5, BillingTypeSubscription, 0); !approx(got, 0.38461538, 1e-8) {
		t.Errorf("无卡号的订阅记录应走钱包价, got=%v", got)
	}
}

// 倍率缺失/损坏时折算退化为恒等（1 额度 = 1 法币），绝不能除以 0 或产生负价。
func TestCreditFiatRate_InvalidMultiplierDegradesToIdentity(t *testing.T) {
	for _, m := range []float64{0, -13} {
		rate := NewCreditFiatRate(m)
		if got := rate.Convert(5, BillingTypeBalance, 0); !approx(got, 5, 1e-9) {
			t.Errorf("multiplier=%v: Convert(5)=%v want 5（恒等）", m, got)
		}
	}
}

// 非法的每日额度不登记，对应记录自动回落到钱包价。
func TestCreditFiatRate_RegisterRejectsInvalidDailyAmount(t *testing.T) {
	cfg := prodPricingConfig()
	rate := NewCreditFiatRate(13)
	rate.RegisterSubscription(101, 0, cfg)
	rate.RegisterSubscription(102, -30, cfg)
	rate.RegisterSubscription(0, 30, cfg) // 卡号非法

	for _, subID := range []int64{101, 102} {
		if got := rate.Convert(5, BillingTypeSubscription, subID); !approx(got, 0.38461538, 1e-8) {
			t.Errorf("subID=%d 不该被登记, got=%v", subID, got)
		}
	}
	if len(rate.subFiatPerCredit) != 0 {
		t.Errorf("非法输入不应写入登记表, got %v", rate.subFiatPerCredit)
	}
}

// 零额度与 nil 折算器都要安全返回 0，不能 panic。
func TestCreditFiatRate_ZeroAndNilAreSafe(t *testing.T) {
	rate := NewCreditFiatRate(13)
	if got := rate.Convert(0, BillingTypeBalance, 0); got != 0 {
		t.Errorf("Convert(0)=%v want 0", got)
	}

	var nilRate *CreditFiatRate
	if got := nilRate.Convert(5, BillingTypeBalance, 0); got != 0 {
		t.Errorf("nil 折算器 Convert=%v want 0", got)
	}
	if got := nilRate.FiatPerCredit(BillingTypeBalance, 0); got != 0 {
		t.Errorf("nil 折算器 FiatPerCredit=%v want 0", got)
	}
	nilRate.RegisterSubscription(1, 30, prodPricingConfig()) // 不能 panic
}
