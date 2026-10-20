//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 用户价格页新端点的测试。核心是「零变化」对照：所有分组都在 legacy 阶段时，
// 新端点给出的每一个价格，必须与改造前页面（ChannelService.ListAvailable 的支持模型定价，
// 再由前端 buildCatalog 乘倍率）显示的价格逐位相同。对照侧（legacyPage*）刻意不经过 Quoter。

type priceCatalogTestGroupRepo struct {
	GroupRepository
	groups []Group
}

func (r *priceCatalogTestGroupRepo) ListActive(context.Context) ([]Group, error) {
	return r.groups, nil
}

func (r *priceCatalogTestGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	for i := range r.groups {
		if r.groups[i].ID == id {
			cp := r.groups[i]
			return &cp, nil
		}
	}
	return nil, nil
}

type priceCatalogFixture struct {
	svc      *UserPriceCatalogService
	cs       *ChannelService
	billing  *BillingService
	resolver *ModelPricingResolver
	quoter   *PriceQuoter
	groups   []Group
}

// newPriceCatalogFixture 按生产装配方式拼出价格页服务；渠道服务同时是页面旧口径（ListAvailable）与 Quoter 的数据源。
func newPriceCatalogFixture(pricingSvc *PricingService, channels []Channel, groups []Group, userRate *float64, catalog userPriceCatalogLister) *priceCatalogFixture {
	repo := &priceCatalogTestGroupRepo{groups: groups}
	channelRepo := &mockChannelRepository{
		listAllFn: func(context.Context) ([]Channel, error) { return channels, nil },
	}
	cs := NewChannelService(channelRepo, repo, nil, pricingSvc, nil)
	platforms := map[int64]string{}
	for _, g := range groups {
		platforms[g.ID] = g.Platform
	}
	cs.cache.Store(populateChannelCache(channels, platforms))

	billing := NewBillingService(&config.Config{}, pricingSvc)
	resolver := NewModelPricingResolver(cs, billing)
	quoter := &PriceQuoter{
		resolver: resolver,
		billing:  billing,
		groups:   repo,
		rates: newUserGroupRateResolver(
			&userGroupRateResolverRepoStub{rate: userRate}, nil, time.Second, nil, "service.user_price_catalog_test"),
	}
	svc := &UserPriceCatalogService{
		channels: cs,
		quoter:   quoter,
		catalog:  catalog,
		now:      time.Now,
		cache:    make(map[string]userPriceCatalogCacheEntry),
	}
	return &priceCatalogFixture{svc: svc, cs: cs, billing: billing, resolver: resolver, quoter: quoter, groups: groups}
}

func (f *priceCatalogFixture) build(t *testing.T, userID int64) *UserPriceCatalog {
	t.Helper()
	out, err := f.svc.Build(context.Background(), UserPriceCatalogQuery{UserID: userID, Groups: f.groups})
	require.NoError(t, err)
	return out
}

func (c *UserPriceCatalog) model(platform, name string) *UserPriceModel {
	for i := range c.Models {
		if c.Models[i].Platform == platform && c.Models[i].Name == name {
			return &c.Models[i]
		}
	}
	return nil
}

func (m *UserPriceModel) entry(groupID int64) *UserPriceEntry {
	for i := range m.Entries {
		if m.Entries[i].GroupID == groupID {
			return &m.Entries[i]
		}
	}
	return nil
}

// legacyPagePrice 是改造前页面（utils/modelCatalog.ts 的 normalizePricing）从渠道定价取出的首档价与分档价。
type legacyPagePrice struct {
	request                              bool
	input, output, cacheRead, cacheWrite *float64
	unit                                 *float64
	tiers                                []legacyPagePrice
	tierMin                              int
	tierMax                              *int
	tierLabel                            string
}

