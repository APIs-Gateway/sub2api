//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeGPT56ChannelTokenPricing_UsesBaseAfter272K(t *testing.T) {
	pricing := &ChannelModelPricing{
		BillingMode:     BillingModeToken,
		InputPrice:      testPtrFloat64(2.5e-6),
		OutputPrice:     testPtrFloat64(15e-6),
		CacheWritePrice: testPtrFloat64(3.125e-6),
		CacheReadPrice:  testPtrFloat64(0.25e-6),
		Intervals: []PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(272000), InputPrice: testPtrFloat64(2.5e-6), OutputPrice: testPtrFloat64(15e-6)},
			{MinTokens: 272000, MaxTokens: nil, InputPrice: testPtrFloat64(5e-6), OutputPrice: testPtrFloat64(22.5e-6), CacheWritePrice: testPtrFloat64(6.25e-6), CacheReadPrice: testPtrFloat64(0.5e-6)},
		},
	}

	got := normalizeGPT56ChannelTokenPricing("openai/gpt5.6-terra", pricing)
	require.NotSame(t, pricing, got)
	require.Len(t, got.Intervals, 2)
	require.NotNil(t, got.Intervals[0].MaxTokens)
	require.Equal(t, 272000, *got.Intervals[0].MaxTokens)
	require.Equal(t, 272000, got.Intervals[1].MinTokens)
	require.Nil(t, got.Intervals[1].MaxTokens)
	require.InDelta(t, 2.5e-6, *got.Intervals[1].InputPrice, 1e-12)
	require.InDelta(t, 15e-6, *got.Intervals[1].OutputPrice, 1e-12)
	require.InDelta(t, 3.125e-6, *got.Intervals[1].CacheWritePrice, 1e-12)
	require.InDelta(t, 0.25e-6, *got.Intervals[1].CacheReadPrice, 1e-12)
	require.InDelta(t, 5e-6, *pricing.Intervals[1].InputPrice, 1e-12)
}

func TestNormalizeGPT56ChannelTokenPricing_ClipsCrossingInterval(t *testing.T) {
	pricing := &ChannelModelPricing{
		BillingMode: BillingModeToken,
		InputPrice:  testPtrFloat64(1e-6),
		Intervals: []PricingInterval{{
			MinTokens:  100000,
			MaxTokens:  testPtrInt(500000),
			InputPrice: testPtrFloat64(3e-6),
		}},
	}

	got := normalizeGPT56ChannelTokenPricing("gpt-5.6-sol", pricing)
	require.Len(t, got.Intervals, 2)
	require.Equal(t, 272000, *got.Intervals[0].MaxTokens)
	require.Equal(t, 272000, got.Intervals[1].MinTokens)
	require.InDelta(t, 1e-6, *got.Intervals[1].InputPrice, 1e-12)
}

func TestNormalizeGPT56ChannelTokenPricing_DoesNotChangeOtherModesOrModels(t *testing.T) {
	intervals := []PricingInterval{{MinTokens: 272000, InputPrice: testPtrFloat64(5e-6)}}
	perRequest := &ChannelModelPricing{BillingMode: BillingModePerRequest, Intervals: intervals}
	otherModel := &ChannelModelPricing{BillingMode: BillingModeToken, Intervals: intervals}

	require.Same(t, perRequest, normalizeGPT56ChannelTokenPricing("gpt-5.6-terra", perRequest))
	require.Same(t, otherModel, normalizeGPT56ChannelTokenPricing("gpt-5.4", otherModel))
}
