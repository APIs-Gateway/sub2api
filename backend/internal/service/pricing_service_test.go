package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPricingSchedulerBlankRemoteURLDoesNotStart(t *testing.T) {
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{RemoteURL: "  \t  "}}, nil)
	defer svc.Stop()

	svc.startUpdateScheduler()
	done := make(chan struct{})
	go func() {
		svc.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("blank remote URL must not start scheduler")
	}
}

func TestPricingNonEmptyInvalidRemoteURLStillReturnsValidationError(t *testing.T) {
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "://invalid",
		DataDir:   t.TempDir(),
	}}, nil)

	err := svc.ForceUpdate()

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid pricing url")
}

func TestParsePricingData_ParsesPriorityAndServiceTierFields(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_above_272k_tokens": 0.000005,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_above_272k_tokens": 0.0000225,
			"output_cost_per_token_priority": 0.00003,
			"cache_creation_input_token_cost": 0.0000025,
			"cache_creation_input_token_cost_above_272k_tokens": 0.000005,
			"cache_creation_input_token_cost_priority": 0.000005,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_above_272k_tokens": 0.0000005,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"supports_prompt_caching": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	pricing := data["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenAbove272KTokens, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 22.5e-6, pricing.OutputCostPerTokenAbove272KTokens, 1e-12)
	require.InDelta(t, 3e-5, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreationInputTokenCostAbove272KTokens, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 5e-7, pricing.CacheReadInputTokenCostAbove272KTokens, 1e-12)
	require.InDelta(t, 5e-7, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

// TestGPT6AstraDedicatedFallbacksUseOfficialRates 验证 gpt-6-astra 无论走
// billing_service.go 的硬编码 fallback（pricingService=nil）还是
// pricing_service.go 的动态目录未命中 fallback（pricingData 为空 map），
// 都落在同一套官方标准价 + 长上下文倍率上。
//
// 注意：本测试没有照抄上游 PR 里额外的
// TestBillingServiceGPT6AstraUsesOfficialPricingAcrossTiersAndLongContext
// （priority tier 叠加长上下文倍率的用例）。那个测试依赖上游
// billing_service.go 里的 openAIModelFastPricingRatio/
// enforceOpenAIFastPricingRatio 机制——fork 当前完全没有这套机制，
// GPT-5.6 系列的 Fast 档倍率是在 fallback 结构体里直接写死 InputPricePerTokenPriority
// 等字段，而不是运行时用倍率函数改写。照搬那个测试会绑死一个 fork 里不存在的
// 内部函数，因此这里只保留不依赖该机制的基础费率断言。
func TestGPT6AstraDedicatedFallbacksUseOfficialRates(t *testing.T) {
	tests := []struct {
		name string
		svc  *BillingService
	}{
		{name: "pricing_service", svc: NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}})},
		{name: "billing_service", svc: NewBillingService(&config.Config{}, nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing, err := tt.svc.GetModelPricing("gpt-6-astra")
			require.NoError(t, err)
			require.InDelta(t, 10e-6, pricing.InputPricePerToken, 1e-12)
			require.InDelta(t, 20e-6, pricing.InputPricePerTokenPriority, 1e-12)
			require.InDelta(t, 50e-6, pricing.OutputPricePerToken, 1e-12)
			require.InDelta(t, 100e-6, pricing.OutputPricePerTokenPriority, 1e-12)
			require.InDelta(t, 12.5e-6, pricing.CacheCreationPricePerToken, 1e-12)
			require.InDelta(t, 25e-6, pricing.CacheCreationPricePerTokenPriority, 1e-12)
			require.InDelta(t, 1e-6, pricing.CacheReadPricePerToken, 1e-12)
			require.InDelta(t, 2e-6, pricing.CacheReadPricePerTokenPriority, 1e-12)
			require.Equal(t, 272_000, pricing.LongContextInputThreshold)
			require.InDelta(t, 2.0, pricing.LongContextInputMultiplier, 1e-12)
			require.InDelta(t, 1.5, pricing.LongContextOutputMultiplier, 1e-12)
		})
	}
}

