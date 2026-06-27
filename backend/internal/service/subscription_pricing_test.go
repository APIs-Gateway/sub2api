package service

import (
	"math"
	"testing"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// 规格第 3 节示例表（T=30）：D=1→u=2.000,P=60；D=10→u≈1.816,P≈545；D=50→u=1.000,P=1500。
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
		{1, 30, 2.000, 60, 1e-9, 1e-9},
		{10, 30, 1.81633, 544.90, 1e-4, 0.5},
		{50, 30, 1.000, 1500, 1e-9, 1e-9},
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
	for d := 1.0; d <= 50.0; d += 0.5 {
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
	if u := c.UnitPrice(80); !approx(u, c.UMin, 1e-12) { // 高于 DMax → 最便宜档
		t.Errorf("UnitPrice(80)=%v want UMin=%v", u, c.UMin)
	}
}

// DMax==DMin 退化（无梯度）：任何 D 都按最贵档 UMax，且不 panic。
func TestSubscriptionPricing_DegenerateRange(t *testing.T) {
	c := SubscriptionPricingConfig{DMin: 10, DMax: 10, UMax: 2.0, UMin: 1.0, TMin: 30, TMax: 90}
	if u := c.UnitPrice(10); !approx(u, 2.0, 1e-12) {
		t.Errorf("退化区间 UnitPrice=%v want 2.0", u)
	}
}

func TestSubscriptionPricing_ValidateCustom(t *testing.T) {
	c := DefaultSubscriptionPricingConfig()
	ok := []struct {
		d float64
		t int
	}{{1, 30}, {50, 90}, {25, 60}}
	for _, x := range ok {
		if err := c.ValidateCustom(x.d, x.t); err != nil {
			t.Errorf("ValidateCustom(%v,%d) 应通过, got %v", x.d, x.t, err)
		}
	}
	bad := []struct {
		d float64
		t int
	}{
		{0.5, 30},         // D 太小
		{51, 30},          // D 太大
		{10, 29},          // T 太短（30 起买）
		{10, 91},          // T 太长
		{10, 45},          // T 非整月（在 [30,90] 内但非 30 的倍数）
		{10, 75},          // 同上：75 也非整月
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
	q, err := c.Quote(10, 30)
	if err != nil {
		t.Fatalf("Quote 应通过: %v", err)
	}
	if q.DailyAmountUSD != 10 || q.ValidityDays != 30 {
		t.Errorf("Quote D/T 不符: %+v", q)
	}
	if !approx(q.UnitPrice, c.UnitPrice(10), 1e-12) || !approx(q.Price, c.Price(10, 30), 1e-12) {
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
	// 总额度同为 1500：D=50×T=30 vs D=10×T=150（T 超范围仅作单价对比，不下单）。
	if c.UnitPrice(50) >= c.UnitPrice(10) {
		t.Errorf("大 D 单价应更低: u(50)=%v u(10)=%v", c.UnitPrice(50), c.UnitPrice(10))
	}
	// 同 G=1500 下大 D 短期更便宜：P(50,30)=1500 < D=25,T=60 的 P。
	if c.Price(50, 30) >= c.Price(25, 60) {
		t.Errorf("大 D 短期应更便宜: P(50,30)=%v P(25,60)=%v", c.Price(50, 30), c.Price(25, 60))
	}
}
