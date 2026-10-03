//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 写死的绝对费用断言。期望值全部手算（价格来源写在各用例注释里），不经过任何计费函数，
// 这样 effectiveTokenPricing / computeTokenBreakdown 被改坏时这里会报错。
// 比较一律用 InDelta：浮点累加有 1e-12 量级的噪声。

func quoteTestGroupOn(platform string, rate float64) *Group {
	return &Group{ID: quoteTestGroupID, Platform: platform, RateMultiplier: rate}
}

// quoteUsageEven 构造「输入 / 输出 / 缓存读取各 n」的用量。
func quoteUsageEven(n int) UsageTokens {
	return UsageTokens{InputTokens: n, OutputTokens: n, CacheReadTokens: n}
}

type quoteAbsoluteCase struct {
	name   string
	model  string
	group  *Group
	tier   string
	at     time.Time
	tokens UsageTokens
	want   float64 // 手算的 ActualCost（倍率 1）
	path   QuotePricingPath
}

func TestPriceQuoter_AbsoluteCosts(t *testing.T) {
	// 目录里故意放 1e-6 / 2e-6 的「旧价」：DeepSeek 一律被官方价卡覆盖，不该读到它。
	flashJunkCatalog := map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
	}
	// 价格来源：
	//   deepseek-v4-flash 官方低谷价（billing_service.go deepseekFlashOffPeak*）：
	//     input $0.15/MTok、output $0.60/MTok、cache read $0.003/MTok，无 cache write。
	//     峰时 ×2（deepseekPeakMultiplierAt，unified 路径叠加；非 OpenAI 网关上无渠道价的 DeepSeek 也走 unified）。
	//   gpt-5.5 兜底价（newOpenAIGPT55FallbackPricing）：
	//     标准 input $2.5 / output $15 / cache read $0.25 每 MTok；
	//     priority = 标准 × 2.5（input 6.25、output 37.5、cache read 0.625）；
	//     长上下文：input+cache read+cache write 超过 272000 时 input/cache read ×2、output ×1.5；
	//     PriorityExcludesLongContext=false，所以 priority 与长上下文叠加。
	//
	// 用量 N：输入 N、输出 N、缓存读取 N（输入侧合计 2N，决定是否过 272K 阈值）。
	cases := []quoteAbsoluteCase{
		// ---- DeepSeek 高峰，OpenAI 分组（unified，叠加峰时 ×2）----
		// 1M：(0.15+0.60+0.003)×1 ×2 = 0.753×2 = 1.506
		{name: "deepseek_peak_openai_1M", model: "deepseek-v4-flash", group: quoteTestGroupOn(PlatformOpenAI, 1), at: quoteTestAtPeak, tokens: quoteUsageEven(1_000_000), want: 1.506, path: QuotePricingPathUnified},
		// 300K：0.753×0.3×2 = 0.4518
		{name: "deepseek_peak_openai_300K", model: "deepseek-v4-flash", group: quoteTestGroupOn(PlatformOpenAI, 1), at: quoteTestAtPeak, tokens: quoteUsageEven(300_000), want: 0.4518, path: QuotePricingPathUnified},
		// 低谷对照：1M = 0.753
		{name: "deepseek_low_openai_1M", model: "deepseek-v4-flash", group: quoteTestGroupOn(PlatformOpenAI, 1), at: quoteTestAtLow, tokens: quoteUsageEven(1_000_000), want: 0.753, path: QuotePricingPathUnified},

		// ---- DeepSeek，Anthropic 分组（无渠道价的 DeepSeek 走 unified，与 OpenAI 分组同样叠加峰时 ×2）----
		// 高峰 1M：0.753×2 = 1.506；300K：0.753×0.3×2 = 0.4518；低谷 1M：0.753
		{name: "deepseek_peak_anthropic_1M", model: "deepseek-v4-flash", group: quoteTestGroupOn(PlatformAnthropic, 1), at: quoteTestAtPeak, tokens: quoteUsageEven(1_000_000), want: 1.506, path: QuotePricingPathUnified},
		{name: "deepseek_peak_anthropic_300K", model: "deepseek-v4-flash", group: quoteTestGroupOn(PlatformAnthropic, 1), at: quoteTestAtPeak, tokens: quoteUsageEven(300_000), want: 0.4518, path: QuotePricingPathUnified},
		{name: "deepseek_low_anthropic_1M", model: "deepseek-v4-flash", group: quoteTestGroupOn(PlatformAnthropic, 1), at: quoteTestAtLow, tokens: quoteUsageEven(1_000_000), want: 0.753, path: QuotePricingPathUnified},

		// ---- gpt-5.5 长上下文，OpenAI 分组 ----
		// 1M：输入侧 2M > 272K → input 2.5×2=5、output 15×1.5=22.5、cache read 0.25×2=0.5 → 28.0
		{name: "gpt55_longctx_openai_1M", model: "gpt-5.5", group: quoteTestGroupOn(PlatformOpenAI, 1), tokens: quoteUsageEven(1_000_000), want: 28.0, path: QuotePricingPathUnified},
		// 300K：输入侧 600K > 272K → 28.0×0.3 = 8.4
		{name: "gpt55_longctx_openai_300K", model: "gpt-5.5", group: quoteTestGroupOn(PlatformOpenAI, 1), tokens: quoteUsageEven(300_000), want: 8.4, path: QuotePricingPathUnified},
		// 100K（阈值以下对照）：输入侧 200K ≤ 272K → (2.5+15+0.25)×0.1 = 1.775
		{name: "gpt55_standard_openai_100K", model: "gpt-5.5", group: quoteTestGroupOn(PlatformOpenAI, 1), tokens: quoteUsageEven(100_000), want: 1.775, path: QuotePricingPathUnified},
		// Anthropic 分组的 legacy 路径同样带价卡长上下文（computeTokenBreakdown applyLongCtx=true），数值相同。
		{name: "gpt55_longctx_anthropic_1M", model: "gpt-5.5", group: quoteTestGroupOn(PlatformAnthropic, 1), tokens: quoteUsageEven(1_000_000), want: 28.0, path: QuotePricingPathLegacy},

		// ---- gpt-5.5 priority，OpenAI 分组（价卡自带 priority 价，且与长上下文叠加）----
		// 1M：input 6.25×2=12.5、output 37.5×1.5=56.25、cache read 0.625×2=1.25 → 70.0
		{name: "gpt55_priority_openai_1M", model: "gpt-5.5", group: quoteTestGroupOn(PlatformOpenAI, 1), tier: "priority", tokens: quoteUsageEven(1_000_000), want: 70.0, path: QuotePricingPathUnified},
		// 300K：70.0×0.3 = 21.0
		{name: "gpt55_priority_openai_300K", model: "gpt-5.5", group: quoteTestGroupOn(PlatformOpenAI, 1), tier: "priority", tokens: quoteUsageEven(300_000), want: 21.0, path: QuotePricingPathUnified},
		// 100K：(6.25+37.5+0.625)×0.1 = 4.4375
		{name: "gpt55_priority_openai_100K", model: "gpt-5.5", group: quoteTestGroupOn(PlatformOpenAI, 1), tier: "priority", tokens: quoteUsageEven(100_000), want: 4.4375, path: QuotePricingPathUnified},
		// 同样请求 priority，Anthropic 分组 legacy 路径忽略档位，按标准价：1M = 28.0、100K = 1.775
		{name: "gpt55_priority_anthropic_ignored_1M", model: "gpt-5.5", group: quoteTestGroupOn(PlatformAnthropic, 1), tier: "priority", tokens: quoteUsageEven(1_000_000), want: 28.0, path: QuotePricingPathLegacy},
		{name: "gpt55_priority_anthropic_ignored_100K", model: "gpt-5.5", group: quoteTestGroupOn(PlatformAnthropic, 1), tier: "priority", tokens: quoteUsageEven(100_000), want: 1.775, path: QuotePricingPathLegacy},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog := flashJunkCatalog
			if tc.model != "deepseek-v4-flash" {
				catalog = nil // gpt-5.5 只用代码里的兜底价
			}
			f := newQuoteTestFixture(catalog, nil, []*Group{tc.group}, nil)
			quote, err := f.quoter.Quote(context.Background(), QuoteRequest{
				Model: tc.model, GroupID: quoteTestGroupID, ServiceTier: tc.tier, At: tc.at,
			})
			require.NoError(t, err)
			require.Equal(t, tc.path, quote.PricingPath)
			got, err := quote.Cost(context.Background(), QuoteUsage{Tokens: tc.tokens})
			require.NoError(t, err)
			require.InDelta(t, tc.want, got.ActualCost, 1e-9)
		})
	}
}

