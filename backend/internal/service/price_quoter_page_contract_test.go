//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 价格页契约测试。
//
// 用户价格页现在读 /api/v1/channels/prices（UserPriceCatalogService，价格由 PriceQuoter.BatchQuote 给出），
// 与计费同源；已知偏差的处理结果见 user_price_catalog_test.go：
//   - 偏差 1（只有兜底价的模型显示无价）：已消除（TestUserPriceCatalog_FallbackOnlyModelNowPriced）；
//   - 偏差 2（DeepSeek 官方价卡与 JSON 不一致）：已消除（TestUserPriceCatalog_DeepSeekShowsOfficialCardOffPeak）；
//   - 偏差 3（页面看不到峰价）：保留。页面展示标准（低谷）价，高峰时段的 2 倍只在计费上体现，
//     原因是页面价不应随一天里的时段跳动，高峰倍率也没有对用户的现成文案；
//   - 偏差 4（service tier）：保留。价格页只展示标准档，priority、flex 是请求级参数；
//   - 偏差 5（图片尺寸分档）：已消除（TestUserPriceCatalog_ImageGenerationModelShowsSizeTiers）；
//   - 偏差 6（用户专属倍率）：已消除，倍率在后端乘好（TestUserPriceCatalog_UserRateAppliedInBackend）。
//
// 下面的用例继续记录旧端点 /api/v1/channels/available（ChannelService.fillGlobalPricingFallback，
// 新手引导的模型清单还在读它）的取价口径：渠道没填价时用 PricingService（LiteLLM）合成展示价，
// 不查 BillingService.fallbackPrices，也不走 DeepSeek 官方价卡与峰谷、不看 service tier 与用户专属倍率。
// 它与 PriceQuoter 的现有偏差在这里逐条列成「已知偏差」：断言的是「偏差目前确实存在」。

// pricePageModel 返回价格页对某个模型最终展示的定价（nil 表示页面显示「无价格」）。
func pricePageModel(catalog map[string]*LiteLLMModelPricing, model string) *ChannelModelPricing {
	svc := &ChannelService{pricingService: &PricingService{pricingData: catalog}}
	models := []SupportedModel{{Name: model, Platform: PlatformOpenAI}}
	svc.fillGlobalPricingFallback(models)
	return models[0].Pricing
}

func quoteForContract(t *testing.T, catalog map[string]*LiteLLMModelPricing, model string, at ...func(*QuoteRequest)) *Quote {
	t.Helper()
	f := newQuoteTestFixture(catalog, nil, []*Group{quoteTestGroup(1)}, nil)
	req := QuoteRequest{Model: model, GroupID: quoteTestGroupID}
	for _, mutate := range at {
		mutate(&req)
	}
	q, err := f.quoter.Quote(context.Background(), req)
	require.NoError(t, err)
	return q
}

// 一致项：纯 LiteLLM 模型，价格页与 Quoter 的 input / output / cache 单价相同。
func TestPricePageContract_AgreesOnPureLiteLLMModel(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"quote-test-model": {
			Mode:                        "chat",
			InputCostPerToken:           2e-6,
			OutputCostPerToken:          8e-6,
			CacheCreationInputTokenCost: 2.5e-6,
			CacheReadInputTokenCost:     0.2e-6,
		},
	}
	page := pricePageModel(catalog, "quote-test-model")
	require.NotNil(t, page)
	q := quoteForContract(t, catalog, "quote-test-model")

	require.Equal(t, QuoteSourceLiteLLM, q.Source)
	require.InDelta(t, *page.InputPrice, q.Prices.PerToken.Input, 1e-15)
	require.InDelta(t, *page.OutputPrice, q.Prices.PerToken.Output, 1e-15)
	require.InDelta(t, *page.CacheWritePrice, q.Prices.PerToken.CacheWrite, 1e-15)
	require.InDelta(t, *page.CacheReadPrice, q.Prices.PerToken.CacheRead, 1e-15)
}

// 一致项：GPT-5.4/5.5 的长上下文阈值与倍率，价格页的展示区间与 Quoter 的 LongContext 口径相同。
func TestPricePageContract_AgreesOnGPT55LongContextTier(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"gpt-5.5": {Mode: "chat", InputCostPerToken: 2.5e-6, OutputCostPerToken: 15e-6},
	}
	page := pricePageModel(catalog, "gpt-5.5")
	require.NotNil(t, page)
	require.Len(t, page.Intervals, 2)
	q := quoteForContract(t, catalog, "gpt-5.5")
	require.NotNil(t, q.LongContext)

	require.Equal(t, q.LongContext.ThresholdTokens, *page.Intervals[0].MaxTokens)
	require.Equal(t, q.LongContext.ThresholdTokens, page.Intervals[1].MinTokens)
	require.InDelta(t, *page.Intervals[1].InputPrice, q.Prices.PerToken.Input*q.LongContext.InputMultiplier, 1e-15)
	require.InDelta(t, *page.Intervals[1].OutputPrice, q.Prices.PerToken.Output*q.LongContext.OutputMultiplier, 1e-15)
}

// 已知偏差 1：GLM / Kimi / MiniMax / Grok / Qwen embedding 只存在于 billing 的 fallbackPrices。
// 价格页看不到（显示「无价格」），计费却照常收费。
func TestPricePageContract_KnownDeviation_FallbackOnlyModelsHaveNoPagePrice(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{}
	for _, model := range []string{"glm-4.6", "qwen3-embedding-8b"} {
		t.Run(model, func(t *testing.T) {
			require.Nil(t, pricePageModel(catalog, model), "价格页没有价格")

			q := quoteForContract(t, catalog, model)
			require.True(t, q.Priced, "计费有价")
			require.Equal(t, QuoteSourceFallback, q.Source)
			require.Greater(t, q.Prices.PerToken.Input, 0.0)
		})
	}
}

