//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

// 与真实远端目录一致的字段形状（astra 按生产价 10/50 美元每百万 token，cache 项取内置兜底价口径；sol / luna 同生产实测 2/10、0.1/0.5）：只有 *_above_272k_tokens 绝对价，没有 long_context_* 字段。
const remoteShapeAbove272KCatalog = `{
  "gpt-6.1-terra": {
    "input_cost_per_token": 1e-06, "output_cost_per_token": 5e-06,
    "cache_creation_input_token_cost": 1.25e-06, "cache_read_input_token_cost": 5e-08,
    "input_cost_per_token_priority": 2e-06, "output_cost_per_token_priority": 1e-05,
    "cache_creation_input_token_cost_priority": 2.5e-06, "cache_read_input_token_cost_priority": 1e-07,
    "input_cost_per_token_above_272k_tokens": 2e-06, "output_cost_per_token_above_272k_tokens": 7.5e-06,
    "cache_creation_input_token_cost_above_272k_tokens": 2.5e-06, "cache_read_input_token_cost_above_272k_tokens": 1e-07,
    "litellm_provider": "openai", "mode": "chat", "supports_service_tier": true
  },
  "gpt-6-sol": {
    "input_cost_per_token": 2e-06, "output_cost_per_token": 1e-05,
    "cache_creation_input_token_cost": 2.5e-06, "cache_read_input_token_cost": 2e-07,
    "input_cost_per_token_priority": 4e-06, "output_cost_per_token_priority": 2e-05,
    "cache_creation_input_token_cost_priority": 5e-06, "cache_read_input_token_cost_priority": 4e-07,
    "input_cost_per_token_above_272k_tokens": 4e-06, "output_cost_per_token_above_272k_tokens": 1.5e-05,
    "cache_creation_input_token_cost_above_272k_tokens": 5e-06, "cache_read_input_token_cost_above_272k_tokens": 4e-07,
    "litellm_provider": "openai", "mode": "chat", "supports_service_tier": true
  },
  "gpt-6.1-sol": {
    "input_cost_per_token": 2e-06, "output_cost_per_token": 1e-05,
    "cache_creation_input_token_cost": 2.5e-06, "cache_read_input_token_cost": 1e-07,
    "input_cost_per_token_priority": 4e-06, "output_cost_per_token_priority": 2e-05,
    "cache_creation_input_token_cost_priority": 5e-06, "cache_read_input_token_cost_priority": 2e-07,
    "input_cost_per_token_above_272k_tokens": 4e-06, "output_cost_per_token_above_272k_tokens": 1.5e-05,
    "cache_creation_input_token_cost_above_272k_tokens": 5e-06, "cache_read_input_token_cost_above_272k_tokens": 2e-07,
    "litellm_provider": "openai", "mode": "chat", "supports_service_tier": true
  },
  "gpt-6-astra": {
    "input_cost_per_token": 1e-05, "output_cost_per_token": 5e-05,
    "cache_creation_input_token_cost": 1.25e-05, "cache_read_input_token_cost": 1e-06,
    "input_cost_per_token_priority": 2e-05, "output_cost_per_token_priority": 1e-04,
    "cache_creation_input_token_cost_priority": 2.5e-05, "cache_read_input_token_cost_priority": 2e-06,
    "input_cost_per_token_above_272k_tokens": 2e-05, "output_cost_per_token_above_272k_tokens": 7.5e-05,
    "cache_creation_input_token_cost_above_272k_tokens": 2.5e-05, "cache_read_input_token_cost_above_272k_tokens": 2e-06,
    "litellm_provider": "openai", "mode": "chat", "supports_service_tier": true
  },
  "gpt-6-luna": {
    "input_cost_per_token": 1e-07, "output_cost_per_token": 5e-07,
    "cache_creation_input_token_cost": 1.25e-07, "cache_read_input_token_cost": 1e-08,
    "input_cost_per_token_priority": 2e-07, "output_cost_per_token_priority": 1e-06,
    "cache_creation_input_token_cost_priority": 2.5e-07, "cache_read_input_token_cost_priority": 2e-08,
    "input_cost_per_token_above_272k_tokens": 2e-07, "output_cost_per_token_above_272k_tokens": 7.5e-07,
    "cache_creation_input_token_cost_above_272k_tokens": 2.5e-07, "cache_read_input_token_cost_above_272k_tokens": 2e-08,
    "litellm_provider": "openai", "mode": "chat", "supports_service_tier": true
  }
}`

func newRemoteShapeBillingService(t *testing.T) *BillingService {
	t.Helper()
	data, err := (&PricingService{}).parsePricingData([]byte(remoteShapeAbove272KCatalog))
	require.NoError(t, err)
	return NewBillingService(&config.Config{}, &PricingService{pricingData: data})
}

func remoteShapeCost(t *testing.T, svc *BillingService, model, tier string, tokens UsageTokens) float64 {
	t.Helper()
	cost, err := svc.CalculateCostWithServiceTier(model, tokens, 1.0, tier)
	require.NoError(t, err, model)
	return cost.TotalCost
}

