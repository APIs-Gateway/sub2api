//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// PriceQuoter 零偏差测试：对一组代表性的「模型 × 分组」组合，
// 把 Quoter 的结果和网关实际使用的计费函数（Resolve + CalculateCostUnified /
// CalculateImageCost + 倍率）逐位比对，误差为 0（require.Equal，不用 InDelta）。
//
// 参照侧（reference*）刻意不经过 Quoter：自己重新 Resolve、自己取倍率，
// 这样 Quoter 内部任何取价/倍率偏离都会被发现。

const (
	quoteTestGroupID = int64(777)
	quoteTestUserID  = int64(42)
)

// 周一北京时间 20:00（UTC 12:00）：DeepSeek 低谷；周一北京时间 10:00（UTC 02:00）：高峰。
// 都在 pro→Flash 切换点（2026-09-14）之后。
var (
	quoteTestAtLow  = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	quoteTestAtPeak = time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC)
)

type quoteTestGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *quoteTestGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	if g, ok := r.groups[id]; ok {
		cp := *g
		return &cp, nil
	}
	return nil, nil
}

type quoteTestFixture struct {
	billing  *BillingService
	resolver *ModelPricingResolver
	quoter   *PriceQuoter
}

// newQuoteTestFixture 按生产装配方式拼出 BillingService / ModelPricingResolver / PriceQuoter。
// catalog 为 nil 表示没有动态价格目录（只有 fallbackPrices）。
func newQuoteTestFixture(catalog map[string]*LiteLLMModelPricing, channels []Channel, groups []*Group, userRate *float64) *quoteTestFixture {
	var pricingSvc *PricingService
	if catalog != nil {
		pricingSvc = &PricingService{pricingData: catalog}
	}
	billing := NewBillingService(&config.Config{}, pricingSvc)

	platforms := map[int64]string{}
	groupMap := map[int64]*Group{}
	for _, g := range groups {
		platforms[g.ID] = g.Platform
		groupMap[g.ID] = g
	}
	cs := &ChannelService{}
	cs.cache.Store(populateChannelCache(channels, platforms))
	resolver := NewModelPricingResolver(cs, billing)

	quoter := &PriceQuoter{
		resolver: resolver,
		billing:  billing,
		groups:   &quoteTestGroupRepo{groups: groupMap},
		rates: newUserGroupRateResolver(
			&userGroupRateResolverRepoStub{rate: userRate}, nil, time.Second, nil, "service.price_quoter_test"),
	}
	return &quoteTestFixture{billing: billing, resolver: resolver, quoter: quoter}
}

func quoteTestGroup(rate float64) *Group {
	return &Group{ID: quoteTestGroupID, Platform: PlatformOpenAI, RateMultiplier: rate}
}

func quoteTestChannel(pricings ...ChannelModelPricing) []Channel {
	return []Channel{{
		ID:           1,
		Name:         "quote-test-channel",
		Status:       StatusActive,
		ModelPricing: pricings,
		GroupIDs:     []int64{quoteTestGroupID},
	}}
}

// referenceTokenCost 复现网关的 token 计费调用：自己 Resolve，再 CalculateCostUnified。
func (f *quoteTestFixture) referenceTokenCost(t *testing.T, model string, rate float64, tokens UsageTokens, tier string, at time.Time) *CostBreakdown {
	t.Helper()
	gid := quoteTestGroupID
	cost, err := f.billing.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          model,
		GroupID:        &gid,
		Tokens:         tokens,
		RequestCount:   1,
		RateMultiplier: rate,
		ServiceTier:    tier,
		PricingAt:      at,
		Resolver:       f.resolver,
	})
	require.NoError(t, err)
	return cost
}

type quoteZeroDiffCase struct {
	name      string
	model     string
	catalog   map[string]*LiteLLMModelPricing
	channels  []Channel
	group     *Group
	userRate  *float64
	userID    int64
	tier      string
	at        time.Time
	wantRate  float64 // 计费应使用的倍率
	checkQuot func(t *testing.T, q *Quote)
}