// Gemini 分组走 CalculateCostWithLongContext（200K 阈值，超出部分的输入/缓存读取 ×2，输出不加价）。
func TestPriceQuoter_GeminiGatewayLongContextAbsoluteCost(t *testing.T) {
	// 价格来源：测试目录里的 gemini-quote-test：input $1/MTok、output $4/MTok、cache read $0.25/MTok。
	catalog := map[string]*LiteLLMModelPricing{
		"gemini-quote-test": {InputCostPerToken: 1e-6, OutputCostPerToken: 4e-6, CacheReadInputTokenCost: 0.25e-6},
	}
	f := newQuoteTestFixture(catalog, nil, []*Group{quoteTestGroupOn(PlatformGemini, 1)}, nil)
	quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gemini-quote-test", GroupID: quoteTestGroupID})
	require.NoError(t, err)
	require.Equal(t, QuotePricingPathLegacy, quote.PricingPath)
	require.NotNil(t, quote.GatewayLongContext)
	require.Equal(t, 200000, quote.GatewayLongContext.ThresholdTokens)
	require.Equal(t, 2.0, quote.GatewayLongContext.ExtraMultiplier)

	tests := []struct {
		name   string
		tokens UsageTokens
		want   float64
	}{
		// 1M 输入 + 1M 输出：范围内输入 200K → 0.2；范围外输入 800K ×2 → 1.6；输出 1M → 4.0；合计 5.8
		{"1M", UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 5.8},
		// 300K 输入 + 300K 输出：范围内 200K → 0.2；范围外 100K ×2 → 0.2；输出 300K → 1.2；合计 1.6
		{"300K", UsageTokens{InputTokens: 300_000, OutputTokens: 300_000}, 1.6},
		// 输入 300K + 缓存读取 100K + 输出 1000：总输入侧 400K；缓存 100K 与输入 100K 在范围内，
		// 其余输入 200K 在范围外：0.1 + 0.025 + 0.004 + 200K×1e-6×2(0.4) = 0.529
		{"300K_with_cache_read", UsageTokens{InputTokens: 300_000, CacheReadTokens: 100_000, OutputTokens: 1000}, 0.529},
		// 阈值以内不加价：100K 输入 + 100K 输出 = 0.1 + 0.4 = 0.5
		{"100K_below_threshold", UsageTokens{InputTokens: 100_000, OutputTokens: 100_000}, 0.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := quote.Cost(context.Background(), QuoteUsage{Tokens: tc.tokens})
			require.NoError(t, err)
			require.InDelta(t, tc.want, got.ActualCost, 1e-9)

			// 参照侧：网关实际调用的函数。
			want, err := f.billing.CalculateCostWithLongContext("gemini-quote-test", tc.tokens, 1, 200000, 2.0)
			require.NoError(t, err)
			require.Equal(t, *want, *got)
		})
	}

	// 非 Gemini 分组（Anthropic）同一个模型不加价：300K 输入 + 300K 输出 = 0.3 + 1.2 = 1.5。
	fa := newQuoteTestFixture(catalog, nil, []*Group{quoteTestGroupOn(PlatformAnthropic, 1)}, nil)
	qa, err := fa.quoter.Quote(context.Background(), QuoteRequest{Model: "gemini-quote-test", GroupID: quoteTestGroupID})
	require.NoError(t, err)
	require.Nil(t, qa.GatewayLongContext)
	got, err := qa.Cost(context.Background(), QuoteUsage{Tokens: UsageTokens{InputTokens: 300_000, OutputTokens: 300_000}})
	require.NoError(t, err)
	require.InDelta(t, 1.5, got.ActualCost, 1e-9)
}