func TestGPT61TerraLongContextFromAboveTierOnlyCatalog(t *testing.T) {
	svc := newRemoteShapeBillingService(t)
	long := UsageTokens{InputTokens: 200_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_001, OutputTokens: 100_000}
	for _, model := range []string{"gpt-6.1-terra", "openai/gpt-6.1-terra-max", "gpt-6.1-terra-2026-10-27"} {
		require.InDelta(t, 1.280, remoteShapeCost(t, svc, model, "", long), 1e-6, model+" standard")
		require.InDelta(t, 2.560, remoteShapeCost(t, svc, model, "priority", long), 1e-6, model+" priority")
		require.InDelta(t, 0.640, remoteShapeCost(t, svc, model, "flex", long), 1e-6, model+" flex")
	}
	// 阈值边界：正好 272,000 不加价，272,001 加价（输入 2 倍）。
	require.InDelta(t, 272_000*1e-6, remoteShapeCost(t, svc, "gpt-6.1-terra", "", UsageTokens{InputTokens: 272_000}), 1e-9)
	require.InDelta(t, 272_001*2e-6, remoteShapeCost(t, svc, "gpt-6.1-terra", "", UsageTokens{InputTokens: 272_001}), 1e-9)
	require.InDelta(t, 272_000*2e-6, remoteShapeCost(t, svc, "gpt-6.1-terra", "priority", UsageTokens{InputTokens: 272_000}), 1e-9)
	require.InDelta(t, 272_001*4e-6, remoteShapeCost(t, svc, "gpt-6.1-terra", "priority", UsageTokens{InputTokens: 272_001}), 1e-9)
}

func TestLongContextDerivationEnabledForGPT6Family(t *testing.T) {
	for _, model := range []string{
		"gpt-6.1-terra", "openai/gpt-6.1-terra-max", "gpt-6-sol", "gpt-6.1-sol", "gpt-6-astra", "gpt-6", "gpt-6-luna",
		"openai/gpt-6-luna-max", "GPT-6-Sol", "gpt-6.1-terra-pro",
	} {
		require.True(t, longContextDerivationEnabled(model), model)
	}
	// GPT-5.x（含 gpt-5.6-*、gpt-5.4/5.5）和其它厂商模型不在折算范围内，保持原逻辑。
	for _, model := range []string{"gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-4o", "gpt-60", "gpt-6o", "claude-sonnet-4", ""} {
		require.False(t, longContextDerivationEnabled(model), model)
	}
	data, err := (&PricingService{}).parsePricingData([]byte(remoteShapeAbove272KCatalog))
	require.NoError(t, err)
	wantMult := map[string][2]float64{
		"gpt-6.1-terra": {2, 1.5}, "gpt-6-sol": {2, 1.5}, "gpt-6.1-sol": {2, 1.5}, "gpt-6-astra": {2, 1.5}, "gpt-6-luna": {2, 1.5},
	}
	for model, want := range wantMult {
		require.Equal(t, 272_000, data[model].LongContextInputTokenThreshold, model)
		require.InDelta(t, want[0], data[model].LongContextInputCostMultiplier, 1e-9, model)
		require.InDelta(t, want[1], data[model].LongContextOutputCostMultiplier, 1e-9, model)
		// 折算后清掉绝对价，priority/flex 的长上下文价由倍率叠加。
		require.Zero(t, data[model].InputCostPerTokenAbove272KTokens, model)
		require.Zero(t, data[model].CacheReadInputTokenCostAbove272KTokens, model)
	}
	// 显式 long_context_* 字段优先，不被折算覆盖。
	explicit, err := (&PricingService{}).parsePricingData([]byte(`{"gpt-6.1-terra":{"input_cost_per_token":1e-06,"output_cost_per_token":5e-06,
		"long_context_input_token_threshold":100000,"long_context_input_cost_multiplier":3,"long_context_output_cost_multiplier":1,
		"input_cost_per_token_above_272k_tokens":2e-06}}`))
	require.NoError(t, err)
	require.Equal(t, 100_000, explicit["gpt-6.1-terra"].LongContextInputTokenThreshold)
	require.InDelta(t, 3.0, explicit["gpt-6.1-terra"].LongContextInputCostMultiplier, 1e-9)
}