// TestPricingServiceBareGPT6AliasUsesAstra 验证裸 "gpt-6"/"openai/gpt-6" 别名
// 在动态价格目录里直接命中 "gpt-6-astra" 条目（而不是回退到静态兜底价）。
func TestPricingServiceBareGPT6AliasUsesAstra(t *testing.T) {
	astraPricing := &LiteLLMModelPricing{InputCostPerToken: 123e-6, OutputCostPerToken: 456e-6}
	pricingSvc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gpt-6-astra": astraPricing}}
	for _, model := range []string{"gpt-6", "openai/gpt-6"} {
		pricing := pricingSvc.GetModelPricing(model)
		require.Same(t, astraPricing, pricing)
	}
}

func TestGetModelPricing_Gpt53CodexSparkUsesGpt51CodexPricing(t *testing.T) {
	sparkPricing := &LiteLLMModelPricing{InputCostPerToken: 1}
	gpt53Pricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": sparkPricing,
			"gpt-5.3":       gpt53Pricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex-spark")
	require.Same(t, sparkPricing, got)
}

func TestGetModelPricing_Gpt53CodexFallbackStillUsesGpt52Codex(t *testing.T) {
	gpt52CodexPricing := &LiteLLMModelPricing{InputCostPerToken: 2}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": gpt52CodexPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, gpt52CodexPricing, got)
}

func TestGetModelPricing_OpenAIFallbackMatchedLoggedAsInfo(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	gpt52CodexPricing := &LiteLLMModelPricing{InputCostPerToken: 2}
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": gpt52CodexPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, gpt52CodexPricing, got)

	require.True(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.2-codex", "info"))
	require.False(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.2-codex", "warn"))
}

func TestGetModelPricing_Gpt54UsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": &LiteLLMModelPricing{InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-7, got.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_Gpt56UsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.6-luna")
	require.NotNil(t, got)
	require.InDelta(t, 2e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.2e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-7, got.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 5e-7, got.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 2e-8, got.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 4e-7, got.InputCostPerTokenAbove272KTokens, 1e-12)
	require.InDelta(t, 1.8e-6, got.OutputCostPerTokenAbove272KTokens, 1e-12)
	require.InDelta(t, 5e-7, got.CacheCreationInputTokenCostAbove272KTokens, 1e-12)
	require.InDelta(t, 4e-8, got.CacheReadInputTokenCostAbove272KTokens, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_Gpt56StaleCatalogDerivesCacheWriteButExplicitPriceWins(t *testing.T) {
	stale := &LiteLLMModelPricing{
		InputCostPerToken:                      2.5e-6,
		InputCostPerTokenPriority:              5e-6,
		InputCostPerTokenAbove272KTokens:       5e-6,
		OutputCostPerTokenAbove272KTokens:      22.5e-6,
		CacheReadInputTokenCostAbove272KTokens: 0.5e-6,
		LongContextInputTokenThreshold:         272000,
		LongContextInputCostMultiplier:         2.0,
		LongContextOutputCostMultiplier:        1.5,
	}
	explicit := &LiteLLMModelPricing{
		InputCostPerToken:                   5e-6,
		InputCostPerTokenPriority:           10e-6,
		CacheCreationInputTokenCost:         7e-6,
		CacheCreationInputTokenCostPriority: 14e-6,
	}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.6-terra": stale,
		"gpt-5.6-sol":   explicit,
	}}

	derived := svc.GetModelPricing("gpt-5.6-terra")
	require.NotNil(t, derived)
	require.NotSame(t, stale, derived, "derivation must not mutate the cached catalog entry")
	require.InDelta(t, 3.125e-6, derived.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 6.25e-6, derived.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 5e-6, derived.InputCostPerTokenAbove272KTokens, 1e-12)
	require.InDelta(t, 22.5e-6, derived.OutputCostPerTokenAbove272KTokens, 1e-12)
	require.InDelta(t, 0.5e-6, derived.CacheReadInputTokenCostAbove272KTokens, 1e-12)
	require.Equal(t, 272000, derived.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, derived.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, derived.LongContextOutputCostMultiplier, 1e-12)

	gotExplicit := svc.GetModelPricing("gpt-5.6-sol")
	require.NotSame(t, explicit, gotExplicit, "GPT-5.6 policy must not mutate the cached catalog entry")
	require.InDelta(t, 7e-6, gotExplicit.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 14e-6, gotExplicit.CacheCreationInputTokenCostPriority, 1e-12)
	require.Equal(t, 272000, gotExplicit.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, gotExplicit.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, gotExplicit.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_OpenAICompactAliasUsesStaticFallback(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("openai/gpt5.5")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
}

// gemini-3.6-flash 的 -high/-low/-medium/-tiered thinking-tier 别名与 base 模型同价；
// normalizeGeminiThinkingTierAlias 只在 catalog(pricingData) 里恰好只有 base 条目时才需要
// 归一化命中 —— 这里直接构造 pricingData 验证，不依赖 resources/model-pricing 里的真实数据
// (那份 JSON 目录文件不在 bill-core-02 批次的 scope 内，未同步更新)。
func TestPricingService_Gemini36FlashThinkingTiersUseBasePricing(t *testing.T) {
	basePricing := &LiteLLMModelPricing{
		InputCostPerToken:       1.5e-6,
		OutputCostPerToken:      7.5e-6,
		CacheReadInputTokenCost: 0.15e-6,
	}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gemini-3.6-flash": basePricing,
	}}

	for _, model := range []string{
		"gemini-3.6-flash",
		"gemini-3.6-flash-high",
		"gemini-3.6-flash-low",
		"gemini-3.6-flash-medium",
		"gemini-3.6-flash-tiered",
	} {
		t.Run(model, func(t *testing.T) {
			require.Same(t, basePricing, svc.GetModelPricing(model))
		})
	}
}

