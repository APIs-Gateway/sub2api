//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// W6 PR4b-2a：PriceDiff 只比较方向，直接返回 PriceDelta。

func pdQuote(input, output float64) *Quote {
	set := &QuotePriceSet{PerToken: QuoteUnitPrices{Input: input, Output: output}}
	return &Quote{Priced: true, BillingMode: "token", Prices: set, FinalPrices: set, EffectiveMultiplier: 1, ImageMultiplier: 1}
}

func TestPriceDiff_Direction(t *testing.T) {
	base := pdQuote(1e-6, 2e-6)
	cases := []struct {
		name          string
		before, after *Quote
		want          PriceDelta
	}{
		{"nil before", nil, base, PriceDeltaUnknown},
		{"nil after", base, nil, PriceDeltaUnknown},
		{"same", pdQuote(1e-6, 2e-6), pdQuote(1e-6, 2e-6), PriceDeltaNone},
		{"rounding noise is equal", pdQuote(1e-6, 2e-6), pdQuote(1e-6*(1+1e-12), 2e-6), PriceDeltaNone},
		{"input up", base, pdQuote(1.5e-6, 2e-6), PriceDeltaUp},
		{"both down", base, pdQuote(0.5e-6, 1e-6), PriceDeltaDown},
		{"mixed", base, pdQuote(2e-6, 1e-6), PriceDeltaUnknown},
		{"to zero is down", base, pdQuote(0, 0), PriceDeltaDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, PriceDiff(tc.before, tc.after)) })
	}
}

func TestPriceDiff_PricedAndModeChanges(t *testing.T) {
	priced := pdQuote(1e-6, 2e-6)
	unpriced := &Quote{Priced: false}
	require.Equal(t, PriceDeltaNone, PriceDiff(unpriced, &Quote{Priced: false}))
	require.Equal(t, PriceDeltaUnknown, PriceDiff(unpriced, priced), "从无价到有价不下结论")
	require.Equal(t, PriceDeltaUnknown, PriceDiff(priced, unpriced))

	perRequest := pdQuote(1e-6, 2e-6)
	perRequest.BillingMode = "per_request"
	require.Equal(t, PriceDeltaUnknown, PriceDiff(priced, perRequest), "计费模式变了")
}

func TestPriceDiff_MultipliersAndOtherPricePoints(t *testing.T) {
	// 没有 FinalPrices 时用 Prices 乘有效倍率。
	set := &QuotePriceSet{PerToken: QuoteUnitPrices{Input: 1e-6}}
	a := &Quote{Priced: true, BillingMode: "token", Prices: set, EffectiveMultiplier: 1}
	b := &Quote{Priced: true, BillingMode: "token", Prices: set, EffectiveMultiplier: 1.5}
	require.Equal(t, PriceDeltaUp, PriceDiff(a, b))
	require.Equal(t, PriceDeltaDown, PriceDiff(b, a))
	// 倍率为 0 的手工 Quote 按 1 处理。
	zero := &Quote{Priced: true, BillingMode: "token", Prices: set}
	require.Equal(t, PriceDeltaNone, PriceDiff(a, zero))

	// 区间单价、按次、图片、长上下文、service tier 都参与比较。
	withIntervals := func(p float64) *Quote {
		q := pdQuote(1e-6, 2e-6)
		q.Intervals = []QuoteTokenInterval{{MinTokens: 0, Prices: QuotePriceSet{PerToken: QuoteUnitPrices{Input: p}}}}
		return q
	}
	require.Equal(t, PriceDeltaUp, PriceDiff(withIntervals(1e-6), withIntervals(2e-6)))
	require.Equal(t, PriceDeltaUnknown, PriceDiff(pdQuote(1e-6, 2e-6), withIntervals(1e-6)), "价格点集合不同")

	perRequest := func(p float64) *Quote {
		q := &Quote{Priced: true, BillingMode: "per_request", EffectiveMultiplier: 2, ImageMultiplier: 3}
		q.PerRequest = &QuotePerRequest{DefaultPrice: p, Tiers: []QuoteRequestTier{{Price: p * 2}}}
		q.ImageRequest = &QuoteImageRequest{Tiers: []QuoteImageTier{{Tier: "1K", Price: p}}}
		return q
	}
	require.Equal(t, PriceDeltaDown, PriceDiff(perRequest(0.05), perRequest(0.04)))
	require.Equal(t, PriceDeltaNone, PriceDiff(perRequest(0.05), perRequest(0.05)))

	withLongContext := func(m float64) *Quote {
		q := pdQuote(1e-6, 2e-6)
		q.LongContext = &QuoteLongContext{InputMultiplier: m, OutputMultiplier: 1,
			ExplicitPrices: &QuotePriceSet{PerToken: QuoteUnitPrices{Input: 3e-6 * m}}}
		q.ServiceTier = &QuoteServiceTier{Multiplier: m}
		return q
	}
	require.Equal(t, PriceDeltaUp, PriceDiff(withLongContext(2), withLongContext(3)))
	require.Equal(t, PriceDeltaNone, PriceDiff(withLongContext(2), withLongContext(2)))
}