func legacyPageFirstNonNil(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

// legacyPageNormalize 与前端 normalizePricing 的取价规则一致：首档取第一个区间，缺省回退基础价；区间超过一个时给出分档。
func legacyPageNormalize(p *ChannelModelPricing) *legacyPagePrice {
	if p == nil {
		return nil
	}
	mode := p.BillingMode
	if mode == "" {
		mode = BillingModeToken
	}
	at := func(i int) *PricingInterval {
		if i < len(p.Intervals) {
			return &p.Intervals[i]
		}
		return nil
	}
	if mode == BillingModeToken {
		set := func(iv *PricingInterval) legacyPagePrice {
			out := legacyPagePrice{input: p.InputPrice, output: p.OutputPrice, cacheRead: p.CacheReadPrice, cacheWrite: p.CacheWritePrice}
			if iv != nil {
				out.input = legacyPageFirstNonNil(iv.InputPrice, p.InputPrice)
				out.output = legacyPageFirstNonNil(iv.OutputPrice, p.OutputPrice)
				out.cacheRead = legacyPageFirstNonNil(iv.CacheReadPrice, p.CacheReadPrice)
				out.cacheWrite = legacyPageFirstNonNil(iv.CacheWritePrice, p.CacheWritePrice)
			}
			return out
		}
		first := set(at(0))
		if first.input == nil && first.output == nil {
			return nil
		}
		if len(p.Intervals) > 1 {
			for i := range p.Intervals {
				tier := set(&p.Intervals[i])
				tier.tierMin, tier.tierMax = p.Intervals[i].MinTokens, p.Intervals[i].MaxTokens
				first.tiers = append(first.tiers, tier)
			}
		}
		return &first
	}
	unitOf := func(iv *PricingInterval) *float64 {
		if iv != nil {
			return legacyPageFirstNonNil(iv.PerRequestPrice, p.PerRequestPrice)
		}
		return p.PerRequestPrice
	}
	unit := unitOf(at(0))
	if unit == nil {
		return nil
	}
	first := legacyPagePrice{request: true, unit: unit}
	if len(p.Intervals) > 1 {
		for i := range p.Intervals {
			first.tiers = append(first.tiers, legacyPagePrice{
				request: true, unit: unitOf(&p.Intervals[i]),
				tierMin: p.Intervals[i].MinTokens, tierMax: p.Intervals[i].MaxTokens, tierLabel: p.Intervals[i].TierLabel,
			})
		}
	}
	return &first
}

func requireSamePrice(t *testing.T, want *float64, rate float64, official, final *float64, msg string) {
	t.Helper()
	if want == nil {
		require.Nil(t, official, msg+" official")
		require.Nil(t, final, msg+" final")
		return
	}
	require.NotNil(t, official, msg+" official")
	require.NotNil(t, final, msg+" final")
	require.Equal(t, *want, *official, msg+" official")
	require.Equal(t, *want*rate, *final, msg+" final")
}

func zeroChangeCatalog() map[string]*LiteLLMModelPricing {
	return map[string]*LiteLLMModelPricing{
		"m-empty": {
			Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 3e-6,
			CacheReadInputTokenCost: 1e-7, CacheCreationInputTokenCost: 1.25e-6,
		},
		"m-mapped": {Mode: "chat", InputCostPerToken: 2e-6, OutputCostPerToken: 6e-6},
		"gpt-5.5":  {Mode: "chat", InputCostPerToken: 2.5e-6, OutputCostPerToken: 15e-6},
		"claude-x": {Mode: "chat", InputCostPerToken: 3e-6, OutputCostPerToken: 15e-6, CacheReadInputTokenCost: 0.3e-6, CacheCreationInputTokenCost: 3.75e-6},
		"m-free":   {Mode: "chat", InputCostPerToken: 0, OutputCostPerToken: 0},
	}
}

func zeroChangeChannels() []Channel {
	openaiPricing := []ChannelModelPricing{
		{
			Platform: PlatformOpenAI, Models: []string{"m-flat"}, BillingMode: BillingModeToken,
			InputPrice: float64Ptr(1.1e-6), OutputPrice: float64Ptr(4.4e-6),
			CacheReadPrice: float64Ptr(0.11e-6), CacheWritePrice: float64Ptr(1.375e-6),
		},
		{
			Platform: PlatformOpenAI, Models: []string{"m-interval"}, BillingMode: BillingModeToken,
			Intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: testPtrInt(200_000), InputPrice: float64Ptr(1e-6), OutputPrice: float64Ptr(4e-6)},
				{MinTokens: 200_000, InputPrice: float64Ptr(2e-6), OutputPrice: float64Ptr(8e-6)},
			},
		},
		{
			Platform: PlatformOpenAI, Models: []string{"m-req"}, BillingMode: BillingModePerRequest,
			Intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: testPtrInt(100), TierLabel: "1K", PerRequestPrice: float64Ptr(0.04)},
				{MinTokens: 100, TierLabel: "2K", PerRequestPrice: float64Ptr(0.06)},
			},
		},
		{Platform: PlatformOpenAI, Models: []string{"m-req-flat"}, BillingMode: BillingModePerRequest, PerRequestPrice: float64Ptr(0.5)},
		{Platform: PlatformOpenAI, Models: []string{"m-empty"}, BillingMode: BillingModeToken},
		{
			Platform: PlatformOpenAI, Models: []string{"m-free"}, BillingMode: BillingModeToken,
			InputPrice: float64Ptr(0), OutputPrice: float64Ptr(0), CacheReadPrice: float64Ptr(0),
		},
	}
	return []Channel{
		{
			ID: 1, Name: "ch-openai", Status: StatusActive, GroupIDs: []int64{1, 2},
			ModelPricing: openaiPricing,
			ModelMapping: map[string]map[string]string{PlatformOpenAI: {
				"m-mapped": "up-m", "gpt-5.5": "gpt-5.5", "m-nopricing": "up-x",
			}},
		},
		{
			ID: 2, Name: "ch-anthropic", Status: StatusActive, GroupIDs: []int64{3},
			ModelMapping: map[string]map[string]string{PlatformAnthropic: {"claude-x": "claude-x"}},
		},
		{ID: 3, Name: "ch-disabled", Status: StatusDisabled, GroupIDs: []int64{4}, ModelMapping: map[string]map[string]string{PlatformOpenAI: {"m-hidden": "m-hidden"}}},
	}
}

