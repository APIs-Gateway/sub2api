//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 价格页边界（W6 7b-2c）：页面展示的价格与倍率必须与计费同源。

// 目录里的图片生成模型没有目录按张价、也没有渠道价时，计费按代码兜底价 $0.134 × 档位系数 × ImageMultiplier 收费，
// 页面照实展示这个按张价，不再退回 token 价。
func TestUserPriceCatalog_ImageGenerationFallbackPriceShown(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"quote-test-image-fb": {Mode: "image_generation", InputCostPerToken: 1e-6},
	}
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{1},
		ModelMapping: map[string]map[string]string{PlatformOpenAI: {"quote-test-image-fb": "quote-test-image-fb"}},
	}}
	groups := []Group{{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 2, ImageRateIndependent: true, ImageRateMultiplier: 3}}
	fx := newPriceCatalogFixture(&PricingService{pricingData: catalog}, channels, groups, nil, nil)
	entry := fx.build(t, 9).model(PlatformOpenAI, "quote-test-image-fb").entry(1)

	require.NotNil(t, entry)
	require.Equal(t, userPriceKindRequest, entry.Kind)
	require.Equal(t, string(BillingModeImage), entry.BillingMode)
	base, rate := 0.134, 3.0
	require.Equal(t, rate, entry.Rate)
	require.Equal(t, base, *entry.Official.Unit)
	require.Equal(t, base*rate, *entry.Prices.Unit)
	require.Len(t, entry.Tiers, 3)
	require.Equal(t, base*1.5*rate, *entry.Tiers[1].Prices.Unit)

	// 与计费同源：页面首档价就是 CalculateImageCost 对 1K 的收费。
	cost := fx.billing.CalculateImageCost("quote-test-image-fb", "1K", 1, nil, rate)
	require.InDelta(t, cost.ActualCost, *entry.Prices.Unit, 1e-12)
}

// 图片请求命中渠道按次（per_request）条目时，计费用 ImageMultiplier；页面对能生成图片的模型也用它。
// 不能生成图片的模型的按次条目仍用分组倍率。
func TestUserPriceCatalog_PerRequestEntryOfImageModelUsesImageMultiplier(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"quote-test-image-pr": {Mode: "image_generation", OutputCostPerImage: 0.2},
		"quote-test-chat-pr":  {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
	}
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{1},
		ModelPricing: []ChannelModelPricing{{
			Platform: PlatformOpenAI, Models: []string{"quote-test-image-pr", "quote-test-chat-pr"},
			BillingMode: BillingModePerRequest, PerRequestPrice: float64Ptr(0.5),
		}},
	}}
	groups := []Group{{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 1.5, ImageRateIndependent: true, ImageRateMultiplier: 0.5}}
	fx := newPriceCatalogFixture(&PricingService{pricingData: catalog}, channels, groups, nil, nil)
	got := fx.build(t, 9)

	img := got.model(PlatformOpenAI, "quote-test-image-pr").entry(1)
	require.NotNil(t, img)
	require.Equal(t, userPriceKindRequest, img.Kind)
	require.Equal(t, 0.5, img.Rate)
	require.Equal(t, 0.5*0.5, *img.Prices.Unit)

	chat := got.model(PlatformOpenAI, "quote-test-chat-pr").entry(1)
	require.NotNil(t, chat)
	require.Equal(t, 1.5, chat.Rate)
	require.Equal(t, 0.5*1.5, *chat.Prices.Unit)
}

// DeepSeek 默认价卡：页面给标准价，并标出高峰倍数（与 effectiveTokenPricing 同一个条件）。
// 渠道自定义价的 DeepSeek、普通模型不带标注。
func TestUserPriceCatalog_DeepSeekPeakMultiplierMarked(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
		"deepseek-v4-pro":   {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
		"plain-chat":        {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
	}
	channels := []Channel{
		{
			ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{1},
			ModelMapping: map[string]map[string]string{PlatformOpenAI: {
				"deepseek-v4-flash": "deepseek-v4-flash", "plain-chat": "plain-chat",
			}},
		},
		{
			ID: 2, Name: "ch2", Status: StatusActive, GroupIDs: []int64{2},
			ModelPricing: []ChannelModelPricing{{
				Platform: PlatformOpenAI, Models: []string{"deepseek-v4-pro"},
				BillingMode: BillingModeToken, InputPrice: testPtrFloat64(3e-6), OutputPrice: testPtrFloat64(6e-6),
			}},
		},
	}
	groups := []Group{
		{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 1},
		{ID: 2, Name: "g2", Platform: PlatformOpenAI, RateMultiplier: 1},
	}
	fx := newPriceCatalogFixture(&PricingService{pricingData: catalog}, channels, groups, nil, nil)
	got := fx.build(t, 9)

	flash := got.model(PlatformOpenAI, "deepseek-v4-flash").entry(1)
	require.NotNil(t, flash)
	require.Equal(t, 2.0, flash.PeakMultiplier)
	require.Equal(t, 0.0, got.model(PlatformOpenAI, "plain-chat").entry(1).PeakMultiplier)
	pro := got.model(PlatformOpenAI, "deepseek-v4-pro").entry(2)
	require.NotNil(t, pro)
	require.Equal(t, 0.0, pro.PeakMultiplier, "渠道自定义价不叠加峰时倍率")
}

// 同一个 DeepSeek 模型跨分组：默认价卡分组带峰时倍率，渠道自定义价分组不带（计费也不对渠道价叠加）。
func TestUserPriceCatalog_DeepSeekPeakMultiplierIsPerGroup(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
	}
	channels := []Channel{
		{
			ID: 1, Name: "default-card", Status: StatusActive, GroupIDs: []int64{1},
			ModelMapping: map[string]map[string]string{PlatformOpenAI: {"deepseek-v4-flash": "deepseek-v4-flash"}},
		},
		{
			ID: 2, Name: "custom-price", Status: StatusActive, GroupIDs: []int64{2},
			ModelPricing: []ChannelModelPricing{{
				Platform: PlatformOpenAI, Models: []string{"deepseek-v4-flash"},
				BillingMode: BillingModeToken, InputPrice: testPtrFloat64(3e-6), OutputPrice: testPtrFloat64(6e-6),
			}},
		},
	}
	groups := []Group{
		{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 1},
		{ID: 2, Name: "g2", Platform: PlatformOpenAI, RateMultiplier: 1},
	}
	fx := newPriceCatalogFixture(&PricingService{pricingData: catalog}, channels, groups, nil, nil)
	m := fx.build(t, 9).model(PlatformOpenAI, "deepseek-v4-flash")
	require.NotNil(t, m)
	require.NotNil(t, m.entry(1))
	require.NotNil(t, m.entry(2))
	require.Equal(t, 2.0, m.entry(1).PeakMultiplier)
	require.Equal(t, 0.0, m.entry(2).PeakMultiplier)
}
