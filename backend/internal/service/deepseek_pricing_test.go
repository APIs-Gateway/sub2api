//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// withDeepseekNow 临时替换包级可替换时钟钩子 deepseekNowFunc 为固定时刻，
// 供峰谷倍率测试做确定性断言；测试结束后自动恢复为原值（默认 timezone.Now）。
// fork 未同步上游渠道分时倍率定价前置提交，CostInput 没有 PricingAt 字段，
// 因此峰谷判定的"当前时刻"通过这个包级钩子注入，而不是按请求传参。
func withDeepseekNow(t *testing.T, at time.Time) {
	t.Helper()
	original := deepseekNowFunc
	deepseekNowFunc = func() time.Time { return at }
	t.Cleanup(func() { deepseekNowFunc = original })
}

// ---------------------------------------------------------------------------
// deepseekPeakMultiplierAt：官方峰谷口径（2026-08-23 起生效）
// 高峰时段 01:00–04:00 与 06:00–10:00 UTC（半开区间，仅工作日）；
// 北京时间周六/周日全天低谷；高峰价 = 2× 低谷价。
// 2026-08-24 为周一（工作日），2026-08-22 周六、2026-08-23 周日。
// ---------------------------------------------------------------------------

func TestDeepseekPeakMultiplierAt(t *testing.T) {
	mon := func(hour, min int) time.Time { return time.Date(2026, 8, 24, hour, min, 0, 0, time.UTC) }
	sat := func(hour, min int) time.Time { return time.Date(2026, 8, 22, hour, min, 0, 0, time.UTC) }
	sun := func(hour, min int) time.Time { return time.Date(2026, 8, 23, hour, min, 0, 0, time.UTC) }

	tests := []struct {
		name string
		now  time.Time
		want float64
	}{
		// 工作日高峰窗口边界（半开区间）
		{"weekday 01:00 peak start", mon(1, 0), 2.0},
		{"weekday 03:59 peak upper bound", mon(3, 59), 2.0},
		{"weekday 04:00 peak end", mon(4, 0), 1.0},
		{"weekday 06:00 peak start", mon(6, 0), 2.0},
		{"weekday 09:59 peak upper bound", mon(9, 59), 2.0},
		{"weekday 10:00 peak end", mon(10, 0), 1.0},
		// 工作日低谷时段
		{"weekday 00:00 off-peak", mon(0, 0), 1.0},
		{"weekday 05:00 off-peak", mon(5, 0), 1.0},
		{"weekday 12:00 off-peak", mon(12, 0), 1.0},
		{"weekday 23:59 off-peak", mon(23, 59), 1.0},
		// 北京时间周末全天低谷（即使 UTC 处于高峰时段）
		{"saturday utc 02:00 beijing sat 10:00", sat(2, 0), 1.0},
		{"sunday utc 07:00 beijing sun 15:00", sun(7, 0), 1.0},
		// 北京时间与 UTC 跨日边界：UTC 周六 16:30 = 北京周日 00:30 → 周末低谷
		{"utc saturday 16:30 = beijing sunday 00:30", sat(16, 30), 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, deepseekPeakMultiplierAt(tt.now))
		})
	}
}

func TestIsDeepSeekModel(t *testing.T) {
	deepseek := []string{
		"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp",
		"deepseek-chat", "deepseek-reasoner", "deepseek-v3-2-251201",
		"deepseek-coder", "deepseek-foo", "deepseek-v4-pro-0813",
		"DEEPSEEK-V4-PRO", " deepseek-v4-flash ",
	}
	for _, m := range deepseek {
		require.True(t, isDeepSeekModel(m), "model %q should be deepseek", m)
	}

	nonDeepseek := []string{
		"gpt-5.4", "claude-sonnet-4", "deepseekcoder", // 无连字符不算 deepseek- 前缀
		"", " deepseek", // 无连字符后缀
	}
	for _, m := range nonDeepseek {
		require.False(t, isDeepSeekModel(m), "model %q should not be deepseek", m)
	}
}

// ---------------------------------------------------------------------------
// 默认价卡（Source=LiteLLM）按官方峰谷倍率计费；分组/渠道自定义定价不叠加
// ---------------------------------------------------------------------------

func TestCalculateCostUnified_DeepseekDefaultCardPeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	// 低谷成本（2026-09-10 官方新价）：1000*1.5e-7 + 500*6e-7 + 1000*3e-9 = 4.53e-4
	offPeakTotal := 1000*1.5e-7 + 500*6e-7 + 1000*3e-9

	withDeepseekNow(t, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)) // 周一低谷
	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	withDeepseekNow(t, time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC)) // 周一高峰
	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