func zeroChangeGroups() []Group {
	return []Group{
		{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 1.4, SubscriptionType: SubscriptionTypeStandard},
		{ID: 2, Name: "g2", Platform: PlatformOpenAI, RateMultiplier: 0.5, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true, Description: "d2"},
		{ID: 3, Name: "g3", Platform: PlatformAnthropic, RateMultiplier: 2, SubscriptionType: SubscriptionTypeStandard},
		{ID: 4, Name: "g4", Platform: PlatformOpenAI, RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard},
		{ID: 5, Name: "g5-no-channel", Platform: PlatformOpenAI, RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard},
	}
}

// 零变化：所有分组都在 legacy 时，新端点的每个价格与改造前页面逐位相同，模型与分组的清单也相同。
func TestUserPriceCatalog_LegacyGroupsMatchCurrentPage(t *testing.T) {
	groups := zeroChangeGroups()
	fx := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), groups, nil, nil)
	got := fx.build(t, 9)

	available, err := fx.cs.ListAvailable(context.Background())
	require.NoError(t, err)
	rateOf := map[int64]float64{}
	for _, g := range groups {
		rateOf[g.ID] = g.RateMultiplier
	}

	wantModels := map[string]struct{}{}
	checked := 0
	for _, ch := range available {
		if ch.Status != StatusActive {
			continue
		}
		for _, ref := range ch.Groups {
			rate := rateOf[ref.ID]
			for _, m := range ch.SupportedModels {
				if m.Platform != ref.Platform {
					continue
				}
				wantModels[m.Platform+"::"+m.Name] = struct{}{}
				model := got.model(m.Platform, m.Name)
				require.NotNil(t, model, "%s 应出现在价格页", m.Name)
				want := legacyPageNormalize(m.Pricing)
				entry := model.entry(ref.ID)
				msg := m.Name + "@" + ref.Name
				if want == nil {
					require.Nil(t, entry, msg+" 改造前页面无价，新端点也不给价")
					continue
				}
				require.NotNil(t, entry, msg)
				checked++
				require.Equal(t, rate, entry.Rate, msg+" rate")
				require.Equal(t, rate, entry.BaseRate, msg+" base rate")
				require.False(t, entry.HasCustomRate, msg)
				if want.request {
					require.Equal(t, userPriceKindRequest, entry.Kind, msg)
					requireSamePrice(t, want.unit, rate, entry.Official.Unit, entry.Prices.Unit, msg+" unit")
				} else {
					require.Equal(t, userPriceKindToken, entry.Kind, msg)
					requireSamePrice(t, want.input, rate, entry.Official.Input, entry.Prices.Input, msg+" input")
					requireSamePrice(t, want.output, rate, entry.Official.Output, entry.Prices.Output, msg+" output")
					requireSamePrice(t, want.cacheRead, rate, entry.Official.CacheRead, entry.Prices.CacheRead, msg+" cacheRead")
					requireSamePrice(t, want.cacheWrite, rate, entry.Official.CacheWrite, entry.Prices.CacheWrite, msg+" cacheWrite")
				}
				require.Len(t, entry.Tiers, len(want.tiers), msg+" tiers")
				for i, wt := range want.tiers {
					tier := entry.Tiers[i]
					require.Equal(t, wt.tierMin, tier.MinTokens, msg+" tier min")
					require.Equal(t, wt.tierMax, tier.MaxTokens, msg+" tier max")
					require.Equal(t, wt.tierLabel, tier.Label, msg+" tier label")
					if want.request {
						requireSamePrice(t, wt.unit, rate, tier.Official.Unit, tier.Prices.Unit, msg+" tier unit")
					} else {
						requireSamePrice(t, wt.input, rate, tier.Official.Input, tier.Prices.Input, msg+" tier input")
						requireSamePrice(t, wt.output, rate, tier.Official.Output, tier.Prices.Output, msg+" tier output")
					}
				}
			}
		}
	}
	require.Greater(t, checked, 10, "对照样本数")

	// 清单一致：没有多出模型，也没有漏掉模型（含「暂无价格」的模型，不含停用渠道里的模型）。
	gotModels := map[string]struct{}{}
	for _, m := range got.Models {
		gotModels[m.Platform+"::"+m.Name] = struct{}{}
	}
	require.Equal(t, wantModels, gotModels)
	require.NotNil(t, got.model(PlatformOpenAI, "m-nopricing"))
	require.Empty(t, got.model(PlatformOpenAI, "m-nopricing").Entries)
	require.Nil(t, got.model(PlatformOpenAI, "m-hidden"))

	// 没有渠道的分组在改造前就不出现在价格页，legacy 阶段保持不变。
	for _, g := range got.Groups {
		require.NotEqual(t, int64(5), g.ID)
		require.NotEqual(t, int64(4), g.ID)
	}
}

