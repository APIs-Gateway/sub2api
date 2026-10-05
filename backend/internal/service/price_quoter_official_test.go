//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPriceQuoter_OfficialReference(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"quote-test-model": {Mode: "chat", InputCostPerToken: 2e-6, OutputCostPerToken: 8e-6},
	}
	// 渠道价与分组倍率都不应该影响官方参考价。
	channelInput := 99e-6
	f := newQuoteTestFixture(catalog, quoteTestChannel(ChannelModelPricing{
		Platform:   PlatformOpenAI,
		Models:     []string{"quote-test-model"},
		InputPrice: &channelInput,
	}), []*Group{quoteTestGroup(3)}, nil)

	t.Run("catalog price ignores channel and multiplier", func(t *testing.T) {
		ref := f.quoter.OfficialReference("  quote-test-model ")
		require.True(t, ref.Priced)
		require.Equal(t, "quote-test-model", ref.Model)
		require.Equal(t, QuoteSourceLiteLLM, ref.Source)
		require.NotNil(t, ref.PerMTok)
		require.InDelta(t, 2.0, ref.PerMTok.Input, 1e-9)
		require.InDelta(t, 8.0, ref.PerMTok.Output, 1e-9)
	})

	t.Run("unknown model is unpriced", func(t *testing.T) {
		ref := f.quoter.OfficialReference("totally-unknown-model-xyz")
		require.False(t, ref.Priced)
		require.Equal(t, QuoteSourceNone, ref.Source)
		require.Nil(t, ref.PerMTok)
	})

	t.Run("blank model and nil quoter", func(t *testing.T) {
		require.False(t, f.quoter.OfficialReference("   ").Priced)
		var nilQuoter *PriceQuoter
		require.False(t, nilQuoter.OfficialReference("quote-test-model").Priced)
		require.False(t, (&PriceQuoter{}).OfficialReference("quote-test-model").Priced)
	})
}