func TestPriceQuoter_TokenCostZeroDiffVsBilling(t *testing.T) {
	userRate := 0.8
	flashJunkCatalog := map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
	}
	litellmCatalog := map[string]*LiteLLMModelPricing{
		"quote-test-model": {
			Mode:                        "chat",
			InputCostPerToken:           2e-6,
			OutputCostPerToken:          8e-6,
			CacheCreationInputTokenCost: 2.5e-6,
			CacheReadInputTokenCost:     0.2e-6,
		},
	}
	emptyCatalog := map[string]*LiteLLMModelPricing{}

	cases := []quoteZeroDiffCase{
		{
			name:  "channel_exact",
			model: "gpt-5.5",
			channels: quoteTestChannel(
				tokenPricingForModels([]string{"gpt-5.5"}, 0.7)),
			group:    quoteTestGroup(1.5),
			wantRate: 1.5,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, QuoteSourceChannel, q.Source)
				require.Equal(t, []string{"input", "output", "cache_write", "cache_read"}, q.ChannelOverrides)
				require.InDelta(t, 0.7e-6, q.Prices.PerToken.Input, 1e-15)
			},
		},
		{
			name:  "channel_wildcard",
			model: "gpt-5.5",
			channels: quoteTestChannel(
				tokenPricingForModels([]string{"gpt-5.*"}, 0.9)),
			group:    quoteTestGroup(1),
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, QuoteSourceChannel, q.Source)
				require.InDelta(t, 0.9e-6, q.Prices.PerToken.Input, 1e-15)
			},
		},
		{
			name:  "channel_partial_override_keeps_base_for_other_fields",
			model: "gpt-5.5",
			channels: quoteTestChannel(ChannelModelPricing{
				Platform:    PlatformOpenAI,
				Models:      []string{"gpt-5.5"},
				BillingMode: BillingModeToken,
				InputPrice:  float64Ptr(0.3e-6),
			}),
			group:    quoteTestGroup(1),
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, QuoteSourceChannel, q.Source)
				require.Equal(t, QuoteSourceFallback, q.BaseSource)
				require.Equal(t, []string{"input"}, q.ChannelOverrides)
				require.Equal(t, 0.3e-6, q.Prices.PerToken.Input)
			},
		},
		{
			name:  "channel_intervals",
			model: "gpt-5.5",
			channels: quoteTestChannel(ChannelModelPricing{
				Platform:    PlatformOpenAI,
				Models:      []string{"gpt-5.5"},
				BillingMode: BillingModeToken,
				Intervals: []PricingInterval{
					{MinTokens: 0, MaxTokens: testPtrInt(200_000), InputPrice: float64Ptr(1e-6), OutputPrice: float64Ptr(4e-6)},
					{MinTokens: 200_000, InputPrice: float64Ptr(2e-6), OutputPrice: float64Ptr(8e-6)},
				},
			}),
			group:    quoteTestGroup(1.2),
			wantRate: 1.2,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, QuoteSourceChannel, q.Source)
				require.Len(t, q.Intervals, 2)
				require.Equal(t, 1e-6, q.Intervals[0].Prices.PerToken.Input)
				require.Equal(t, 2e-6, q.Intervals[1].Prices.PerToken.Input)
				require.Nil(t, q.LongContext, "区间定价自含上下文分层，不再叠加长上下文")
			},
		},
		{
			name:     "litellm_only",
			model:    "quote-test-model",
			catalog:  litellmCatalog,
			group:    quoteTestGroup(1),
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, QuoteSourceLiteLLM, q.Source)
				require.Equal(t, 2e-6, q.Prices.PerToken.Input)
				require.InDelta(t, 2.0, q.Prices.PerMTok.Input, 1e-9)
			},
		},
		{
			name:     "fallback_only_glm",
			model:    "glm-4.6",
			catalog:  emptyCatalog,
			group:    quoteTestGroup(1),
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.True(t, q.Priced)
				require.Equal(t, QuoteSourceFallback, q.Source)
				require.InDelta(t, 0.6e-6, q.Prices.PerToken.Input, 1e-15)
			},
		},
		{
			name:     "fallback_only_without_catalog",
			model:    "glm-4.6",
			group:    quoteTestGroup(1),
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, QuoteSourceFallback, q.Source)
			},
		},
		{
			name:     "deepseek_low",
			model:    "deepseek-v4-flash",
			catalog:  flashJunkCatalog,
			group:    quoteTestGroup(1),
			at:       quoteTestAtLow,
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, QuoteSourceCardPolicy, q.Source)
				require.True(t, q.Policy.DeepSeekOfficialCard)
				require.False(t, q.Policy.DeepSeekPeak)
				require.InDelta(t, 1.5e-7, q.Prices.PerToken.Input, 1e-15, "目录里的 1e-6 被官方价卡覆盖")
			},
		},
		{
			name:     "deepseek_peak",
			model:    "deepseek-v4-flash",
			catalog:  flashJunkCatalog,
			group:    quoteTestGroup(1),
			at:       quoteTestAtPeak,
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.True(t, q.Policy.DeepSeekPeak)
				require.Equal(t, 2.0, q.Policy.DeepSeekPeakMultiplier)
				require.InDelta(t, 1.5e-7*2, q.Prices.PerToken.Input, 1e-15)
			},
		},
		{
			name:  "deepseek_peak_channel_pricing_not_stacked",
			model: "deepseek-v4-flash",
			channels: quoteTestChannel(ChannelModelPricing{
				Platform:    PlatformOpenAI,
				Models:      []string{"deepseek-v4-flash"},
				BillingMode: BillingModeToken,
				InputPrice:  float64Ptr(5e-7),
				OutputPrice: float64Ptr(1e-6),
			}),
			catalog:  flashJunkCatalog,
			group:    quoteTestGroup(1),
			at:       quoteTestAtPeak,
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, QuoteSourceChannel, q.Source)
				require.False(t, q.Policy.DeepSeekPeak, "渠道自定义价不叠加峰时倍率")
				require.Equal(t, 5e-7, q.Prices.PerToken.Input)
			},
		},
		{
			name:     "deepseek_pro_after_flash_switch",
			model:    "deepseek-v4-pro",
			catalog:  map[string]*LiteLLMModelPricing{"deepseek-v4-pro": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6}},
			group:    quoteTestGroup(1),
			at:       quoteTestAtLow,
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.True(t, q.Policy.DeepSeekProBilledAsFlash)
			},
		},
		{
			name:     "long_context_gpt55",
			model:    "gpt-5.5",
			group:    quoteTestGroup(1),
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.NotNil(t, q.LongContext)
				require.Equal(t, 272000, q.LongContext.ThresholdTokens)
				require.Equal(t, "gpt-5.4-5.5", q.Policy.LongContextPolicy)
			},
		},
		{
			name:     "service_tier_priority",
			model:    "gpt-5.5",
			group:    quoteTestGroup(1),
			tier:     "priority",
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, "priority", q.ServiceTier.Requested)
				require.Equal(t, "priority_card", q.ServiceTier.Mode)
				// gpt-5.5 兜底价：priority 输入价 = 标准 2.5e-6 × 2.5（newOpenAIGPT55FallbackPricing）。
				require.InDelta(t, 6.25e-6, q.Prices.PerToken.Input, 1e-15)
			},
		},
		{
			name:     "service_tier_fast_alias_normalized_to_priority",
			model:    "gpt-5.5",
			group:    quoteTestGroup(1),
			tier:     "fast",
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, "priority", q.ServiceTier.Requested)
			},
		},
		{
			name:     "service_tier_flex",
			model:    "gpt-5.5",
			group:    quoteTestGroup(1),
			tier:     "flex",
			wantRate: 1,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, "flex", q.ServiceTier.Requested)
				require.Equal(t, "multiplier", q.ServiceTier.Mode)
				require.Equal(t, 0.5, q.ServiceTier.Multiplier)
			},
		},
		{
			name:     "user_specific_rate_replaces_group_rate",
			model:    "glm-4.6",
			group:    quoteTestGroup(1.5),
			userRate: &userRate,
			userID:   quoteTestUserID,
			wantRate: userRate,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Equal(t, 1.5, q.GroupMultiplier)
				require.NotNil(t, q.UserMultiplier)
				require.Equal(t, userRate, *q.UserMultiplier)
				require.Equal(t, userRate, q.EffectiveMultiplier)
			},
		},
		{
			name:     "user_without_override_uses_group_rate",
			model:    "glm-4.6",
			group:    quoteTestGroup(1.5),
			userID:   quoteTestUserID,
			wantRate: 1.5,
			checkQuot: func(t *testing.T, q *Quote) {
				require.Nil(t, q.UserMultiplier)
				require.Equal(t, 1.5, q.EffectiveMultiplier)
			},
		},
	}

	usages := map[string]UsageTokens{
		"small": {InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200, CacheCreationTokens: 100},
		"1M":    {InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheCreationTokens: 1_000_000},
		"long":  {InputTokens: 300_000, OutputTokens: 1000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuoteTestFixture(tc.catalog, tc.channels, []*Group{tc.group}, tc.userRate)
			quote, err := f.quoter.Quote(context.Background(), QuoteRequest{
				Model:       tc.model,
				GroupID:     quoteTestGroupID,
				UserID:      tc.userID,
				ServiceTier: tc.tier,
				At:          tc.at,
			})
			require.NoError(t, err)
			require.True(t, quote.Access.OK)
			require.True(t, quote.Priced)
			require.Equal(t, tc.wantRate, quote.EffectiveMultiplier)
			if tc.checkQuot != nil {
				tc.checkQuot(t, quote)
			}

			// 1 token 探针得到的单价（FinalPrices，已乘倍率）与计费函数对 1 token 的结果逐位相同。
			// 原价（Prices）= 倍率 1 下的计费结果。
			refTier := quote.ServiceTier.Requested
			require.Equal(t, f.referenceTokenCost(t, tc.model, tc.wantRate, UsageTokens{InputTokens: 1}, refTier, quote.At).ActualCost, quote.FinalPrices.PerToken.Input)
			if len(quote.Intervals) > 0 {
				// 区间定价按「输入+缓存」判定档位，只有 1 个输出 token 时上下文为 0，不落入任何区间；
				// 所以输出价用「1 输入 + 1 输出」减「1 输入」得到，与报价探针的参考上下文（1）一致。
				withOut := f.referenceTokenCost(t, tc.model, tc.wantRate, UsageTokens{InputTokens: 1, OutputTokens: 1}, refTier, quote.At).ActualCost
				inOnly := f.referenceTokenCost(t, tc.model, tc.wantRate, UsageTokens{InputTokens: 1}, refTier, quote.At).ActualCost
				require.InDelta(t, withOut-inOnly, quote.FinalPrices.PerToken.Output, 1e-15)
			} else {
				require.Equal(t, f.referenceTokenCost(t, tc.model, tc.wantRate, UsageTokens{OutputTokens: 1}, refTier, quote.At).ActualCost, quote.FinalPrices.PerToken.Output)
			}
			require.Equal(t, f.referenceTokenCost(t, tc.model, tc.wantRate, UsageTokens{CacheReadTokens: 1}, refTier, quote.At).ActualCost, quote.FinalPrices.PerToken.CacheRead)
			require.Equal(t, f.referenceTokenCost(t, tc.model, tc.wantRate, UsageTokens{CacheCreationTokens: 1}, refTier, quote.At).ActualCost, quote.FinalPrices.PerToken.CacheWrite)
			require.Equal(t, f.referenceTokenCost(t, tc.model, 1, UsageTokens{InputTokens: 1}, refTier, quote.At).ActualCost, quote.Prices.PerToken.Input)

			for usageName, usage := range usages {
				got, err := quote.Cost(context.Background(), QuoteUsage{Tokens: usage})
				require.NoError(t, err, usageName)
				want := f.referenceTokenCost(t, tc.model, tc.wantRate, usage, refTier, quote.At)
				require.Equal(t, *want, *got, "usage=%s", usageName)
			}
		})
	}
}