func TestPricingService_Gemini36FlashTierSpecificPricingTakesPrecedence(t *testing.T) {
	basePricing := &LiteLLMModelPricing{InputCostPerToken: 1.5e-6}
	tierPricing := &LiteLLMModelPricing{InputCostPerToken: 2e-6}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gemini-3.6-flash":     basePricing,
		"gemini-3.6-flash-low": tierPricing,
	}}

	require.Same(t, tierPricing, svc.GetModelPricing("models/gemini-3.6-flash-low"))
}

// 反$0计费的核心保证：即便 pricingData(动态/远程目录)里完全没有 gemini-3.6-flash，
// BillingService 也必须落到 getFallbackPricing 的静态兜底价，而不是记 $0。
func TestBillingService_Gemini36FlashThinkingTierFallbacksAreBillable(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}

	for _, model := range []string{
		"gemini-3.6-flash",
		"gemini-3.6-flash-high",
		"gemini-3.6-flash-low",
		"gemini-3.6-flash-medium",
		"gemini-3.6-flash-tiered",
	} {
		t.Run(model, func(t *testing.T) {
			cost, err := svc.CalculateCost(model, tokens, 1)
			require.NoError(t, err)
			require.InDelta(t, 1.5, cost.InputCost, 1e-12)
			require.InDelta(t, 7.5, cost.OutputCost, 1e-12)
			require.InDelta(t, 0.15, cost.CacheReadCost, 1e-12)
			require.InDelta(t, 9.15, cost.TotalCost, 1e-12)
		})
	}
}