// 参照侧用 CalculateCost（Anthropic 网关对非 DeepSeek 模型、无渠道价的实际路径）：Quote 的单价、策略标记、费用都要与之一致。
// DeepSeek 不在这里：它在网关里走 CalculateCostUnified，见 TestPriceQuoter_DeepSeekOnNonOpenAIGatewayMatchesUnifiedBilling。
func TestPriceQuoter_LegacyPathMatchesCalculateCost(t *testing.T) {
	rate := 1.3
	for _, model := range []string{"gpt-5.5", "glm-4.6"} {
		for _, tier := range []string{"", "priority", "flex"} {
			t.Run(model+"/"+tier, func(t *testing.T) {
				f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroupOn(PlatformAnthropic, rate)}, nil)
				quote, err := f.quoter.Quote(context.Background(), QuoteRequest{
					Model: model, GroupID: quoteTestGroupID, ServiceTier: tier, At: quoteTestAtPeak,
				})
				require.NoError(t, err)
				require.Equal(t, QuotePricingPathLegacy, quote.PricingPath)
				require.False(t, quote.Policy.DeepSeekPeak, "非 DeepSeek 模型没有峰时倍率")
				if tier != "" {
					require.Equal(t, "ignored", quote.ServiceTier.Mode)
					require.Equal(t, 1.0, quote.ServiceTier.Multiplier)
				}

				for _, tokens := range []UsageTokens{
					quoteUsageEven(1_000_000),
					quoteUsageEven(300_000),
					{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200, CacheCreationTokens: 100},
				} {
					got, err := quote.Cost(context.Background(), QuoteUsage{Tokens: tokens})
					require.NoError(t, err)
					want, err := f.billing.CalculateCost(model, tokens, rate)
					require.NoError(t, err)
					require.Equal(t, *want, *got)
				}

				// 1 token 探针得到的单价与 CalculateCost 逐位相同。
				wantIn, err := f.billing.CalculateCost(model, UsageTokens{InputTokens: 1}, rate)
				require.NoError(t, err)
				require.Equal(t, wantIn.ActualCost, quote.FinalPrices.PerToken.Input)
			})
		}
	}
}