func TestPriceQuoter_PerRequestAndImageCostZeroDiffVsBilling(t *testing.T) {
	imageCatalog := map[string]*LiteLLMModelPricing{
		"quote-test-image": {Mode: "image_generation", OutputCostPerImage: 0.2},
	}
	price2K := 0.31

	t.Run("channel_per_request", func(t *testing.T) {
		f := newQuoteTestFixture(nil, quoteTestChannel(ChannelModelPricing{
			Platform:        PlatformOpenAI,
			Models:          []string{"gpt-5.5"},
			BillingMode:     BillingModePerRequest,
			PerRequestPrice: float64Ptr(0.05),
		}), []*Group{quoteTestGroup(1.3)}, nil)
		quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-5.5", GroupID: quoteTestGroupID})
		require.NoError(t, err)
		require.True(t, quote.Priced)
		require.Equal(t, QuoteSourceChannel, quote.Source)
		require.Equal(t, string(BillingModePerRequest), quote.BillingMode)
		require.NotNil(t, quote.PerRequest)
		require.Equal(t, 0.05, quote.PerRequest.DefaultPrice)
		require.Nil(t, quote.Prices)

		got, err := quote.Cost(context.Background(), QuoteUsage{Tokens: UsageTokens{InputTokens: 1000}})
		require.NoError(t, err)
		want := f.referenceTokenCost(t, "gpt-5.5", 1.3, UsageTokens{InputTokens: 1000}, "", time.Time{})
		require.Equal(t, *want, *got)
		require.InDelta(t, 0.05*1.3, got.ActualCost, 1e-12)
	})

	t.Run("channel_image_mode_tiers", func(t *testing.T) {
		g := quoteTestGroup(1.3)
		f := newQuoteTestFixture(nil, quoteTestChannel(ChannelModelPricing{
			Platform:        PlatformOpenAI,
			Models:          []string{"gpt-image-2"},
			BillingMode:     BillingModeImage,
			PerRequestPrice: float64Ptr(0.1),
			Intervals: []PricingInterval{
				{TierLabel: "1K", PerRequestPrice: float64Ptr(0.1)},
				{TierLabel: "2K", PerRequestPrice: float64Ptr(0.25)},
			},
		}), []*Group{g}, nil)
		quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-image-2", GroupID: quoteTestGroupID})
		require.NoError(t, err)
		require.True(t, quote.Priced)
		require.Len(t, quote.PerRequest.Tiers, 2)

		got, err := quote.Cost(context.Background(), QuoteUsage{ImageCount: 3, ImageSize: "2K"})
		require.NoError(t, err)

		gid := quoteTestGroupID
		want, err := f.billing.CalculateCostUnified(CostInput{
			Ctx:            context.Background(),
			Model:          "gpt-image-2",
			GroupID:        &gid,
			RequestCount:   3,
			SizeTier:       NormalizeImageBillingTierOrDefault("2K"),
			RateMultiplier: resolveImageRateMultiplier(&APIKey{Group: g}, g.RateMultiplier),
			Resolver:       f.resolver,
		})
		require.NoError(t, err)
		require.Equal(t, *want, *got)
		require.InDelta(t, 0.25*3*1.3, got.ActualCost, 1e-12)
	})

	t.Run("no_channel_image_uses_calculate_image_cost", func(t *testing.T) {
		g := quoteTestGroup(1.3)
		g.ImagePrice2K = &price2K
		f := newQuoteTestFixture(imageCatalog, nil, []*Group{g}, nil)
		quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "quote-test-image", GroupID: quoteTestGroupID})
		require.NoError(t, err)
		require.NotNil(t, quote.ImageRequest)
		require.Len(t, quote.ImageRequest.Tiers, 3)
		require.Equal(t, "litellm", quote.ImageRequest.Tiers[0].Source)
		require.Equal(t, "group_config", quote.ImageRequest.Tiers[1].Source)
		require.Equal(t, price2K, quote.ImageRequest.Tiers[1].Price)

		for _, size := range []string{"1K", "2K", "4K", ""} {
			got, err := quote.Cost(context.Background(), QuoteUsage{ImageCount: 2, ImageSize: size})
			require.NoError(t, err)
			want := f.billing.CalculateImageCost("quote-test-image", size, 2,
				&ImagePriceConfig{Price2K: &price2K}, resolveImageRateMultiplier(&APIKey{Group: g}, g.RateMultiplier))
			require.Equal(t, *want, *got, "size=%q", size)
		}
	})

	t.Run("image_rate_independent_group", func(t *testing.T) {
		g := quoteTestGroup(1.5)
		g.ImageRateIndependent = true
		g.ImageRateMultiplier = 0.3
		userRate := 0.9
		f := newQuoteTestFixture(imageCatalog, nil, []*Group{g}, &userRate)
		quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "quote-test-image", GroupID: quoteTestGroupID, UserID: quoteTestUserID})
		require.NoError(t, err)
		require.Equal(t, 0.9, quote.EffectiveMultiplier, "token 倍率仍用用户专属倍率")
		require.Equal(t, 0.3, quote.ImageMultiplier, "独立图片倍率不受用户倍率影响")

		got, err := quote.Cost(context.Background(), QuoteUsage{ImageCount: 4, ImageSize: "1K"})
		require.NoError(t, err)
		want := f.billing.CalculateImageCost("quote-test-image", "1K", 4, &ImagePriceConfig{}, 0.3)
		require.Equal(t, *want, *got)
	})

	t.Run("image_follows_effective_rate_when_not_independent", func(t *testing.T) {
		g := quoteTestGroup(1.5)
		userRate := 0.9
		f := newQuoteTestFixture(imageCatalog, nil, []*Group{g}, &userRate)
		quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "quote-test-image", GroupID: quoteTestGroupID, UserID: quoteTestUserID})
		require.NoError(t, err)
		require.Equal(t, 0.9, quote.ImageMultiplier)
		got, err := quote.Cost(context.Background(), QuoteUsage{ImageCount: 1, ImageSize: "2K"})
		require.NoError(t, err)
		want := f.billing.CalculateImageCost("quote-test-image", "2K", 1, &ImagePriceConfig{}, 0.9)
		require.Equal(t, *want, *got)
	})
}