// "紧凑别名 + 日期后缀"（openai/gpt5.6-terra-2026-06-08）在 catalog 查找阶段命不中
// catalog 里的 gpt-5.6-terra，会落到编译进二进制的静态 fallback。这里放一个价格与静态
// fallback 明显不同的 catalog 条目，把该行为钉死：如果哪天 lookup 改成能命中 catalog，
// 本用例会失败，提醒一并复核 docs/specs/available-channels-gpt-5-6-pricing.md §3 的数据
// 路径描述，以及"远程 catalog 调价后这类模型名会滞后到下次发版"的影响面。
func TestGetModelPricing_Gpt56CompactAliasWithDateUsesStaticFallback(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.6-terra": {InputCostPerToken: 9e-6, OutputCostPerToken: 9e-5},
	}}

	got := svc.GetModelPricing("openai/gpt5.6-terra-2026-06-08")
	require.NotNil(t, got)
	require.InDelta(t, 2e-6, got.InputCostPerToken, 1e-12, "catalog 未命中时应回落到静态 fallback")
	require.InDelta(t, 1.2e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-6, got.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 2e-7, got.CacheReadInputTokenCost, 1e-12)
}

func TestDefaultPricingIncludesCodexAutoReview(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	svc.pricingData = pricingData

	got := svc.GetModelPricing("codex-auto-review")
	require.NotNil(t, got)
	require.InDelta(t, 5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 3e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 5e-7, got.CacheReadInputTokenCost, 1e-12)
}

func TestDefaultPricingIncludesGPT56LongContextMetadata(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)

	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		pricing := pricingData[model]
		require.NotNil(t, pricing, model)
		require.Equal(t, 272000, pricing.LongContextInputTokenThreshold, model)
		require.InDelta(t, 2.0, pricing.LongContextInputCostMultiplier, 1e-12, model)
		require.InDelta(t, 1.5, pricing.LongContextOutputCostMultiplier, 1e-12, model)
	}
}

func TestDefaultPricingIncludesGPT56CacheWritePrices(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	svc.pricingData = pricingData

	for _, tt := range []struct {
		model      string
		input      float64
		cacheRead  float64
		cacheWrite float64
		output     float64
		priority   float64
	}{
		{model: "gpt-5.6-sol", input: 5e-6, cacheRead: 0.5e-6, cacheWrite: 6.25e-6, output: 30e-6, priority: 12.5e-6},
		{model: "gpt-5.6-terra", input: 2e-6, cacheRead: 0.2e-6, cacheWrite: 2.5e-6, output: 12e-6, priority: 5e-6},
		{model: "gpt-5.6-luna", input: 0.2e-6, cacheRead: 0.02e-6, cacheWrite: 0.25e-6, output: 1.2e-6, priority: 0.5e-6},
	} {
		t.Run(tt.model, func(t *testing.T) {
			got := svc.GetModelPricing(tt.model)
			require.NotNil(t, got)
			require.InDelta(t, tt.input, got.InputCostPerToken, 1e-12)
			require.InDelta(t, tt.cacheRead, got.CacheReadInputTokenCost, 1e-12)
			require.InDelta(t, tt.cacheWrite, got.CacheCreationInputTokenCost, 1e-12)
			require.InDelta(t, tt.output, got.OutputCostPerToken, 1e-12)
			require.InDelta(t, tt.input*2, got.InputCostPerTokenAbove272KTokens, 1e-12)
			require.InDelta(t, tt.cacheRead*2, got.CacheReadInputTokenCostAbove272KTokens, 1e-12)
			require.InDelta(t, tt.cacheWrite*2, got.CacheCreationInputTokenCostAbove272KTokens, 1e-12)
			require.InDelta(t, tt.output*1.5, got.OutputCostPerTokenAbove272KTokens, 1e-12)
			require.InDelta(t, tt.priority, got.CacheCreationInputTokenCostPriority, 1e-12)
		})
	}
}

func TestGetModelPricing_Gpt54MiniUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-mini")
	require.NotNil(t, got)
	require.InDelta(t, 7.5e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 4.5e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 7.5e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_Gpt54NanoUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-nano")
	require.NotNil(t, got)
	require.InDelta(t, 2e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.25e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_ImageModelDoesNotFallbackToTextModel(t *testing.T) {
	imagePricing := &LiteLLMModelPricing{InputCostPerToken: 3}
	textPricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-image-2": imagePricing,
			"gpt-5.4":     textPricing,
		},
	}

	got := svc.GetModelPricing("gpt-image-3")
	require.Same(t, imagePricing, got)
}