// 非 OpenAI 网关上无渠道价的 DeepSeek：gateway_service.go calculateTokenCost 走 CalculateCostUnified 并传 PricingAt
// （叠加峰时倍率，不传 ServiceTier），Gemini 分组再按原生入口走 CalculateCostWithLongContextUnified。
// Quote 的路径、策略标记、单价、费用都要与之逐位一致；参照侧自己 Resolve、自己调计费函数，不经过 Quoter。
func TestPriceQuoter_DeepSeekOnNonOpenAIGatewayMatchesUnifiedBilling(t *testing.T) {
	flashJunkCatalog := map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
	}
	// 周六北京时间 10:00（UTC 02:00）：UTC 小时落在高峰窗口，但北京时间周末全天低谷。
	saturdayPeakHours := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)
	slots := []struct {
		name     string
		at       time.Time
		wantPeak bool
	}{
		{"peak", quoteTestAtPeak, true},
		{"low", quoteTestAtLow, false},
		{"weekend", saturdayPeakHours, false},
	}
	rate := 1.3
	for _, platform := range []string{PlatformAnthropic, PlatformAntigravity, PlatformGemini} {
		for _, slot := range slots {
			t.Run(platform+"/"+slot.name, func(t *testing.T) {
				f := newQuoteTestFixture(flashJunkCatalog, nil, []*Group{quoteTestGroupOn(platform, rate)}, nil)
				quote, err := f.quoter.Quote(context.Background(), QuoteRequest{
					Model: "deepseek-v4-flash", GroupID: quoteTestGroupID, ServiceTier: "priority", At: slot.at,
				})
				require.NoError(t, err)
				require.Equal(t, QuotePricingPathUnified, quote.PricingPath)
				require.Equal(t, slot.wantPeak, quote.Policy.DeepSeekPeak)
				require.Equal(t, "ignored", quote.ServiceTier.Mode, "非 OpenAI 网关不按 service tier 计费")
				if platform == PlatformGemini {
					require.NotNil(t, quote.GatewayLongContext)
				} else {
					require.Nil(t, quote.GatewayLongContext)
				}

				gid := quoteTestGroupID
				reference := func(tokens UsageTokens) *CostBreakdown {
					input := CostInput{
						Ctx: context.Background(), Model: "deepseek-v4-flash", GroupID: &gid, Tokens: tokens,
						RequestCount: 1, RateMultiplier: rate, PricingAt: slot.at, Resolver: f.resolver,
					}
					var cost *CostBreakdown
					var refErr error
					if platform == PlatformGemini {
						cost, refErr = f.billing.CalculateCostWithLongContextUnified(input, 200000, 2.0)
					} else {
						cost, refErr = f.billing.CalculateCostUnified(input)
					}
					require.NoError(t, refErr)
					return cost
				}
				for _, tokens := range []UsageTokens{
					quoteUsageEven(1_000_000),
					quoteUsageEven(300_000),
					quoteUsageEven(50_000),
					{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200, CacheCreationTokens: 100},
				} {
					got, err := quote.Cost(context.Background(), QuoteUsage{Tokens: tokens})
					require.NoError(t, err)
					require.Equal(t, *reference(tokens), *got)
				}

				// 1 token 探针得到的单价与计费函数逐位相同。
				require.Equal(t, reference(UsageTokens{InputTokens: 1}).ActualCost, quote.FinalPrices.PerToken.Input)
			})
		}
	}

	// 手算绝对值（官方 flash 低谷价：input $0.15 / output $0.60 / cache read $0.003 每 MTok，目录里的旧价被覆盖），倍率 1。
	// Anthropic 分组 100K：0.753×0.1 = 0.0753；高峰 ×2 = 0.1506。
	// Gemini 分组 300K 输入 + 300K 输出：范围内输入 200K 0.03、范围外输入 100K ×2 0.03、输出 0.18，合计 0.24；高峰 ×2 = 0.48。
	absolute := []struct {
		platform string
		at       time.Time
		tokens   UsageTokens
		want     float64
	}{
		{PlatformAnthropic, quoteTestAtLow, quoteUsageEven(100_000), 0.0753},
		{PlatformAnthropic, quoteTestAtPeak, quoteUsageEven(100_000), 0.1506},
		{PlatformGemini, quoteTestAtLow, UsageTokens{InputTokens: 300_000, OutputTokens: 300_000}, 0.24},
		{PlatformGemini, quoteTestAtPeak, UsageTokens{InputTokens: 300_000, OutputTokens: 300_000}, 0.48},
	}
	for _, tc := range absolute {
		f := newQuoteTestFixture(flashJunkCatalog, nil, []*Group{quoteTestGroupOn(tc.platform, 1)}, nil)
		quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "deepseek-v4-flash", GroupID: quoteTestGroupID, At: tc.at})
		require.NoError(t, err)
		got, err := quote.Cost(context.Background(), QuoteUsage{Tokens: tc.tokens})
		require.NoError(t, err)
		require.InDelta(t, tc.want, got.ActualCost, 1e-9, "%s %s", tc.platform, tc.at)
	}
}