func TestCalculateCostUnified_DeepseekProDefaultCardPeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	offPeakTotal := 1000*6.6e-7 + 500*1.98e-6 + 1000*2.2e-8

	withDeepseekNow(t, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	withDeepseekNow(t, time.Date(2026, 8, 24, 6, 30, 0, 0, time.UTC)) // 周一高峰
	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

func TestCalculateCostUnified_DeepseekVersionedNamePeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	offPeakTotal := 1000*1.5e-7 + 500*6e-7 + 1000*3e-9

	withDeepseekNow(t, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)) // 周一低谷
	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash-0731", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	withDeepseekNow(t, time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC)) // 周一高峰
	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash-0731", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

// TestCalculateCostUnified_DeepseekGroupPricingNotScaledByPeak 验证分组/渠道
// 自定义定价（Source=Channel）既不被强制覆盖官方价，也不叠加官方峰谷倍率。
// fork 的 ModelPricingResolver.Resolve 只接受 GroupID（不像上游那样接受完整
// Group 对象），这里改为直接构造预解析的 ResolvedPricing 并通过
// CostInput.Resolved 注入，等价于渠道解析出 Source=PricingSourceChannel 的结果，
// 避免额外搭建 ChannelService mock。
func TestCalculateCostUnified_DeepseekGroupPricingNotScaledByPeak(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	// 分组自定义价：1000*1e-6 + 500*2e-6 + 1000*3e-9（缓存读沿用官方 flash 价）
	groupTotal := 1000*1e-6 + 500*2e-6 + 1000*3e-9

	resolved := &ResolvedPricing{
		Mode:   BillingModeToken,
		Source: PricingSourceChannel,
		BasePricing: &ModelPricing{
			InputPricePerToken:     1e-6,
			OutputPricePerToken:    2e-6,
			CacheReadPricePerToken: deepseekFlashOffPeakCacheRead,
		},
	}

	for _, at := range []time.Time{
		time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), // 低谷
		time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC),  // 高峰
	} {
		withDeepseekNow(t, at)
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "deepseek-v4-flash",
			Tokens: tokens, RateMultiplier: 1.0, Resolver: resolver, Resolved: resolved,
		})
		require.NoError(t, err)
		require.InDelta(t, groupTotal, cost.TotalCost, 1e-10,
			"分组自定义定价不应叠加官方峰谷倍率（now=%v）", at)
	}
}

func TestCalculateCostUnified_NonDeepseekDefaultCardNotScaledByPeak(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	total := 1000*3e-6 + 500*15e-6 // claude-sonnet-4 fallback

	for _, at := range []time.Time{
		time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC),
	} {
		withDeepseekNow(t, at)
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "claude-sonnet-4", Tokens: tokens,
			RateMultiplier: 1.0, Resolver: resolver,
		})
		require.NoError(t, err)
		require.InDelta(t, total, cost.TotalCost, 1e-10,
			"非 DeepSeek 模型不应受官方峰谷倍率影响（now=%v）", at)
	}
}

// ---------------------------------------------------------------------------
// 官方价强制覆盖（远端旧价兜底）与未知 deepseek-* flash 兜底
// ---------------------------------------------------------------------------

