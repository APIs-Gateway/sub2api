//go:build unit

package service

import (
	"math"
	"testing"
)

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestDeriveWindowCaps(t *testing.T) {
	cases := []struct {
		name              string
		d                 float64
		days              int
		wantWeek, wantMon float64
	}{
		{"30天整: M=30D", 2, 30, 14, 60},
		{"短期 7 天: M=7D(<30 按实际天封顶)", 3, 7, 21, 21},
		{"长期 90 天: M=30D(封到 30 天)", 1, 90, 7, 30},
		{"小数 D 无浮点尾差", 1.1, 30, 7.7, 33},
		{"恰好 30 天边界", 5, 30, 35, 150},
		{"31 天按 30 封", 5, 31, 35, 150},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, m := DeriveWindowCaps(c.d, c.days)
			if !approxEq(w, c.wantWeek) {
				t.Fatalf("weekly: got %v want %v", w, c.wantWeek)
			}
			if !approxEq(m, c.wantMon) {
				t.Fatalf("monthly: got %v want %v", m, c.wantMon)
			}
		})
	}
}

func TestQuoteSubscription_DerivesCapsAndValidates(t *testing.T) {
	s := &SubscriptionService{}

	// 合法 D/T → 报价含派生周/月封顶 + 公式版本。
	q, err := s.QuoteSubscription(2, 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.DailyAmountUSD != 2 || q.ValidityDays != 30 {
		t.Fatalf("D/T mismatch: %+v", q)
	}
	if !approxEq(q.WeeklyCapUSD, 14) || !approxEq(q.MonthlyCapUSD, 60) {
		t.Fatalf("caps mismatch: weekly=%v monthly=%v", q.WeeklyCapUSD, q.MonthlyCapUSD)
	}
	if q.Price <= 0 || q.UnitPrice <= 0 {
		t.Fatalf("price/unit must be positive: %+v", q)
	}
	if q.FormulaVersion != SubscriptionFormulaVersion {
		t.Fatalf("formula version: got %d want %d", q.FormulaVersion, SubscriptionFormulaVersion)
	}

	// 越界 D/T → 带码 BadRequest，不产报价。
	if _, err := s.QuoteSubscription(0, 30); err == nil {
		t.Fatal("expected error for D below min")
	}
	cfg := DefaultSubscriptionPricingConfig()
	if _, err := s.QuoteSubscription(cfg.DMin, cfg.TMin-1); err == nil {
		t.Fatal("expected error for T below min")
	}
}

func TestPricingBounds_MatchesDefaultConfig(t *testing.T) {
	s := &SubscriptionService{}
	b := s.PricingBounds()
	cfg := DefaultSubscriptionPricingConfig()
	if b.DMin != cfg.DMin || b.DMax != cfg.DMax || b.TMin != cfg.TMin || b.TMax != cfg.TMax ||
		b.UMin != cfg.UMin || b.UMax != cfg.UMax {
		t.Fatalf("bounds mismatch: %+v vs %+v", b, cfg)
	}
}
