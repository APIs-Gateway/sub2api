package service

import (
	"math"
	"testing"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// 规格第 3 节示例表（T=30）：D=30→u=2.000,P=1800；D=270→u=1.500,P=12150；D=510→u=1.000,P=15300。
func TestSubscriptionPricing_SpecExamples(t *testing.T) {
	c := DefaultSubscriptionPricingConfig()
	cases := []struct {
		d     float64
		t     int
		wantU float64
		wantP float64
		uEps  float64
		pEps  float64
	}{
		{30, 30, 2.000, 1800, 1e-9, 1e-9},
		{270, 30, 1.500, 12150, 1e-9, 1e-9},
		{510, 30, 1.000, 15300, 1e-9, 1e-9},
	}
	for _, tc := range cases {
		if u := c.UnitPrice(tc.d); !approx(u, tc.wantU, tc.uEps) {
			t.Errorf("UnitPrice(%v)=%v want≈%v", tc.d, u, tc.wantU)
		}
		if p := c.Price(tc.d, tc.t); !approx(p, tc.wantP, tc.pEps) {
			t.Errorf("Price(%v,%d)=%v want≈%v", tc.d, tc.t, p, tc.wantP)
		}
	}
}

// u(D) 必须随 D 单调不增（量大优惠）。
func TestSubscriptionPricing_UnitPriceMonotoneDecreasing(t *testing.T) {
	c := DefaultSubscriptionPricingConfig()
	prev := math.Inf(1)
	for d := 1.0; d <= 510.0; d += 0.5 {
		u := c.UnitPrice(d)
		if u > prev+1e-12 {
			t.Fatalf("u 非单调递减: D=%v u=%v > prev=%v", d, u, prev)
		}
		prev = u
	}
}

// 越界 D 的 u 必须 clamp 到 [UMin, UMax]。
func TestSubscriptionPricing_UnitPriceClamp(t *testing.T) {
	c := DefaultSubscriptionPricingConfig()
	if u := c.UnitPrice(0.0); !approx(u, c.UMax, 1e-12) { // 低于 DMin → 最贵档
		t.Errorf("UnitPrice(0)=%v want UMax=%v", u, c.UMax)
	}
	if u := c.UnitPrice(-5); !approx(u, c.UMax, 1e-12) {
		t.Errorf("UnitPrice(-5)=%v want UMax=%v", u, c.UMax)
	}
	if u := c.UnitPrice(600); !approx(u, c.UMin, 1e-12) { // 高于 DMax → 最便宜档
		t.Errorf("UnitPrice(600)=%v want UMin=%v", u, c.UMin)
	}
}

// DMax==DMin 退化（无梯度）：任何 D 都按最贵档 UMax，且不 panic。
func TestSubscriptionPricing_DegenerateRange(t *testing.T) {
	c := SubscriptionPricingConfig{DMin: 30, DMax: 30, UMax: 2.0, UMin: 1.0, TMin: 30, TMax: 90}
	if u := c.UnitPrice(30); !approx(u, 2.0, 1e-12) {
		t.Errorf("退化区间 UnitPrice=%v want 2.0", u)
	}
}

func TestSubscriptionPricing_UnitPriceUsesFineScale(t *testing.T) {
	c := SubscriptionPricingConfig{DMin: 30, DMax: 500, UMax: 0.07, UMin: 0.04, TMin: 30, TMax: 360, TStep: 30}
	if u := c.UnitPrice(40); !approx(u, 0.0694, 1e-12) {
		t.Fatalf("UnitPrice(40)=%v want 0.0694", u)
	}
	if p := c.Price(40, 30); !approx(p, 83.28, 1e-12) {
		t.Fatalf("Price(40,30)=%v want 83.28", p)
	}
}

func TestSubscriptionPricing_ValidateCustom(t *testing.T) {
	c := DefaultSubscriptionPricingConfig()
	ok := []struct {
		d float64
		t int
	}{{30, 30}, {510, 360}, {270, 60}}
	for _, x := range ok {
		if err := c.ValidateCustom(x.d, x.t); err != nil {
			t.Errorf("ValidateCustom(%v,%d) 应通过, got %v", x.d, x.t, err)
		}
	}
	bad := []struct {
		d float64
		t int
	}{
		{29.9, 30},        // D 太小
		{40, 30},          // D 非 30 的整数倍
		{511, 30},         // D 太大
		{30, 29},          // T 太短（30 起买）
		{30, 361},         // T 太长
		{30, 45},          // T 非整月（在 [30,360] 内但非 30 的倍数）
		{30, 75},          // 同上：75 也非整月
		{math.NaN(), 30},  // 非法 D
		{math.Inf(1), 30}, // 非法 D
	}
	for _, x := range bad {
		if err := c.ValidateCustom(x.d, x.t); err == nil {
			t.Errorf("ValidateCustom(%v,%d) 应拒绝", x.d, x.t)
		}
	}
}

func TestSubscriptionPricing_Quote(t *testing.T) {
	c := DefaultSubscriptionPricingConfig()
	q, err := c.Quote(30, 30)
	if err != nil {
		t.Fatalf("Quote 应通过: %v", err)
	}
	if q.DailyAmountUSD != 30 || q.ValidityDays != 30 {
		t.Errorf("Quote D/T 不符: %+v", q)
	}
	if !approx(q.UnitPrice, c.UnitPrice(30), 1e-12) || !approx(q.Price, c.Price(30, 30), 1e-12) {
		t.Errorf("Quote u/price 不符: %+v", q)
	}
	if q.FormulaVersion != SubscriptionFormulaVersion {
		t.Errorf("Quote formula_version=%d want %d", q.FormulaVersion, SubscriptionFormulaVersion)
	}
	if _, err := c.Quote(99, 30); err == nil {
		t.Errorf("Quote 越界 D 应拒绝")
	}
}

// D-only 折扣副作用（规格第 3 节警示）：同样总额度 G，「大 D 短期」比「小 D 长期」更便宜；
// 「30 天起买」约束下，最短就是 30 天。这里只断言折扣方向正确（大 D 单价更低）。
func TestSubscriptionPricing_VolumeDiscountDirection(t *testing.T) {
	c := DefaultSubscriptionPricingConfig()
	// 总额度同为 15300：D=510×T=30 vs D=30×T=510（T 超范围仅作单价对比，不下单）。
	if c.UnitPrice(510) >= c.UnitPrice(30) {
		t.Errorf("大 D 单价应更低: u(510)=%v u(30)=%v", c.UnitPrice(510), c.UnitPrice(30))
	}
	// 同 G=15300 下大 D 短期更便宜：P(510,30)=15300 < D=255,T=60 的 P。
	if c.Price(510, 30) >= c.Price(255, 60) {
		t.Errorf("大 D 短期应更便宜: P(510,30)=%v P(255,60)=%v", c.Price(510, 30), c.Price(255, 60))
	}
}