func TestParsePricingData_PreservesPriorityAndServiceTierFields(t *testing.T) {
	raw := map[string]any{
		"gpt-5.4": map[string]any{
			"input_cost_per_token":                 2.5e-6,
			"input_cost_per_token_priority":        5e-6,
			"output_cost_per_token":                15e-6,
			"output_cost_per_token_priority":       30e-6,
			"cache_read_input_token_cost":          0.25e-6,
			"cache_read_input_token_cost_priority": 0.5e-6,
			"supports_service_tier":                true,
			"supports_prompt_caching":              true,
			"litellm_provider":                     "openai",
			"mode":                                 "chat",
		},
	}
	body, err := json.Marshal(raw)
	require.NoError(t, err)

	svc := &PricingService{}
	pricingMap, err := svc.parsePricingData(body)
	require.NoError(t, err)

	pricing := pricingMap["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 2.5e-6, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 30e-6, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.5e-6, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_PreservesServiceTierPriorityFields(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	pricing := pricingData["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 0.0000025, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.000005, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.000015, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 0.00003, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.00000025, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.0000005, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

// ---------------------------------------------------------------------------
// ListModelNamesByProvider
// ---------------------------------------------------------------------------

func TestListModelNamesByProvider_ReturnsMatchingModels(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-4-5-20251101": {LiteLLMProvider: "anthropic", InputCostPerToken: 1.5e-5},
			"claude-sonnet-4-5":        {LiteLLMProvider: "anthropic", InputCostPerToken: 3e-6},
			"gpt-4o":                   {LiteLLMProvider: "openai", InputCostPerToken: 5e-6},
			"gemini-2.5-pro":           {LiteLLMProvider: "google", InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.ListModelNamesByProvider("anthropic")
	require.ElementsMatch(t, []string{"claude-opus-4-5-20251101", "claude-sonnet-4-5"}, got)
	// Must be sorted
	require.Equal(t, "claude-opus-4-5-20251101", got[0])
	require.Equal(t, "claude-sonnet-4-5", got[1])
}

func TestListModelNamesByProvider_CaseInsensitive(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-4o": {LiteLLMProvider: "OpenAI", InputCostPerToken: 5e-6},
		},
	}

	got := svc.ListModelNamesByProvider("openai")
	require.Equal(t, []string{"gpt-4o"}, got)

	got2 := svc.ListModelNamesByProvider("OPENAI")
	require.Equal(t, []string{"gpt-4o"}, got2)
}

func TestListModelNamesByProvider_NoMatch(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-4o": {LiteLLMProvider: "openai", InputCostPerToken: 5e-6},
		},
	}

	got := svc.ListModelNamesByProvider("anthropic")
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestListModelNamesByProvider_EmptyCatalog(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{},
	}

	got := svc.ListModelNamesByProvider("openai")
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestParsePricingData_PreservesImageInputTokenPriceWithoutTextPrice(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-image-2": {
			"input_cost_per_image_token": 0.00000001,
			"output_cost_per_image_token": 0.00000002,
			"litellm_provider": "openai",
			"mode": "image_generation"
		}
	}`))
	require.NoError(t, err)

	pricing := pricingData["gpt-image-2"]
	require.NotNil(t, pricing)
	require.InDelta(t, 1e-8, pricing.InputCostPerImageToken, 1e-12)
	require.InDelta(t, 2e-8, pricing.OutputCostPerImageToken, 1e-12)
}

func TestParsePricingData_PreservesImageOutputTokenPriceWithoutOtherPrices(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-image-output-only": {
			"output_cost_per_image_token": 0.00000002,
			"litellm_provider": "openai",
			"mode": "image_generation"
		}
	}`))
	require.NoError(t, err)

	pricing := pricingData["gpt-image-output-only"]
	require.NotNil(t, pricing)
	require.InDelta(t, 2e-8, pricing.OutputCostPerImageToken, 1e-12)
}