// cxw 2026-11-28 拍板：gpt-6 系列超过 27.2 万 token 按价格表的 *_above_272k_tokens 价计费。
// 价格表只给 input/output/cache 读写四项的绝对价；priority 档没有 above 字段，按 priority 价 × 同一倍率。
func TestGPT6FamilyLongContextFollowsCatalogAboveTier(t *testing.T) {
	var raw map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(remoteShapeAbove272KCatalog), &raw))
	num := func(model, key string) float64 {
		v, ok := raw[model][key].(float64)
		require.True(t, ok, model+"."+key)
		return v
	}
	svc := newRemoteShapeBillingService(t)
	long := UsageTokens{InputTokens: 200_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_001, OutputTokens: 100_000}
	short := UsageTokens{InputTokens: 100_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_000, OutputTokens: 100_000}
	for model := range raw {
		in, out := num(model, "input_cost_per_token"), num(model, "output_cost_per_token")
		write, read := num(model, "cache_creation_input_token_cost"), num(model, "cache_read_input_token_cost")
		pin, pout := num(model, "input_cost_per_token_priority"), num(model, "output_cost_per_token_priority")
		pwrite, pread := num(model, "cache_creation_input_token_cost_priority"), num(model, "cache_read_input_token_cost_priority")
		ain, aout := num(model, "input_cost_per_token_above_272k_tokens"), num(model, "output_cost_per_token_above_272k_tokens")
		awrite, aread := num(model, "cache_creation_input_token_cost_above_272k_tokens"), num(model, "cache_read_input_token_cost_above_272k_tokens")

		// 272K 以内：基础价，与加价前一致。
		wantShort := 100_000*in + 50_000*write + 50_000*read + 100_000*out
		require.InDelta(t, wantShort, remoteShapeCost(t, svc, model, "", short), 1e-9, model+" short")
		// 272K 以上：整笔请求按价格表 above 价（input / output / cache 写 / cache 读）。
		wantLong := 200_000*ain + 50_000*awrite + 50_001*aread + 100_000*aout
		require.InDelta(t, wantLong, remoteShapeCost(t, svc, model, "", long), 1e-9, model+" long")
		// priority：价格表没有 priority 的 above 字段，按 priority 价 × 输入侧 2 倍 / 输出 1.5 倍。
		wantPriorityLong := 2*(200_000*pin+50_000*pwrite+50_001*pread) + 1.5*100_000*pout
		require.InDelta(t, wantPriorityLong, remoteShapeCost(t, svc, model, "priority", long), 1e-9, model+" priority long")
		wantPriorityShort := 100_000*pin + 50_000*pwrite + 50_000*pread + 100_000*pout
		require.InDelta(t, wantPriorityShort, remoteShapeCost(t, svc, model, "priority", short), 1e-9, model+" priority short")
		// flex：标准价的一半，长上下文同样先加价再减半。
		require.InDelta(t, wantLong/2, remoteShapeCost(t, svc, model, "flex", long), 1e-9, model+" flex long")
		// 价格表的 above 价确实等于折算用的倍率（折算不会改变表里的数）。
		require.InDelta(t, ain, in*2, 1e-15, model)
		require.InDelta(t, aout, out*1.5, 1e-15, model)
		require.InDelta(t, awrite, write*2, 1e-15, model)
		require.InDelta(t, aread, read*2, 1e-15, model)
	}
}

func TestGPT6LunaLongContextBoundariesAndSpellings(t *testing.T) {
	svc := newRemoteShapeBillingService(t)
	// 阈值按 input + cache read + cache write 合计，正好 272,000 不加价，272,001 加价。
	require.InDelta(t, 272_000*1e-7, remoteShapeCost(t, svc, "gpt-6-luna", "", UsageTokens{InputTokens: 272_000}), 1e-12)
	require.InDelta(t, 272_001*2e-7, remoteShapeCost(t, svc, "gpt-6-luna", "", UsageTokens{InputTokens: 272_001}), 1e-12)
	require.InDelta(t, 272_000*1e-8, remoteShapeCost(t, svc, "gpt-6-luna", "", UsageTokens{CacheReadTokens: 272_000}), 1e-12)
	require.InDelta(t, 272_001*2e-8, remoteShapeCost(t, svc, "gpt-6-luna", "", UsageTokens{CacheReadTokens: 272_001}), 1e-12)
	require.InDelta(t, 272_000*1.25e-7, remoteShapeCost(t, svc, "gpt-6-luna", "", UsageTokens{CacheCreationTokens: 272_000}), 1e-12)
	require.InDelta(t, 272_001*2.5e-7, remoteShapeCost(t, svc, "gpt-6-luna", "", UsageTokens{CacheCreationTokens: 272_001}), 1e-12)
	// 输出 token 不参与阈值判断。
	require.InDelta(t, 1_000*1e-7+500_000*5e-7, remoteShapeCost(t, svc, "gpt-6-luna", "", UsageTokens{InputTokens: 1_000, OutputTokens: 500_000}), 1e-12)
	// 带后缀 / 日期快照 / provider 前缀的写法走同一条目。
	for _, model := range []string{"gpt-6-luna-max", "openai/gpt-6-luna", "gpt-6-luna-2026-09-22"} {
		require.InDelta(t, 300_000*2e-7, remoteShapeCost(t, svc, model, "", UsageTokens{InputTokens: 300_000}), 1e-12, model)
	}
}