// Quoter 报价与网关实际入口 GatewayService.calculateRecordUsageCost 直接对比：同一份 fixture（同一个
// BillingService / ModelPricingResolver）、同样的用量、倍率与计费时点，两边 require.Equal 整个费用明细。
// 上面的用例参照的是测试里手写的计费入参，网关分支以后改了入参（比如开始传 ServiceTier）它们抓不到，
// 这个用例会抓到。覆盖 DeepSeek（无渠道价，叠加峰时倍率）与非 DeepSeek（原有 legacy 分支）。
func TestPriceQuoter_CostMatchesGatewayCalculateRecordUsageCost(t *testing.T) {
	// 周六北京时间 10:00（UTC 02:00）：UTC 小时落在高峰窗口，但北京时间周末全天低谷。
	saturdayPeakHours := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)
	slots := []struct {
		name string
		at   time.Time
	}{
		{"peak", quoteTestAtPeak},
		{"low", quoteTestAtLow},
		{"weekend", saturdayPeakHours},
	}
	usages := []UsageTokens{
		quoteUsageEven(1_000_000),
		quoteUsageEven(300_000),
		quoteUsageEven(50_000),
		{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200, CacheCreationTokens: 100},
	}
	rate := 1.3
	ctx := context.Background()
	for _, platform := range []string{PlatformAnthropic, PlatformAntigravity, PlatformGemini} {
		// Gemini 原生入口写死 200K 阈值、超出部分 2 倍（与 handler 一致）；其余入口没有网关级长上下文加价。
		opts := &recordUsageOpts{}
		if platform == PlatformGemini {
			opts = &recordUsageOpts{LongContextThreshold: 200000, LongContextMultiplier: 2.0}
		}
		for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "glm-4.6", "gpt-5.5"} {
			for _, slot := range slots {
				t.Run(platform+"/"+model+"/"+slot.name, func(t *testing.T) {
					f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroupOn(platform, rate)}, nil)
					gateway := &GatewayService{billingService: f.billing, resolver: f.resolver}
					gid := quoteTestGroupID
					apiKey := &APIKey{ID: 1, GroupID: &gid, Group: quoteTestGroupOn(platform, rate)}

					quote, err := f.quoter.Quote(ctx, QuoteRequest{
						Model: model, GroupID: quoteTestGroupID, ServiceTier: "priority", At: slot.at,
					})
					require.NoError(t, err)
					if platform == PlatformGemini {
						require.NotNil(t, quote.GatewayLongContext)
						require.Equal(t, opts.LongContextThreshold, quote.GatewayLongContext.ThresholdTokens)
						require.Equal(t, opts.LongContextMultiplier, quote.GatewayLongContext.ExtraMultiplier)
					} else {
						require.Nil(t, quote.GatewayLongContext)
					}

					for _, tokens := range usages {
						got, err := quote.Cost(ctx, QuoteUsage{Tokens: tokens})
						require.NoError(t, err)
						want := gateway.calculateRecordUsageCost(ctx, &ForwardResult{
							Model: model,
							Usage: ClaudeUsage{
								InputTokens:              tokens.InputTokens,
								OutputTokens:             tokens.OutputTokens,
								CacheCreationInputTokens: tokens.CacheCreationTokens,
								CacheReadInputTokens:     tokens.CacheReadTokens,
							},
						}, apiKey, model, rate, 1.0, opts, slot.at)
						require.NotNil(t, want)
						require.Equal(t, *want, *got)
					}
				})
			}
		}
	}
}