// 分档价：渠道区间与长上下文（GPT-5.5 的 272K 阈值）都能给出分档，输入输出与改造前页面逐位相同。
func TestUserPriceCatalog_LongContextTiersMatchCurrentPage(t *testing.T) {
	fx := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), zeroChangeGroups(), nil, nil)
	got := fx.build(t, 9)

	entry := got.model(PlatformOpenAI, "gpt-5.5").entry(1)
	require.NotNil(t, entry)
	require.Len(t, entry.Tiers, 2)

	available, err := fx.cs.ListAvailable(context.Background())
	require.NoError(t, err)
	var page *ChannelModelPricing
	for _, ch := range available {
		if ch.Name != "ch-openai" {
			continue
		}
		for _, m := range ch.SupportedModels {
			if m.Name == "gpt-5.5" {
				page = m.Pricing
			}
		}
	}
	require.NotNil(t, page)
	require.Len(t, page.Intervals, 2)

	require.Equal(t, 0, entry.Tiers[0].MinTokens)
	require.Equal(t, *page.Intervals[0].MaxTokens, *entry.Tiers[0].MaxTokens)
	require.Equal(t, page.Intervals[1].MinTokens, entry.Tiers[1].MinTokens)
	require.Nil(t, entry.Tiers[1].MaxTokens)
	require.Equal(t, *page.Intervals[1].InputPrice, *entry.Tiers[1].Official.Input)
	require.Equal(t, *page.Intervals[1].OutputPrice, *entry.Tiers[1].Official.Output)
	require.Equal(t, *page.Intervals[1].InputPrice*1.4, *entry.Tiers[1].Prices.Input)
}

// 用户专属倍率在后端乘好：Rate 是专属倍率，BaseRate 是分组默认倍率，价格已含专属倍率。
func TestUserPriceCatalog_UserRateAppliedInBackend(t *testing.T) {
	userRate := 0.8
	fx := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), zeroChangeGroups(), &userRate, nil)
	got := fx.build(t, 9)

	entry := got.model(PlatformOpenAI, "m-flat").entry(1)
	require.NotNil(t, entry)
	require.Equal(t, 0.8, entry.Rate)
	require.Equal(t, 1.4, entry.BaseRate)
	require.True(t, entry.HasCustomRate)
	// 期望值用变量相乘，避免 Go 常量表达式按任意精度先算再取整，与运行时 float64 乘法差一位。
	price, rate := 1.1e-6, 0.8
	require.Equal(t, price, *entry.Official.Input)
	require.Equal(t, price*rate, *entry.Prices.Input)

	// 匿名查询（用户 ID 为 0）不查专属倍率。
	anon := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), zeroChangeGroups(), &userRate, nil).build(t, 0)
	require.Equal(t, 1.4, anon.model(PlatformOpenAI, "m-flat").entry(1).Rate)
}