// 回归锁：GPT-5.x 现有长上下文结果、没有 above 字段的模型都不受 GPT-6 折算影响。
func TestGPT5AndNoAboveFieldModelsUnchangedByGPT6LongContextDerivation(t *testing.T) {
	const catalog = `{
  "gpt-5.6-luna": {
    "input_cost_per_token": 2e-07, "output_cost_per_token": 1.2e-06,
    "cache_creation_input_token_cost": 2.5e-07, "cache_read_input_token_cost": 2e-08,
    "input_cost_per_token_priority": 4e-07, "output_cost_per_token_priority": 2.4e-06,
    "cache_creation_input_token_cost_priority": 5e-07, "cache_read_input_token_cost_priority": 4e-08,
    "input_cost_per_token_above_272k_tokens": 4e-07, "output_cost_per_token_above_272k_tokens": 1.8e-06,
    "cache_creation_input_token_cost_above_272k_tokens": 5e-07, "cache_read_input_token_cost_above_272k_tokens": 4e-08,
    "long_context_input_token_threshold": 272000, "long_context_input_cost_multiplier": 2.0, "long_context_output_cost_multiplier": 1.5,
    "litellm_provider": "openai", "mode": "chat", "supports_service_tier": true
  },
  "gpt-5.5": {
    "input_cost_per_token": 5e-06, "output_cost_per_token": 3e-05, "cache_read_input_token_cost": 5e-07,
    "input_cost_per_token_above_272k_tokens": 1e-05, "output_cost_per_token_above_272k_tokens": 4.5e-05,
    "cache_read_input_token_cost_above_272k_tokens": 1e-06,
    "litellm_provider": "openai", "mode": "chat"
  },
  "gpt-6-nolong": {
    "input_cost_per_token": 1e-06, "output_cost_per_token": 5e-06, "cache_read_input_token_cost": 1e-07,
    "litellm_provider": "openai", "mode": "chat"
  }
}`
	data, err := (&PricingService{}).parsePricingData([]byte(catalog))
	require.NoError(t, err)
	// 解析层：gpt-5.x 的字段原样保留（显式 long_context_* 不动；只有 above 绝对价的 gpt-5.5 不折算）。
	luna := data["gpt-5.6-luna"]
	require.Equal(t, 272_000, luna.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, luna.LongContextInputCostMultiplier, 1e-9)
	require.InDelta(t, 1.5, luna.LongContextOutputCostMultiplier, 1e-9)
	require.InDelta(t, 4e-7, luna.InputCostPerTokenAbove272KTokens, 1e-15)
	require.InDelta(t, 4e-8, luna.CacheReadInputTokenCostAbove272KTokens, 1e-15)
	g55 := data["gpt-5.5"]
	require.Zero(t, g55.LongContextInputTokenThreshold)
	require.InDelta(t, 1e-5, g55.InputCostPerTokenAbove272KTokens, 1e-15)
	require.InDelta(t, 4.5e-5, g55.OutputCostPerTokenAbove272KTokens, 1e-15)
	// 没有 above 字段的 GPT-6 条目：不凭空生成阈值。
	require.Zero(t, data["gpt-6-nolong"].LongContextInputTokenThreshold)
	require.Zero(t, data["gpt-6-nolong"].LongContextInputCostMultiplier)

	svc := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	long := UsageTokens{InputTokens: 200_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_001, OutputTokens: 100_000}
	short := UsageTokens{InputTokens: 100_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_000, OutputTokens: 100_000}
	// gpt-5.6-luna：>272K 标准档按 above 价，priority 不叠加长上下文（既有行为）。
	require.InDelta(t, 0.28700004, remoteShapeCost(t, svc, "gpt-5.6-luna", "", long), 1e-9)
	require.InDelta(t, 0.34700004, remoteShapeCost(t, svc, "gpt-5.6-luna", "priority", long), 1e-9)
	require.InDelta(t, 100_000*2e-7+50_000*2.5e-7+50_000*2e-8+100_000*1.2e-6, remoteShapeCost(t, svc, "gpt-5.6-luna", "", short), 1e-9)
	// gpt-5.5：只有 above 绝对价，既有逻辑下 >272K 用绝对价。
	require.InDelta(t, 200_000*1e-5+100_000*1e-6+100_000*4.5e-5, remoteShapeCost(t, svc, "gpt-5.5", "", UsageTokens{InputTokens: 200_000, CacheReadTokens: 100_000, OutputTokens: 100_000}), 1e-9)
	// 没有 above 字段的 GPT-6 条目：超过 272K 不加价。
	require.InDelta(t, 300_000*1e-6+10_000*5e-6, remoteShapeCost(t, svc, "gpt-6-nolong", "", UsageTokens{InputTokens: 300_000, OutputTokens: 10_000}), 1e-9)
}

// 价格页 / 可用渠道页：GPT-6 家族折算后仍要显示 >272K 档，且带 cache 价。
func TestGPT6FamilyLongContextShownOnPriceSurfaces(t *testing.T) {
	data, err := (&PricingService{}).parsePricingData([]byte(remoteShapeAbove272KCatalog))
	require.NoError(t, err)
	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	for model, want := range map[string][4]float64{
		// input, output, cache write, cache read（价格表 above 价）
		"gpt-6-luna":    {2e-7, 7.5e-7, 2.5e-7, 2e-8},
		"gpt-6-sol":     {4e-6, 1.5e-5, 5e-6, 4e-7},
		"gpt-6.1-sol":   {4e-6, 1.5e-5, 5e-6, 2e-7},
		"gpt-6-astra":   {2e-5, 7.5e-5, 2.5e-5, 2e-6},
		"gpt-6.1-terra": {2e-6, 7.5e-6, 2.5e-6, 1e-7},
	} {
		intervals := longContextDisplayIntervals(data[model], model)
		require.Len(t, intervals, 2, model)
		high := intervals[1]
		require.Equal(t, 272_000, high.MinTokens, model)
		require.InDelta(t, want[0], *high.InputPrice, 1e-15, model)
		require.InDelta(t, want[1], *high.OutputPrice, 1e-15, model)
		require.NotNil(t, high.CacheWritePrice, model)
		require.NotNil(t, high.CacheReadPrice, model)
		require.InDelta(t, want[2], *high.CacheWritePrice, 1e-15, model)
		require.InDelta(t, want[3], *high.CacheReadPrice, 1e-15, model)

		// 报价结构（用户价格页数据源）：阈值 + 倍率，user_price_catalog 据此展示第二档。
		pricing, err := billing.GetModelPricing(model)
		require.NoError(t, err, model)
		lc := quoteLongContext(pricing)
		require.Equal(t, 272_000, lc.ThresholdTokens, model)
		require.InDelta(t, 2.0, lc.InputMultiplier, 1e-9, model)
		require.InDelta(t, 1.5, lc.OutputMultiplier, 1e-9, model)
		require.False(t, lc.PriorityExcludesLongContext, model)
		got := longContextUnitPrices(QuoteUnitPrices{
			Input: pricing.InputPricePerToken, Output: pricing.OutputPricePerToken,
			CacheWrite: pricing.CacheCreationPricePerToken, CacheRead: pricing.CacheReadPricePerToken,
		}, lc)
		require.InDelta(t, want[0], got.Input, 1e-15, model)
		require.InDelta(t, want[1], got.Output, 1e-15, model)
		require.InDelta(t, want[2], got.CacheWrite, 1e-15, model)
		require.InDelta(t, want[3], got.CacheRead, 1e-15, model)
	}
}