func TestPriceQuoter_ServedGroupFallbackPricing(t *testing.T) {
	home := quoteTestGroup(1)
	served := &Group{ID: 888, Platform: PlatformOpenAI, RateMultiplier: 2}
	zeroRate := &Group{ID: 889, Platform: PlatformOpenAI, RateMultiplier: 0}
	f := newQuoteTestFixture(nil, nil, []*Group{home, served, zeroRate}, nil)

	q, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "glm-4.6", GroupID: home.ID, ServedGroupID: served.ID})
	require.NoError(t, err)
	require.Equal(t, home.ID, q.GroupID)
	require.Equal(t, served.ID, q.ServedGroupID)
	require.Equal(t, 2.0, q.EffectiveMultiplier)

	// 服务组倍率 <= 0：与 RecordUsage 的 servedFromFallback 判定一致，仍按 home 组计价。
	q, err = f.quoter.Quote(context.Background(), QuoteRequest{Model: "glm-4.6", GroupID: home.ID, ServedGroupID: zeroRate.ID})
	require.NoError(t, err)
	require.Equal(t, home.ID, q.ServedGroupID)
	require.Equal(t, 1.0, q.EffectiveMultiplier)

	q, err = f.quoter.Quote(context.Background(), QuoteRequest{Model: "glm-4.6", GroupID: home.ID, ServedGroupID: home.ID})
	require.NoError(t, err)
	require.Equal(t, home.ID, q.ServedGroupID)
}