// 渠道显式填的 0 价（免费）要展示；没填的缓存项不展示；输入输出都没有价格的目录模型按免费展示。
func TestUserPriceCatalog_ExplicitZeroAndAbsentFields(t *testing.T) {
	fx := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), zeroChangeGroups(), nil, nil)
	got := fx.build(t, 9)

	free := got.model(PlatformOpenAI, "m-free").entry(1)
	require.NotNil(t, free)
	require.NotNil(t, free.Prices.Input)
	require.Equal(t, 0.0, *free.Prices.Input)
	require.NotNil(t, free.Prices.Output)
	require.NotNil(t, free.Prices.CacheRead, "渠道显式填了缓存读取价 0")
	require.Nil(t, free.Prices.CacheWrite, "没填的缓存写入价不展示")

	empty := got.model(PlatformOpenAI, "m-empty").entry(1)
	require.NotNil(t, empty)
	require.Equal(t, 1e-6, *empty.Official.Input, "全空的渠道条目回落目录价")
	require.Equal(t, 1.25e-6, *empty.Official.CacheWrite)
}

// 已知偏差 1（已消除）：只有硬编码兜底价的模型，价格页现在也显示价格，与计费一致。
func TestUserPriceCatalog_FallbackOnlyModelNowPriced(t *testing.T) {
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{1},
		ModelMapping: map[string]map[string]string{PlatformOpenAI: {"glm-4.6": "glm-4.6", "qwen3-embedding-8b": "qwen3-embedding-8b"}},
	}}
	groups := []Group{{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 2}}
	fx := newPriceCatalogFixture(&PricingService{pricingData: map[string]*LiteLLMModelPricing{}}, channels, groups, nil, nil)
	got := fx.build(t, 9)

	for _, name := range []string{"glm-4.6", "qwen3-embedding-8b"} {
		entry := got.model(PlatformOpenAI, name).entry(1)
		require.NotNil(t, entry, name)
		require.Greater(t, *entry.Official.Input, 0.0, name)
		require.Equal(t, *entry.Official.Input*2, *entry.Prices.Input, name)
	}
	// 嵌入模型没有输出价，不展示输出项。
	require.Nil(t, got.model(PlatformOpenAI, "qwen3-embedding-8b").entry(1).Prices.Output)
}

// 已知偏差 2（已消除）与 3（保留）：DeepSeek 页面价是官方价卡的标准（低谷）价，不是目录 JSON 的价，
// 也不随高峰时段跳动；高峰时段的 2 倍只在计费上体现。
func TestUserPriceCatalog_DeepSeekShowsOfficialCardOffPeak(t *testing.T) {
	original := deepseekNowFunc
	t.Cleanup(func() { deepseekNowFunc = original })

	catalog := map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
	}
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{1},
		ModelMapping: map[string]map[string]string{PlatformOpenAI: {"deepseek-v4-flash": "deepseek-v4-flash"}},
	}}
	groups := []Group{{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 1}}

	for name, at := range map[string]time.Time{"high_peak": quoteTestAtPeak, "off_peak": quoteTestAtLow} {
		t.Run(name, func(t *testing.T) {
			deepseekNowFunc = func() time.Time { return at }
			fx := newPriceCatalogFixture(&PricingService{pricingData: catalog}, channels, groups, nil, nil)
			entry := fx.build(t, 9).model(PlatformOpenAI, "deepseek-v4-flash").entry(1)
			require.NotNil(t, entry)
			require.Equal(t, deepseekFlashOffPeakInputPrice, *entry.Official.Input)
			require.Equal(t, deepseekFlashOffPeakOutputPrice, *entry.Official.Output)
		})
	}
}