// 凡是网关转发成 gpt-6.1-terra 的写法，都必须按 Terra 计价（不回退 gpt-5.4）。
func TestGPT61TerraForwardedSpellingsAreBilledAsTerra(t *testing.T) {
	tokens := UsageTokens{InputTokens: 100_000, OutputTokens: 100_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_000}
	custom := NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-6.1-terra": {InputCostPerToken: 3e-6, OutputCostPerToken: 1e-6},
	}})
	for name, billing := range map[string]*BillingService{"fallback": newTestBillingService(), "catalog": newRemoteShapeBillingService(t)} {
		svc := &OpenAIGatewayService{billingService: billing}
		apiKey := &APIKey{Group: &Group{ID: 1, Platform: PlatformOpenAI}}
		for _, model := range []string{"gpt-6.1-terra", "gpt-6.1-terra-2026-10-27", "gpt-6.1-terra-minimal", "openai/GPT-6.1-Terra-2026-10-27"} {
			// /v1/responses、/v1/chat/completions、/v1/messages 共用 usageBillingModelCandidates + 该计费入口。
			for _, candidates := range [][]string{
				usageBillingModelCandidates(model),
				usageBillingModelCandidates(model, "public-alias", model),
				usageBillingModelCandidates("public-alias", model),
			} {
				if candidates[0] == "public-alias" {
					continue // 别名本身无价，由下一候选计价；此处只验证候选集合包含 Terra 规范名
				}
				cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), &OpenAIForwardResult{Model: model}, apiKey, candidates, 1.0, 1.0, tokens, "", time.Time{})
				require.NoError(t, err, name+"/"+model)
				require.InDelta(t, 0.665, cost.TotalCost, 1e-9, name+"/"+model)
			}
			require.Contains(t, usageBillingModelCandidates("public-alias", model), "gpt-6.1-terra", model)
		}
		// -pro / -ultra 不是 Terra：不得按 Terra 价计费。
		for _, model := range []string{"gpt-6.1-terra-pro", "gpt-6.1-terra-ultra"} {
			if cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), &OpenAIForwardResult{Model: model}, apiKey, usageBillingModelCandidates(model), 1.0, 1.0, tokens, "", time.Time{}); err == nil && cost != nil {
				require.NotEqual(t, 0.665, cost.TotalCost, name+"/"+model)
			}
		}
	}
	// 分组/渠道为 gpt-6.1-terra 配的自定义价，按同一规范名命中（legacyPolicy.PriceOverride 用 normalizeKnownOpenAICodexModel 兜底查找）。
	for _, model := range []string{"gpt-6.1-terra-2026-10-27", "gpt-6.1-terra-minimal"} {
		require.Equal(t, "gpt-6.1-terra", normalizeKnownOpenAICodexModel(model))
		pricing, err := custom.GetModelPricing(model)
		require.NoError(t, err, model)
		require.InDelta(t, 3e-6, pricing.InputPricePerToken, 1e-15, model)
	}
}

func TestGPT61SolAndTerraAnthropicMaxReachesUpstream(t *testing.T) {
	for _, model := range []string{"gpt-6.1-terra", "gpt-6.1-terra-max", "gpt-6.1-sol", "gpt-6.1-sol-max", "openai/gpt-6.1-terra-max"} {
		out, err := apicompat.AnthropicToResponses(&apicompat.AnthropicRequest{Model: model, OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "max"}})
		require.NoError(t, err, model)
		require.Equal(t, "max", out.Reasoning.Effort, model)
	}
	// 经兼容层归一后（后缀 max → output_config.effort）同样是 max。
	for _, model := range []string{"gpt-6.1-terra-max", "gpt-6.1-sol-max"} {
		req := &apicompat.AnthropicRequest{Model: model}
		applyOpenAICompatModelNormalization(req)
		out, err := apicompat.AnthropicToResponses(req)
		require.NoError(t, err, model)
		require.Equal(t, "max", openAICompatAnthropicReasoningEffort(req, req.Model, out.Reasoning.Effort), model)
	}
	// thinking 明确关闭时以 none 为准（Terra 允许；Sol 拒绝）。
	out, err := apicompat.AnthropicToResponses(&apicompat.AnthropicRequest{Model: "gpt-6.1-terra", Thinking: &apicompat.AnthropicThinking{Type: "disabled"}, OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "max"}})
	require.NoError(t, err)
	require.Equal(t, "none", out.Reasoning.Effort)
	_, err = apicompat.AnthropicToResponses(&apicompat.AnthropicRequest{Model: "gpt-6.1-sol", Thinking: &apicompat.AnthropicThinking{Type: "disabled"}, OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "max"}})
	require.Error(t, err)
	// 其它模型仍把 max 降成 xhigh。
	other, err := apicompat.AnthropicToResponses(&apicompat.AnthropicRequest{Model: "gpt-5.2", OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "max"}})
	require.NoError(t, err)
	require.Equal(t, "xhigh", other.Reasoning.Effort)
}