// 渠道定价命中时，任何平台都走 Unified；但只有 OpenAI 网关（openai / grok 分组）把 service tier 传进去。
// 依据：openai_gateway_service.go calculateOpenAIRecordUsageTokenCost 传 ServiceTier；
// gateway_service.go calculateTokenCost 的渠道价分支调用 CalculateCostUnified 时不传 ServiceTier。
func TestPriceQuoter_ChannelPricingServiceTierPerGateway(t *testing.T) {
	usage := QuoteUsage{Tokens: UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000}}
	// 渠道价：1M 输入 × $0.5 + 1M 输出 × $1 = 1.5。
	cases := []struct {
		platform string
		wantMode string
		want     float64
	}{
		// OpenAI 网关：flex ×0.5 → 0.75
		{PlatformOpenAI, "multiplier", 0.75},
		{PlatformGrok, "multiplier", 0.75},
		// 其它网关：忽略档位 → 1.5（网关实际扣费）
		{PlatformAnthropic, "ignored", 1.5},
		{PlatformGemini, "ignored", 1.5},
		{PlatformAntigravity, "ignored", 1.5},
	}
	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			channels := quoteTestChannel(ChannelModelPricing{
				Platform:    tc.platform,
				Models:      []string{"glm-4.6"},
				BillingMode: BillingModeToken,
				InputPrice:  float64Ptr(5e-7),
				OutputPrice: float64Ptr(1e-6),
			})
			f := newQuoteTestFixture(nil, channels, []*Group{quoteTestGroupOn(tc.platform, 1)}, nil)
			quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "glm-4.6", GroupID: quoteTestGroupID, ServiceTier: "flex"})
			require.NoError(t, err)
			require.Equal(t, QuoteSourceChannel, quote.Source)
			require.Equal(t, QuotePricingPathUnified, quote.PricingPath)
			require.Equal(t, tc.wantMode, quote.ServiceTier.Mode)
			// 展示单价与扣费同口径：输入价 $0.5/MTok 在忽略档位时不打折。
			wantInput := 5e-7
			if tc.wantMode == "multiplier" {
				wantInput = 2.5e-7
			}
			require.InDelta(t, wantInput, quote.Prices.PerToken.Input, 1e-15)
			got, err := quote.Cost(context.Background(), usage)
			require.NoError(t, err)
			require.InDelta(t, tc.want, got.ActualCost, 1e-9)
		})
	}
}