func TestQuoteOffPeakReference(t *testing.T) {
	require.Equal(t, quoteTestAtLow, quoteOffPeakReference(quoteTestAtLow), "低谷时刻原样返回")
	got := quoteOffPeakReference(quoteTestAtPeak)
	require.True(t, got.After(quoteTestAtPeak))
	require.LessOrEqual(t, got.Sub(quoteTestAtPeak), 6*time.Hour)
	require.Equal(t, 1.0, deepseekPeakMultiplierAt(got))
}

// 图片模式的渠道条目用图片倍率（与计费一致）；独立图片倍率与分组倍率不同时，页面显示计费实际用的那个。
func TestUserPriceCatalog_ImageModeUsesImageMultiplier(t *testing.T) {
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{1, 2},
		ModelPricing: []ChannelModelPricing{{
			Platform: PlatformOpenAI, Models: []string{"img-1"}, BillingMode: BillingModeImage,
			Intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: testPtrInt(10), TierLabel: "1K", PerRequestPrice: float64Ptr(1.8)},
				{MinTokens: 10, TierLabel: "2K", PerRequestPrice: float64Ptr(2.4)},
			},
		}},
	}}
	groups := []Group{
		{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 1.5},
		{ID: 2, Name: "g2", Platform: PlatformOpenAI, RateMultiplier: 1.5, ImageRateIndependent: true, ImageRateMultiplier: 0.5},
	}
	fx := newPriceCatalogFixture(&PricingService{pricingData: map[string]*LiteLLMModelPricing{}}, channels, groups, nil, nil)
	got := fx.build(t, 9).model(PlatformOpenAI, "img-1")

	first, second, shared, independentRate := 1.8, 2.4, 1.5, 0.5
	sharedEntry := got.entry(1)
	require.Equal(t, BillingModeImage, BillingMode(sharedEntry.BillingMode))
	require.Equal(t, shared, sharedEntry.Rate)
	require.Equal(t, first*shared, *sharedEntry.Prices.Unit)
	independent := got.entry(2)
	require.Equal(t, independentRate, independent.Rate)
	require.Equal(t, first*independentRate, *independent.Prices.Unit)
	require.Equal(t, first, *independent.Official.Unit)
	require.Len(t, independent.Tiers, 2)
	require.Equal(t, "2K", independent.Tiers[1].Label)
	require.Equal(t, second*independentRate, *independent.Tiers[1].Prices.Unit)
}

// 已知偏差 5（已消除）：目录里的图片生成模型分 1K、2K、4K 三档，按张计价，倍率用图片倍率。
func TestUserPriceCatalog_ImageGenerationModelShowsSizeTiers(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"quote-test-image": {Mode: "image_generation", OutputCostPerImage: 0.2},
	}
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{1},
		ModelMapping: map[string]map[string]string{PlatformOpenAI: {"quote-test-image": "quote-test-image"}},
	}}
	groups := []Group{{ID: 1, Name: "g1", Platform: PlatformOpenAI, RateMultiplier: 2}}
	fx := newPriceCatalogFixture(&PricingService{pricingData: catalog}, channels, groups, nil, nil)
	entry := fx.build(t, 9).model(PlatformOpenAI, "quote-test-image").entry(1)

	require.NotNil(t, entry)
	require.Equal(t, userPriceKindRequest, entry.Kind)
	require.Equal(t, string(BillingModeImage), entry.BillingMode)
	base, rate := 0.2, 2.0
	require.Equal(t, base, *entry.Official.Unit)
	require.Equal(t, base*rate, *entry.Prices.Unit)
	require.Len(t, entry.Tiers, 3)
	require.Equal(t, []string{"1K", "2K", "4K"}, []string{entry.Tiers[0].Label, entry.Tiers[1].Label, entry.Tiers[2].Label})
	require.Equal(t, base*1.5, *entry.Tiers[1].Official.Unit)
	require.Equal(t, base*2*rate, *entry.Tiers[2].Prices.Unit)
}

// 响应里只有分组与价格，不带渠道名。
func TestUserPriceCatalog_ResponseCarriesNoChannelInfo(t *testing.T) {
	fx := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), zeroChangeGroups(), nil, nil)
	got := fx.build(t, 9)
	for _, g := range got.Groups {
		require.NotContains(t, []string{"ch-openai", "ch-anthropic"}, g.Name)
	}
	require.NotEmpty(t, got.Groups)
	require.Equal(t, "d2", func() string {
		for _, g := range got.Groups {
			if g.ID == 2 {
				return g.Description
			}
		}
		return ""
	}())
}