func TestGPT61TerraLongContextDisplayIncludesCache(t *testing.T) {
	for _, name := range []string{"gpt-6.1-terra", "openai/gpt-6.1-terra-max"} {
		tier, ok := openAILongContextTierForModel(name)
		require.True(t, ok, name)
		require.Equal(t, 272_000, tier.threshold)
		intervals := longContextDisplayIntervals(&LiteLLMModelPricing{
			InputCostPerToken: 1e-6, OutputCostPerToken: 5e-6, CacheCreationInputTokenCost: 1.25e-6, CacheReadInputTokenCost: 5e-8,
			LongContextInputTokenThreshold: 272_000, LongContextInputCostMultiplier: 2, LongContextOutputCostMultiplier: 1.5,
		}, name)
		require.Len(t, intervals, 2, name)
		high := intervals[1]
		require.InDelta(t, 2e-6, *high.InputPrice, 1e-15)
		require.InDelta(t, 7.5e-6, *high.OutputPrice, 1e-15)
		require.NotNil(t, high.CacheWritePrice, name)
		require.NotNil(t, high.CacheReadPrice, name)
		require.InDelta(t, 2.5e-6, *high.CacheWritePrice, 1e-15)
		require.InDelta(t, 0.1e-6, *high.CacheReadPrice, 1e-15)
	}
	_, ok := openAILongContextTierForModel("gpt-6.1-sol")
	require.False(t, ok, "Sol display behaviour is unchanged")
}

// 分组/渠道配了自定义 token 价的 GPT-6：超过 27.2 万 token 在自定义价上乘阈值倍率（输入侧 2 倍、输出 1.5 倍），
// 扣费、用户价格页、可用渠道页三处必须一致。
func TestGPT6CustomGroupPriceLongContextBillingAndPages(t *testing.T) {
	data, err := (&PricingService{}).parsePricingData([]byte(remoteShapeAbove272KCatalog))
	require.NoError(t, err)
	custom := ChannelModelPricing{
		Platform: PlatformOpenAI, Models: []string{"gpt-6-luna"}, BillingMode: BillingModeToken,
		InputPrice: float64Ptr(3e-7), OutputPrice: float64Ptr(1e-6),
		CacheWritePrice: float64Ptr(4e-7), CacheReadPrice: float64Ptr(3e-8),
	}
	channels := quoteTestChannel(custom)
	f := newQuoteTestFixture(data, channels, []*Group{quoteTestGroup(1)}, nil)

	long := UsageTokens{InputTokens: 200_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_001, OutputTokens: 100_000}
	short := UsageTokens{InputTokens: 100_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_000, OutputTokens: 100_000}
	wantLong := 2*(200_000*3e-7+50_000*4e-7+50_001*3e-8) + 1.5*100_000*1e-6
	wantShort := 100_000*3e-7 + 50_000*4e-7 + 50_000*3e-8 + 100_000*1e-6

	// 扣费
	require.InDelta(t, wantLong, f.referenceTokenCost(t, "gpt-6-luna", 1, long, "", time.Time{}).TotalCost, 1e-9)
	require.InDelta(t, wantShort, f.referenceTokenCost(t, "gpt-6-luna", 1, short, "", time.Time{}).TotalCost, 1e-9)
	wantUnit := [4]float64{6e-7, 1.5e-6, 8e-7, 6e-8} // 输入 / 输出 / cache 写 / cache 读

	// 用户价格页：第二档 = 自定义价 × 倍率，且 Quote.Cost 与扣费一致。
	qt, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-6-luna", GroupID: quoteTestGroupID})
	require.NoError(t, err)
	entry := userPriceEntryFromQuote(qt)
	require.NotNil(t, entry)
	require.Len(t, entry.Tiers, 2)
	require.Equal(t, 272_000, entry.Tiers[1].MinTokens)
	require.InDelta(t, 3e-7, *entry.Tiers[0].Prices.Input, 1e-15)
	require.InDelta(t, wantUnit[0], *entry.Tiers[1].Prices.Input, 1e-15)
	require.InDelta(t, wantUnit[1], *entry.Tiers[1].Prices.Output, 1e-15)
	require.InDelta(t, wantUnit[2], *entry.Tiers[1].Prices.CacheWrite, 1e-15)
	require.InDelta(t, wantUnit[3], *entry.Tiers[1].Prices.CacheRead, 1e-15)
	quoted, err := qt.Cost(context.Background(), QuoteUsage{Tokens: long})
	require.NoError(t, err)
	require.InDelta(t, wantLong, quoted.TotalCost, 1e-9)

	// 可用渠道页：同样给出第二档，且不污染渠道缓存里共享的定价对象。
	cs := &ChannelService{pricingService: &PricingService{pricingData: data}}
	models := channels[0].SupportedModels()
	cs.fillGlobalPricingFallback(models)
	var pricing *ChannelModelPricing
	for _, m := range models {
		if m.Name == "gpt-6-luna" {
			pricing = m.Pricing
		}
	}
	require.NotNil(t, pricing)
	require.Len(t, pricing.Intervals, 2)
	require.Equal(t, 272_000, pricing.Intervals[1].MinTokens)
	require.Nil(t, pricing.Intervals[1].MaxTokens)
	require.InDelta(t, 3e-7, *pricing.Intervals[0].InputPrice, 1e-15)
	require.InDelta(t, wantUnit[0], *pricing.Intervals[1].InputPrice, 1e-15)
	require.InDelta(t, wantUnit[1], *pricing.Intervals[1].OutputPrice, 1e-15)
	require.InDelta(t, wantUnit[2], *pricing.Intervals[1].CacheWritePrice, 1e-15)
	require.InDelta(t, wantUnit[3], *pricing.Intervals[1].CacheReadPrice, 1e-15)
	require.Empty(t, channels[0].ModelPricing[0].Intervals, "shared channel pricing must not be mutated")
	require.Empty(t, custom.Intervals)
}