// Grok 分组由 OpenAI 网关计费（server/routes/gateway.go isOpenAICompatibleGatewayPlatform），
// 与 OpenAI 分组一样走 Unified 并按 service tier 计价；没有渠道价时也不会掉进 legacy 路径。
func TestPriceQuoter_GrokGroupBilledByOpenAIGateway(t *testing.T) {
	f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroupOn(PlatformGrok, 1)}, nil)
	quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-5.5", GroupID: quoteTestGroupID, ServiceTier: "priority"})
	require.NoError(t, err)
	require.Equal(t, QuotePricingPathUnified, quote.PricingPath)
	require.Nil(t, quote.GatewayLongContext)
	require.Equal(t, "priority_card", quote.ServiceTier.Mode)
	// gpt-5.5 priority 100K：(6.25+37.5+0.625)×0.1 = 4.4375（与 OpenAI 分组相同，价格来源见 TestPriceQuoter_AbsoluteCosts）
	got, err := quote.Cost(context.Background(), QuoteUsage{Tokens: quoteUsageEven(100_000)})
	require.NoError(t, err)
	require.InDelta(t, 4.4375, got.ActualCost, 1e-9)
}

func TestPriceQuoter_DeepSeekOfficialCardFlagExcludesChannelPricing(t *testing.T) {
	channels := quoteTestChannel(ChannelModelPricing{
		Platform:    PlatformOpenAI,
		Models:      []string{"deepseek-v4-flash"},
		BillingMode: BillingModeToken,
		InputPrice:  float64Ptr(5e-7),
		OutputPrice: float64Ptr(1e-6),
	})
	f := newQuoteTestFixture(nil, channels, []*Group{quoteTestGroupOn(PlatformOpenAI, 1)}, nil)
	quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "deepseek-v4-flash", GroupID: quoteTestGroupID, At: quoteTestAtPeak})
	require.NoError(t, err)
	require.Equal(t, QuoteSourceChannel, quote.Source)
	require.False(t, quote.Policy.DeepSeekOfficialCard)
	require.False(t, quote.Policy.DeepSeekPeak)
}