// 展示设置：只留勾选的分组与模型。
func TestUserPriceCatalog_DisplayFilters(t *testing.T) {
	fx := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), zeroChangeGroups(), nil, nil)
	got, err := fx.svc.Build(context.Background(), UserPriceCatalogQuery{
		UserID:          9,
		Groups:          fx.groups,
		DisplayGroupIDs: map[int64]struct{}{2: {}},
		DisplayModels:   map[string]struct{}{"m-flat": {}, "m-empty": {}},
	})
	require.NoError(t, err)
	require.Len(t, got.Models, 2)
	for _, m := range got.Models {
		require.Len(t, m.Entries, 1)
		require.Equal(t, int64(2), m.Entries[0].GroupID)
	}
	require.Len(t, got.Groups, 1)

	empty, err := fx.svc.Build(context.Background(), UserPriceCatalogQuery{UserID: 9, Groups: fx.groups, DisplayGroupIDs: map[int64]struct{}{99: {}}})
	require.NoError(t, err)
	require.Empty(t, empty.Models)
	require.Empty(t, empty.Groups)
}

// 同一用户、同样的分组与筛选在 30 秒内复用结果，过期后重算；不同用户互不影响。
func TestUserPriceCatalog_CacheTTL(t *testing.T) {
	fx := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), zeroChangeGroups(), nil, nil)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	fx.svc.now = func() time.Time { return now }

	first := fx.build(t, 9)
	require.Same(t, first, fx.build(t, 9))
	require.NotSame(t, first, fx.build(t, 10))

	now = now.Add(userPriceCatalogCacheTTL + time.Second)
	require.NotSame(t, first, fx.build(t, 9))
}

// v2 分组的模型清单读模型目录（只取 active），再去掉该分组里关闭的模型；价格仍由 Quoter 给出。
type stagedStubPolicy struct {
	GroupPolicy
	v2     map[int64]bool
	closed map[string]bool
}

func (p stagedStubPolicy) Stage(ctx context.Context, groupID int64) PricingStage {
	if p.v2[groupID] {
		return PricingStageV2
	}
	return p.GroupPolicy.Stage(ctx, groupID)
}

func (p stagedStubPolicy) ModelAccess(ctx context.Context, groupID int64, model string) QuoteAccess {
	if p.v2[groupID] && p.closed[model] {
		return QuoteAccess{OK: false, Reason: "closed_in_group"}
	}
	return p.GroupPolicy.ModelAccess(ctx, groupID, model)
}

type catalogListerStub struct {
	entries []ModelCatalogEntry
	err     error
}