// 自定义价配了区间 / 只配了部分项 / 模型没有长上下文规则时，可用渠道页的展示规则。
func TestWithLongContextTierForCustomPricingEdges(t *testing.T) {
	data, err := (&PricingService{}).parsePricingData([]byte(remoteShapeAbove272KCatalog))
	require.NoError(t, err)
	lp := data["gpt-6-luna"]

	// 已有有效区间：原样返回。
	withIntervals := &ChannelModelPricing{BillingMode: BillingModeToken, InputPrice: float64Ptr(1e-7),
		Intervals: []PricingInterval{{MinTokens: 0, InputPrice: float64Ptr(1e-7)}}}
	require.Same(t, withIntervals, withLongContextTierForCustomPricing(withIntervals, lp, "gpt-6-luna"))
	// 按次模式：原样返回。
	perReq := &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: float64Ptr(0.1)}
	require.Same(t, perReq, withLongContextTierForCustomPricing(perReq, lp, "gpt-6-luna"))
	// 只配输入价：只展示输入价那一项的第二档。
	partial := &ChannelModelPricing{BillingMode: BillingModeToken, InputPrice: float64Ptr(3e-7)}
	out := withLongContextTierForCustomPricing(partial, lp, "gpt-6-luna")
	require.NotSame(t, partial, out)
	require.Len(t, out.Intervals, 2)
	require.InDelta(t, 6e-7, *out.Intervals[1].InputPrice, 1e-15)
	require.Nil(t, out.Intervals[1].OutputPrice)
	require.Nil(t, out.Intervals[1].CacheReadPrice)
	require.Empty(t, partial.Intervals)
	// 没有长上下文规则的模型：原样返回。
	plain := &ChannelModelPricing{BillingMode: BillingModeToken, InputPrice: float64Ptr(3e-7)}
	require.Same(t, plain, withLongContextTierForCustomPricing(plain, &LiteLLMModelPricing{InputCostPerToken: 1e-6}, "gpt-4o"))
	require.Same(t, plain, withLongContextTierForCustomPricing(plain, nil, "gpt-6-luna"))
}