// 渠道图片请求。依据：
//   - 渠道是 token 模式：两个网关都按普通 token 请求计费（token 倍率、完整 token、不看图片倍率与张数）。
//     OpenAI：openai_gateway_service.go calculateOpenAIRecordUsageCost（resolved != nil 时不 return，落到 token 循环）；
//     其它：gateway_service.go calculateRecordUsageCost（Mode == Token 走 calculateTokenCost）。
//   - 渠道是按次/图片模式：Unified，RequestCount=张数 × 尺寸档 × 图片倍率；OpenAI 不传 token，其它网关只传 input/output/image output。
func TestPriceQuoter_ChannelImageRequestPathsPerGateway(t *testing.T) {
	imageCatalog := map[string]*LiteLLMModelPricing{
		"quote-test-image": {Mode: "image_generation", OutputCostPerImage: 0.2},
	}
	tokenChannel := func(platform string) []Channel {
		return quoteTestChannel(ChannelModelPricing{
			Platform:       platform,
			Models:         []string{"quote-test-image"},
			BillingMode:    BillingModeToken,
			InputPrice:     float64Ptr(1e-6),
			OutputPrice:    float64Ptr(2e-6),
			CacheReadPrice: float64Ptr(0.5e-6),
		})
	}
	perRequestChannel := func(platform string) []Channel {
		return quoteTestChannel(ChannelModelPricing{
			Platform:        platform,
			Models:          []string{"quote-test-image"},
			BillingMode:     BillingModeImage,
			PerRequestPrice: float64Ptr(0.1),
			Intervals: []PricingInterval{
				{TierLabel: "1K", PerRequestPrice: float64Ptr(0.1)},
				{TierLabel: "2K", PerRequestPrice: float64Ptr(0.25)},
			},
		})
	}
	usage := QuoteUsage{
		Tokens:     UsageTokens{InputTokens: 1000, OutputTokens: 2000, CacheReadTokens: 500},
		ImageCount: 2,
		ImageSize:  "2K",
	}
	// 图片倍率独立于 token 倍率：token 倍率 1.5，图片倍率 0.3，用来区分两条路径用的是哪个倍率。
	group := func(platform string) *Group {
		g := quoteTestGroupOn(platform, 1.5)
		g.ImageRateIndependent = true
		g.ImageRateMultiplier = 0.3
		return g
	}

	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformAnthropic} {
		t.Run(platform+"/token_mode_channel_bills_as_tokens", func(t *testing.T) {
			f := newQuoteTestFixture(imageCatalog, tokenChannel(platform), []*Group{group(platform)}, nil)
			quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "quote-test-image", GroupID: quoteTestGroupID})
			require.NoError(t, err)
			require.Nil(t, quote.ImageRequest, "渠道价生效时不展示 CalculateImageCost 的分档价")
			got, err := quote.Cost(context.Background(), usage)
			require.NoError(t, err)
			// 手算：1000×1e-6 + 2000×2e-6 + 500×0.5e-6 = 0.001 + 0.004 + 0.00025 = 0.00525；× token 倍率 1.5 = 0.007875
			require.InDelta(t, 0.007875, got.ActualCost, 1e-12)
		})

		t.Run(platform+"/per_request_channel_bills_by_image_count", func(t *testing.T) {
			f := newQuoteTestFixture(imageCatalog, perRequestChannel(platform), []*Group{group(platform)}, nil)
			quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "quote-test-image", GroupID: quoteTestGroupID})
			require.NoError(t, err)
			got, err := quote.Cost(context.Background(), usage)
			require.NoError(t, err)
			// 手算：2K 档 $0.25 × 2 张 × 图片倍率 0.3 = 0.15；token 不计费
			require.InDelta(t, 0.15, got.ActualCost, 1e-12)
		})
	}

	t.Run("no_channel_uses_calculate_image_cost", func(t *testing.T) {
		price2K := 0.31
		g := group(PlatformOpenAI)
		g.ImagePrice2K = &price2K
		f := newQuoteTestFixture(imageCatalog, nil, []*Group{g}, nil)
		quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "quote-test-image", GroupID: quoteTestGroupID})
		require.NoError(t, err)
		require.NotNil(t, quote.ImageRequest)
		got, err := quote.Cost(context.Background(), usage)
		require.NoError(t, err)
		// 手算：分组 2K 图片价 $0.31（getImageUnitPrice 分组价优先）× 2 张 × 图片倍率 0.3 = 0.186
		require.InDelta(t, 0.186, got.ActualCost, 1e-12)
	})
}