func TestPriceQuoter_UnpricedModel(t *testing.T) {
	f := newQuoteTestFixture(map[string]*LiteLLMModelPricing{}, nil, []*Group{quoteTestGroup(1)}, nil)
	q, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "totally-unknown-model-xyz", GroupID: quoteTestGroupID})
	require.NoError(t, err)
	require.False(t, q.Priced)
	require.Equal(t, QuoteSourceNone, q.Source)
	require.Nil(t, q.Prices)

	// 与计费一致：取不到价格时计费返回 ErrModelPricingUnavailable。
	_, err = q.Cost(context.Background(), QuoteUsage{Tokens: UsageTokens{InputTokens: 1}})
	require.ErrorIs(t, err, ErrModelPricingUnavailable)
}

func TestPriceQuoter_QuoteRequestValidation(t *testing.T) {
	f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1)}, nil)
	ctx := context.Background()

	_, err := f.quoter.Quote(ctx, QuoteRequest{Model: "  ", GroupID: quoteTestGroupID})
	require.ErrorIs(t, err, ErrPriceQuoteModelRequired)

	_, err = f.quoter.Quote(ctx, QuoteRequest{Model: "glm-4.6", GroupID: 0})
	require.ErrorIs(t, err, ErrPriceQuoteGroupRequired)

	_, err = f.quoter.Quote(ctx, QuoteRequest{Model: "glm-4.6", GroupID: quoteTestGroupID, ServiceTier: "turbo"})
	require.ErrorIs(t, err, ErrPriceQuoteServiceTierInvalid)

	_, err = f.quoter.Quote(ctx, QuoteRequest{Model: "glm-4.6", GroupID: 99999})
	require.ErrorIs(t, err, ErrGroupNotFound)

	var nilQuoter *PriceQuoter
	_, err = nilQuoter.Quote(ctx, QuoteRequest{Model: "glm-4.6", GroupID: quoteTestGroupID})
	require.ErrorIs(t, err, ErrPriceQuoterUnavailable)

	var nilQuote *Quote
	_, err = nilQuote.Cost(ctx, QuoteUsage{})
	require.ErrorIs(t, err, ErrPriceQuoterUnavailable)
}

func TestQuoteUSDPerMTok(t *testing.T) {
	require.Equal(t, 0.0, quoteUSDPerMTok(0))
	require.Equal(t, 3.0, quoteUSDPerMTok(3e-6))
	require.Equal(t, 0.15, quoteUSDPerMTok(1.5e-7))
	require.Equal(t, 0.003, quoteUSDPerMTok(3e-9))
}