// GPT-5.6 有目录显式 above 绝对价：自定义价配置下，扣费、用户价格页、可用渠道页三处的第二档必须一致。
func TestGPT56CustomGroupPriceLongContextPagesMatchBilling(t *testing.T) {
	const catalog = `{
  "gpt-5.6-luna": {
    "input_cost_per_token": 2e-07, "output_cost_per_token": 1.2e-06,
    "cache_creation_input_token_cost": 2.5e-07, "cache_read_input_token_cost": 2e-08,
    "input_cost_per_token_priority": 4e-07, "output_cost_per_token_priority": 2.4e-06,
    "input_cost_per_token_above_272k_tokens": 4e-07, "output_cost_per_token_above_272k_tokens": 1.8e-06,
    "cache_creation_input_token_cost_above_272k_tokens": 5e-07, "cache_read_input_token_cost_above_272k_tokens": 4e-08,
    "long_context_input_token_threshold": 272000, "long_context_input_cost_multiplier": 2.0, "long_context_output_cost_multiplier": 1.5,
    "litellm_provider": "openai", "mode": "chat", "supports_service_tier": true
  }
}`
	data, err := (&PricingService{}).parsePricingData([]byte(catalog))
	require.NoError(t, err)
	channels := quoteTestChannel(ChannelModelPricing{
		Platform: PlatformOpenAI, Models: []string{"gpt-5.6-luna"}, BillingMode: BillingModeToken,
		InputPrice: float64Ptr(3e-7), OutputPrice: float64Ptr(1e-6),
		CacheWritePrice: float64Ptr(4e-7), CacheReadPrice: float64Ptr(3e-8),
	})
	f := newQuoteTestFixture(data, channels, []*Group{quoteTestGroup(1)}, nil)
	unit := func(tokens UsageTokens, n int) float64 {
		return f.referenceTokenCost(t, "gpt-5.6-luna", 1, tokens, "", time.Time{}).TotalCost / float64(n)
	}
	const n = 272_001
	billed := [4]float64{
		unit(UsageTokens{InputTokens: n}, n), unit(UsageTokens{OutputTokens: n}, n),
		unit(UsageTokens{CacheCreationTokens: n}, n), unit(UsageTokens{CacheReadTokens: n}, n),
	}
	// 输出不计入阈值，需要先放一笔超阈值输入：用「n 个输入 + n 个输出」减去纯输入部分。
	both := f.referenceTokenCost(t, "gpt-5.6-luna", 1, UsageTokens{InputTokens: n, OutputTokens: n}, "", time.Time{}).TotalCost
	billed[1] = (both - billed[0]*n) / n

	t.Logf("gpt-5.6-luna custom-price >272K billed unit prices (in/out/write/read): %v", billed)
	qt, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-5.6-luna", GroupID: quoteTestGroupID})
	require.NoError(t, err)
	entry := userPriceEntryFromQuote(qt)
	require.NotNil(t, entry)
	require.Len(t, entry.Tiers, 2)
	cs := &ChannelService{pricingService: &PricingService{pricingData: data}}
	models := channels[0].SupportedModels()
	cs.fillGlobalPricingFallback(models)
	require.Len(t, models, 1)
	iv := models[0].Pricing.Intervals
	require.Len(t, iv, 2)

	user := entry.Tiers[1].Prices
	channel := iv[1]
	for i, got := range [][2]*float64{
		{user.Input, channel.InputPrice}, {user.Output, channel.OutputPrice},
		{user.CacheWrite, channel.CacheWritePrice}, {user.CacheRead, channel.CacheReadPrice},
	} {
		require.NotNil(t, got[0], i)
		require.NotNil(t, got[1], i)
		require.InDelta(t, billed[i], *got[0], 1e-12, "user page vs billing, field %d", i)
		require.InDelta(t, billed[i], *got[1], 1e-12, "available channels vs billing, field %d", i)
	}
}

// cache above 价与输入倍率不一致：打 warn、照常按输入倍率计费，并让快照 diff 看得到 cache above 价的变化。
func TestGPT6CacheAboveRatioMismatchWarnsAndBillsByInputMultiplier(t *testing.T) {
	lunaWithCacheReadAbove := func(readAbove string) string {
		return `{"gpt-6-luna": {
    "input_cost_per_token": 1e-07, "output_cost_per_token": 5e-07,
    "cache_creation_input_token_cost": 1.25e-07, "cache_read_input_token_cost": 1e-08,
    "input_cost_per_token_above_272k_tokens": 2e-07, "output_cost_per_token_above_272k_tokens": 7.5e-07,
    "cache_creation_input_token_cost_above_272k_tokens": 2.5e-07, "cache_read_input_token_cost_above_272k_tokens": ` + readAbove + `,
    "litellm_provider": "openai", "mode": "chat", "supports_service_tier": true}}`
	}

	// 一致（读 2 倍）：不告警。
	sink, restore := captureStructuredLog(t)
	consistent, err := (&PricingService{}).parsePricingData([]byte(lunaWithCacheReadAbove("2e-08")))
	require.NoError(t, err)
	require.False(t, sink.ContainsMessage("cache above-272K ratio differs"))
	restore()

	// 差 0.5%：在容忍范围内，不告警。
	sink, restore = captureStructuredLog(t)
	_, err = (&PricingService{}).parsePricingData([]byte(lunaWithCacheReadAbove("2.01e-08")))
	require.NoError(t, err)
	require.False(t, sink.ContainsMessage("cache above-272K ratio differs"))
	restore()

	// 读 3 倍：告警，写明模型名与两个比值；计费仍按输入倍率 2 倍。
	sink, restore = captureStructuredLog(t)
	defer restore()
	mismatch, err := (&PricingService{}).parsePricingData([]byte(lunaWithCacheReadAbove("3e-08")))
	require.NoError(t, err)
	require.True(t, sink.ContainsMessageAtLevel("gpt-6-luna cache above-272K ratio differs", "warn"))
	require.True(t, sink.ContainsMessage("cache_read_ratio=3.0000"))
	require.True(t, sink.ContainsMessage("cache_write_ratio=2.0000"))
	require.True(t, sink.ContainsMessage("input_multiplier=2.0000"))
	require.True(t, sink.ContainsFieldValue("model", "gpt-6-luna"))

	svc := NewBillingService(&config.Config{}, &PricingService{pricingData: mismatch})
	long := UsageTokens{CacheReadTokens: 300_000}
	require.InDelta(t, 300_000*1e-8*2, remoteShapeCost(t, svc, "gpt-6-luna", "", long), 1e-12)

	// 快照 diff：cache above 价变化即使不影响计费，也体现在 changed fields 里。
	fields := changedPricingFields(consistent["gpt-6-luna"], mismatch["gpt-6-luna"])
	require.Contains(t, fields, "observed_cache_read_above_ratio")
	require.NotContains(t, fields, "observed_cache_write_above_ratio")
}