func TestGetModelPricing_DeepseekForcesOfficialRatesOverJSON(t *testing.T) {
	// JSON 给任意价（模拟远端旧价/占位价），deepseek-* 必须被强制覆盖为官方低谷价。
	pricingSvc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"deepseek-flash":               {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-v4-flash":            {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-v4-pro":              {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-v4-flash-vision-exp": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-chat":                {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-reasoner":            {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
	}}
	bs := NewBillingService(&config.Config{}, pricingSvc)

	tests := []struct {
		model                    string
		input, output, cacheRead float64
	}{
		// 2026-09-10 官方降价后：deepseek-flash（V4.1-Flash 新名）与旧名
		// deepseek-v4-flash 同按 Flash 新价。
		{"deepseek-flash", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-v4-flash", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-v4-flash-vision-exp", 1.5e-7, 6e-7, 3e-9},
		// 已停服的 chat/reasoner：即使 JSON 有旧条目也按 flash 价兜底。
		{"deepseek-chat", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-reasoner", 1.5e-7, 6e-7, 3e-9},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			// flash 档三档价与 pro→Flash 切换无关，GetModelPricing 断言不随时间翻转。
			pricing, err := bs.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tt.cacheRead, pricing.CacheReadPricePerToken, 1e-15)
			// 固定时点（切换点之后的 2026-10-01）复核：仍走 Flash 新价。
			atPricing, err := bs.getModelPricingAt(tt.model, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			require.InDelta(t, tt.input, atPricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tt.output, atPricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tt.cacheRead, atPricing.CacheReadPricePerToken, 1e-15)
		})
	}

	// pro 档（含版本化名称）：断言经固定时点的 getModelPricingAt，不依赖墙上时钟。
	// 2026-08-01 早于切换点 2026-09-14 04:00 UTC → Pro 价。
	for _, model := range []string{"deepseek-v4-pro", "deepseek-v4-pro-0813"} {
		t.Run(model+"/before-cutoff", func(t *testing.T) {
			pricing, err := bs.getModelPricingAt(model, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			require.InDelta(t, 6.6e-7, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, 1.98e-6, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, 2.2e-8, pricing.CacheReadPricePerToken, 1e-15)
		})
	}

	// 半开边界钉死：2026-09-14 03:59:59 仍 Pro 价，04:00:00 整起 Flash 新价。
	proBefore, err := bs.getModelPricingAt("deepseek-v4-pro", time.Date(2026, 9, 14, 3, 59, 59, 0, time.UTC))
	require.NoError(t, err)
	require.InDelta(t, 6.6e-7, proBefore.InputPricePerToken, 1e-15)
	require.InDelta(t, 1.98e-6, proBefore.OutputPricePerToken, 1e-15)
	require.InDelta(t, 2.2e-8, proBefore.CacheReadPricePerToken, 1e-15)
	proAtCutoff, err := bs.getModelPricingAt("deepseek-v4-pro", time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.InDelta(t, 1.5e-7, proAtCutoff.InputPricePerToken, 1e-15)
	require.InDelta(t, 6e-7, proAtCutoff.OutputPricePerToken, 1e-15)
	require.InDelta(t, 3e-9, proAtCutoff.CacheReadPricePerToken, 1e-15)

	// 无显式时点的 GetModelPricing 走 deepseekNowFunc：钩子固定在切换前后分别断言。
	withDeepseekNow(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	proNowBefore, err := bs.GetModelPricing("deepseek-v4-pro")
	require.NoError(t, err)
	require.InDelta(t, 6.6e-7, proNowBefore.InputPricePerToken, 1e-15)
	withDeepseekNow(t, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC))
	proNowAfter, err := bs.GetModelPricing("deepseek-v4-pro")
	require.NoError(t, err)
	require.InDelta(t, 1.5e-7, proNowAfter.InputPricePerToken, 1e-15)

	// 版本化名称（不在 JSON / fallbackPrices 精确表中）：按子串归档计价。
	versioned := []struct {
		model                    string
		input, output, cacheRead float64
	}{
		{"deepseek-v4-flash-0731", 1.5e-7, 6e-7, 3e-9},
	}
	for _, tt := range versioned {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := bs.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tt.cacheRead, pricing.CacheReadPricePerToken, 1e-15)
		})
	}
}

func TestGetModelPricing_UnknownDeepseekMapsToFlash(t *testing.T) {
	// JSON 含 $0 占位条目（如旧 deepseek-v3-2-251201）：未知 deepseek-* 不再
	// fail-closed，统一按 flash 价兜底（1.5e-7/6e-7/3e-9），不得按 $0 计费。
	pricingSvc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"deepseek-v3-2-251201": {InputCostPerToken: 0, OutputCostPerToken: 0},
	}}
	bs := NewBillingService(&config.Config{}, pricingSvc)

	for _, m := range []string{"deepseek-v3-2-251201", "deepseek-chat", "deepseek-reasoner", "deepseek-foo"} {
		t.Run(m, func(t *testing.T) {
			pricing, err := bs.GetModelPricing(m)
			require.NoError(t, err)
			require.InDelta(t, 1.5e-7, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, 6e-7, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, 3e-9, pricing.CacheReadPricePerToken, 1e-15)
		})
	}
}

// ---------------------------------------------------------------------------
// 2026-09-10 官方降价：deepseek-flash（V4.1-Flash 新名）与旧名同价；
// 2026-09-14 04:00 UTC 起 deepseek-v4-pro 按上游路由改按 Flash 价计费
// ---------------------------------------------------------------------------

func TestCalculateCostUnified_DeepseekFlashAndLegacyFlashShareNewRates(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	offPeakTotal := 1000*1.5e-7 + 500*6e-7 + 1000*3e-9

	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash"} {
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: model, Tokens: tokens,
			RateMultiplier: 1.0, Resolver: resolver,
			PricingAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		})
		require.NoError(t, err)
		require.InDelta(t, offPeakTotal, cost.TotalCost, 1e-10, "model %s must use new flash rates", model)
	}
}

func TestCalculateCostUnified_DeepseekProRoutesToFlashAtCutoff(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	proTotal := 1000*6.6e-7 + 500*1.98e-6 + 1000*2.2e-8 // 切换前 Pro 价
	flashTotal := 1000*1.5e-7 + 500*6e-7 + 1000*3e-9    // 切换后 Flash 价

	before, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, proTotal, before.TotalCost, 1e-10)

	after, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, flashTotal, after.TotalCost, 1e-10)

	versioned, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro-0813", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, flashTotal, versioned.TotalCost, 1e-10)
}

// 显式 PricingAt 优先于 deepseekNowFunc：钩子指向低谷，请求时点在高峰，按高峰计。
func TestCalculateCostUnified_DeepseekExplicitPricingAtOverridesNow(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)
	tokens := UsageTokens{InputTokens: 1000}

	withDeepseekNow(t, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)) // 低谷
	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC), // 高峰
	})
	require.NoError(t, err)
	require.InDelta(t, 1000*1.5e-7*2, cost.TotalCost, 1e-12)
}