func (s catalogListerStub) List(_ context.Context, filter ModelCatalogFilter) ([]ModelCatalogEntry, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []ModelCatalogEntry
	for _, e := range s.entries {
		if filter.Status != "" && e.Status != filter.Status {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func TestUserPriceCatalog_V2GroupReadsCatalog(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{
		"m-a":      {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
		"m-closed": {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
		"m-draft":  {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
		"claude-x": {Mode: "chat", InputCostPerToken: 3e-6, OutputCostPerToken: 15e-6},
	}
	lister := catalogListerStub{entries: []ModelCatalogEntry{
		{ModelKey: "m-a", Platform: PlatformOpenAI, Status: ModelCatalogActive},
		{ModelKey: "m-closed", Platform: PlatformOpenAI, Status: ModelCatalogActive},
		{ModelKey: "m-draft", Platform: PlatformOpenAI, Status: ModelCatalogDraft},
		{ModelKey: "claude-x", Platform: PlatformAnthropic, Status: ModelCatalogActive},
	}}
	groups := []Group{
		{ID: 1, Name: "legacy-g", Platform: PlatformOpenAI, RateMultiplier: 1.4},
		{ID: 5, Name: "v2-g", Platform: PlatformOpenAI, RateMultiplier: 2},
	}
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{1},
		ModelMapping: map[string]map[string]string{PlatformOpenAI: {"m-a": "m-a"}},
	}}
	fx := newPriceCatalogFixture(&PricingService{pricingData: catalog}, channels, groups, nil, lister)
	fx.resolver.policyOverride = stagedStubPolicy{
		GroupPolicy: legacyPolicy{cs: fx.cs},
		v2:          map[int64]bool{5: true},
		closed:      map[string]bool{"m-closed": true},
	}
	got := fx.build(t, 9)

	require.Equal(t, PricingStageV2, fx.quoter.GroupStage(context.Background(), 5))
	require.Equal(t, PricingStageLegacy, fx.quoter.GroupStage(context.Background(), 1))

	a := got.model(PlatformOpenAI, "m-a")
	require.NotNil(t, a)
	require.Len(t, a.Entries, 2, "legacy 分组走渠道清单，v2 分组走目录清单，同一个模型合并成一行")
	require.Equal(t, 1.4, a.entry(1).Rate)
	require.Equal(t, 2.0, a.entry(5).Rate)
	price, rate := 1e-6, 2.0
	require.Equal(t, price*rate, *a.entry(5).Prices.Input)

	require.Nil(t, got.model(PlatformOpenAI, "m-closed"), "在 v2 分组里关闭的模型不出现")
	require.Nil(t, got.model(PlatformOpenAI, "m-draft"), "目录里不是 active 的模型不出现")
	require.Nil(t, got.model(PlatformAnthropic, "claude-x"), "平台与分组不同的目录模型不出现")
}

func TestUserPriceCatalog_V2CatalogErrorPropagates(t *testing.T) {
	groups := []Group{{ID: 5, Name: "v2-g", Platform: PlatformOpenAI, RateMultiplier: 2}}
	fx := newPriceCatalogFixture(&PricingService{pricingData: map[string]*LiteLLMModelPricing{}}, nil, groups, nil, catalogListerStub{err: context.DeadlineExceeded})
	fx.resolver.policyOverride = stagedStubPolicy{GroupPolicy: legacyPolicy{cs: fx.cs}, v2: map[int64]bool{5: true}}
	_, err := fx.svc.Build(context.Background(), UserPriceCatalogQuery{UserID: 9, Groups: groups})
	require.Error(t, err)
}

func TestUserPriceCatalog_UnavailableQuoter(t *testing.T) {
	var svc *UserPriceCatalogService
	_, err := svc.Build(context.Background(), UserPriceCatalogQuery{})
	require.ErrorIs(t, err, ErrPriceQuoterUnavailable)
}

type countingGroupReader struct {
	inner priceQuoteGroupReader
	calls int
}

func (r *countingGroupReader) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	r.calls++
	return r.inner.GetByIDLite(ctx, id)
}

// BatchQuote 的每个结果与单独调用 Quote 相同，同一批里相同分组只读一次；失败的请求位置为 nil。
func TestPriceQuoter_BatchQuoteMatchesQuote(t *testing.T) {
	fx := newPriceCatalogFixture(&PricingService{pricingData: zeroChangeCatalog()}, zeroChangeChannels(), zeroChangeGroups(), nil, nil)
	counter := &countingGroupReader{inner: fx.quoter.groups}
	fx.quoter.groups = counter

	reqs := []QuoteRequest{
		{Model: "m-flat", GroupID: 1},
		{Model: "m-interval", GroupID: 1},
		{Model: "m-flat", GroupID: 2},
		{Model: "", GroupID: 1},
		{Model: "m-flat", GroupID: 404},
	}
	counter.calls = 0
	batch := fx.quoter.BatchQuote(context.Background(), reqs)
	require.Len(t, batch, len(reqs))
	require.Equal(t, 3, counter.calls, "分组 1、2、404 各读一次")
	require.Nil(t, batch[3])
	require.Nil(t, batch[4])

	for _, i := range []int{0, 1, 2} {
		single, err := fx.quoter.Quote(context.Background(), reqs[i])
		require.NoError(t, err)
		require.Equal(t, single.FinalPrices, batch[i].FinalPrices)
		require.Equal(t, single.Prices, batch[i].Prices)
		require.Equal(t, single.EffectiveMultiplier, batch[i].EffectiveMultiplier)
		require.Equal(t, single.Access, batch[i].Access)
		require.Equal(t, single.Intervals, batch[i].Intervals)
	}

	var nilQuoter *PriceQuoter
	require.Len(t, nilQuoter.BatchQuote(context.Background(), reqs), len(reqs))
	require.Equal(t, PricingStageLegacy, nilQuoter.GroupStage(context.Background(), 1))
}