// 已知偏差 2：DeepSeek 计费强制使用官方价卡，价格页按目录 JSON 显示。
func TestPricePageContract_KnownDeviation_DeepSeekOfficialCardVsCatalogJSON(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
	}
	page := pricePageModel(catalog, "deepseek-v4-flash")
	require.NotNil(t, page)
	require.InDelta(t, 1e-6, *page.InputPrice, 1e-15, "价格页：目录 JSON 的价")

	q := quoteForContract(t, catalog, "deepseek-v4-flash", func(r *QuoteRequest) { r.At = quoteTestAtLow })
	require.Equal(t, QuoteSourceCardPolicy, q.Source)
	require.InDelta(t, 1.5e-7, q.Prices.PerToken.Input, 1e-15, "计费：官方价卡")
}

// 已知偏差 3：DeepSeek 工作日高峰时段计费按 2× 低谷价，价格页没有峰谷概念。
func TestPricePageContract_KnownDeviation_DeepSeekPeakNotOnPage(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
	}
	pageBefore := pricePageModel(catalog, "deepseek-v4-flash")
	low := quoteForContract(t, catalog, "deepseek-v4-flash", func(r *QuoteRequest) { r.At = quoteTestAtLow })
	peak := quoteForContract(t, catalog, "deepseek-v4-flash", func(r *QuoteRequest) { r.At = quoteTestAtPeak })

	require.False(t, low.Policy.DeepSeekPeak)
	require.True(t, peak.Policy.DeepSeekPeak)
	require.InDelta(t, low.Prices.PerToken.Input*2, peak.Prices.PerToken.Input, 1e-15)
	// 价格页对同一模型只有一个价，不随时间变化。
	pageAfter := pricePageModel(catalog, "deepseek-v4-flash")
	require.Equal(t, *pageBefore.InputPrice, *pageAfter.InputPrice)
}

// 已知偏差 4：价格页没有 service tier 概念。priority（价卡自带独立价或标准价 2×）与 flex（0.5×）
// 只体现在计费与 Quoter 上。
func TestPricePageContract_KnownDeviation_ServiceTierNotOnPage(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"gpt-5.5": {Mode: "chat", InputCostPerToken: 2.5e-6, OutputCostPerToken: 15e-6},
	}
	page := pricePageModel(catalog, "gpt-5.5")
	require.NotNil(t, page)
	require.InDelta(t, 2.5e-6, *page.InputPrice, 1e-15)

	standard := quoteForContract(t, catalog, "gpt-5.5")
	flex := quoteForContract(t, catalog, "gpt-5.5", func(r *QuoteRequest) { r.ServiceTier = "flex" })
	require.InDelta(t, standard.Prices.PerToken.Input*0.5, flex.Prices.PerToken.Input, 1e-15)
}

// 已知偏差 5：图片生成模型。价格页只给一个 PerRequestPrice（目录 output_cost_per_image）；
// 计费对 2K 乘 1.5、4K 乘 2，并允许分组配置的图片价覆盖。
func TestPricePageContract_KnownDeviation_ImageTiersNotOnPage(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"quote-test-image": {Mode: "image_generation", OutputCostPerImage: 0.2},
	}
	page := pricePageModel(catalog, "quote-test-image")
	require.NotNil(t, page)
	require.Equal(t, BillingModeImage, page.BillingMode)
	require.NotNil(t, page.PerRequestPrice)
	require.Empty(t, page.Intervals, "价格页没有尺寸分档")

	q := quoteForContract(t, catalog, "quote-test-image")
	require.NotNil(t, q.ImageRequest)
	require.Len(t, q.ImageRequest.Tiers, 3)
	require.InDelta(t, *page.PerRequestPrice, q.ImageRequest.Tiers[0].Price, 1e-12, "1K 与价格页一致")
	require.InDelta(t, *page.PerRequestPrice*1.5, q.ImageRequest.Tiers[1].Price, 1e-12, "2K 是价格页没有的 1.5×")
	require.InDelta(t, *page.PerRequestPrice*2, q.ImageRequest.Tiers[2].Price, 1e-12, "4K 是价格页没有的 2×")
}

// 已知偏差 6：价格页展示的是分组默认倍率，用户专属倍率由前端另拉 /groups/rates，
// 价格页的价格数据本身不含倍率；Quoter 把两者折成 EffectiveMultiplier。
func TestPricePageContract_KnownDeviation_UserRateNotInPagePricing(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"quote-test-model": {Mode: "chat", InputCostPerToken: 2e-6, OutputCostPerToken: 8e-6},
	}
	userRate := 0.5
	f := newQuoteTestFixture(catalog, nil, []*Group{quoteTestGroup(2)}, &userRate)
	q, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "quote-test-model", GroupID: quoteTestGroupID, UserID: quoteTestUserID})
	require.NoError(t, err)

	page := pricePageModel(catalog, "quote-test-model")
	require.InDelta(t, *page.InputPrice, q.Prices.PerToken.Input, 1e-15, "原价一致")
	require.InDelta(t, *page.InputPrice*userRate, q.FinalPrices.PerToken.Input, 1e-15, "最终价含用户专属倍率，价格页数据里没有")
}
