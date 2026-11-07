//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

// 与真实远端目录一致的字段形状：只有 *_above_272k_tokens 绝对价，没有 long_context_* 字段。
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
    "input_cost_per_token": 5e-06, "output_cost_per_token": 3e-05,
    "cache_creation_input_token_cost": 6.25e-06, "cache_read_input_token_cost": 5e-07,
    "input_cost_per_token_priority": 1e-05, "output_cost_per_token_priority": 6e-05,
    "cache_creation_input_token_cost_priority": 1.25e-05, "cache_read_input_token_cost_priority": 1e-06,
    "input_cost_per_token_above_272k_tokens": 1e-05, "output_cost_per_token_above_272k_tokens": 4.5e-05,
    "cache_creation_input_token_cost_above_272k_tokens": 1.25e-05, "cache_read_input_token_cost_above_272k_tokens": 1e-06,
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

func TestLongContextDerivationEnabledOnlyForTerra(t *testing.T) {
	require.True(t, longContextDerivationEnabled("gpt-6.1-terra"))
	require.True(t, longContextDerivationEnabled("openai/gpt-6.1-terra-max"))
	for _, model := range []string{"gpt-6-sol", "gpt-6.1-sol", "gpt-6-astra", "gpt-6", "gpt-6-luna", "gpt-5.6-terra", "gpt-6.1-terra-pro"} {
		require.False(t, longContextDerivationEnabled(model), model)
	}
	data, err := (&PricingService{}).parsePricingData([]byte(remoteShapeAbove272KCatalog))
	require.NoError(t, err)
	require.Equal(t, 272_000, data["gpt-6.1-terra"].LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, data["gpt-6.1-terra"].LongContextInputCostMultiplier, 1e-9)
	require.InDelta(t, 1.5, data["gpt-6.1-terra"].LongContextOutputCostMultiplier, 1e-9)
	for _, model := range []string{"gpt-6-sol", "gpt-6.1-sol", "gpt-6-astra", "gpt-6-luna"} {
		require.Zero(t, data[model].LongContextInputTokenThreshold, model)
		require.Positive(t, data[model].InputCostPerTokenAbove272KTokens, model, "absolute fields untouched")
	}
	// 显式 long_context_* 字段优先，不被折算覆盖。
	explicit, err := (&PricingService{}).parsePricingData([]byte(`{"gpt-6.1-terra":{"input_cost_per_token":1e-06,"output_cost_per_token":5e-06,
		"long_context_input_token_threshold":100000,"long_context_input_cost_multiplier":3,"long_context_output_cost_multiplier":1,
		"input_cost_per_token_above_272k_tokens":2e-06}}`))
	require.NoError(t, err)
	require.Equal(t, 100_000, explicit["gpt-6.1-terra"].LongContextInputTokenThreshold)
	require.InDelta(t, 3.0, explicit["gpt-6.1-terra"].LongContextInputCostMultiplier, 1e-9)
}

// 回归锁：其它 GPT-6 模型在同样的远端形状下，超过 27.2 万时扣费必须等于不加价的结果。
func TestGPT6OtherModelsLongContextUnchangedWithAboveTierOnlyCatalog(t *testing.T) {
	svc := newRemoteShapeBillingService(t)
	type base struct{ in, out, write, read, pin, pout, pwrite, pread float64 }
	rates := map[string]base{
		"gpt-6-sol":   {2e-6, 10e-6, 2.5e-6, 0.2e-6, 4e-6, 20e-6, 5e-6, 0.4e-6},
		"gpt-6.1-sol": {2e-6, 10e-6, 2.5e-6, 0.1e-6, 4e-6, 20e-6, 5e-6, 0.2e-6},
		"gpt-6-astra": {5e-6, 30e-6, 6.25e-6, 0.5e-6, 10e-6, 60e-6, 12.5e-6, 1e-6},
		"gpt-6-luna":  {0.1e-6, 0.5e-6, 0.125e-6, 0.01e-6, 0.2e-6, 1e-6, 0.25e-6, 0.02e-6},
	}
	long := UsageTokens{InputTokens: 200_000, CacheCreationTokens: 50_000, CacheReadTokens: 50_001, OutputTokens: 100_000}
	for model, r := range rates {
		wantStd := 200_000*r.in + 50_000*r.write + 50_001*r.read + 100_000*r.out
		wantPriority := 200_000*r.pin + 50_000*r.pwrite + 50_001*r.pread + 100_000*r.pout
		require.InDelta(t, wantStd, remoteShapeCost(t, svc, model, "", long), 1e-9, model)
		require.InDelta(t, wantPriority, remoteShapeCost(t, svc, model, "priority", long), 1e-9, model)
		require.InDelta(t, wantStd/2, remoteShapeCost(t, svc, model, "flex", long), 1e-9, model)
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
