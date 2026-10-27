//go:build unit

package service

// W6 PR3：GroupPolicy 门面与 legacyPolicy 的等价性测试。
//
// 思路：同一份 ChannelService 数据，同时走「旧路径」（直接读 ChannelService，下面的 pre* 函数是改造前
// 调用点代码的逐字拷贝）和「policy 路径」（legacyPolicy），在 分组 × 模型 的网格上逐点比较。
// 网格里有：精确名、大小写与首尾空白、通配符、codex 归一化变体、claude 点号写法、停用渠道、
// 没有渠道的分组、缓存加载失败。每个测试另有几条写死的期望，避免对照双方同时变成空壳。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	gpGroupMain          = int64(10) // OpenAI 分组：限制模型、渠道映射计费、有映射与通配符、功能开关齐全
	gpGroupAnthropic     = int64(11) // 同一渠道里的 Anthropic 分组
	gpGroupApply         = int64(20) // 勾选「应用模型定价到账号统计」，计费来源 requested，不限制模型
	gpGroupUpstream      = int64(30) // 限制模型 + 计费来源 upstream
	gpGroupUpstreamAstra = int64(31) // 同上，定价里多一个 gpt-6-astra
	gpGroupDisabled      = int64(40) // 渠道已停用，等同没有渠道
	gpGroupNone          = int64(99) // 不属于任何渠道
)

var gpGroups = []int64{gpGroupMain, gpGroupAnthropic, gpGroupApply, gpGroupUpstream, gpGroupUpstreamAstra, gpGroupDisabled, gpGroupNone}

var gpProbeModels = []string{
	"",
	"gpt-5.6-luna", "GPT-5.6-LUNA", "  gpt-5.6-luna  ",
	"gpt-5.6-luna-high", "gpt-5.6-luna-2026-08-01",
	"gpt-5.4", "gpt-5.4-mini", "GPT-5.4-Mini",
	"gpt-5.5", "gpt-5.3-codex", "gpt-5.6-sol", "gpt-6-astra", "gpt-image-2", "unknown-model",
	"claude-sonnet-4.5", "claude-sonnet-4-5", " Claude-Sonnet-4.5 ", "claude-opus-4-5", "claude-opus-4.5",
}

var gpPlatforms = []string{PlatformOpenAI, PlatformAnthropic, ""}

func gpTokenPricing(platform string, input float64, models ...string) ChannelModelPricing {
	return ChannelModelPricing{
		Platform:    platform,
		Models:      models,
		BillingMode: BillingModeToken,
		InputPrice:  testPtrFloat64(input),
		OutputPrice: testPtrFloat64(input * 4),
	}
}

// newGroupPolicyFixture 构造一份覆盖各种情形的渠道缓存（不经过仓库）。
func newGroupPolicyFixture() *ChannelService {
	channels := []Channel{
		{
			ID: 1, Name: "main", Status: StatusActive,
			BillingModelSource: BillingModelSourceChannelMapped, RestrictModels: true,
			GroupIDs: []int64{gpGroupMain, gpGroupAnthropic},
			ModelPricing: []ChannelModelPricing{
				gpTokenPricing(PlatformOpenAI, 0.4e-6, "gpt-5.6-luna"),
				gpTokenPricing(PlatformOpenAI, 0.3e-6, "GPT-5.4*"),
				{Platform: PlatformOpenAI, Models: []string{"gpt-image-2"}, BillingMode: BillingModeImage, PerRequestPrice: testPtrFloat64(0.04)},
				gpTokenPricing(PlatformAnthropic, 3e-6, "claude-sonnet-4.5"),
			},
			ModelMapping: map[string]map[string]string{
				PlatformOpenAI:    {"gpt-5.5": "gpt-5.6-luna", "gpt-5.3*": "gpt-5.4"},
				PlatformAnthropic: {"claude-opus-4-5": "claude-sonnet-4-5"},
			},
			FeaturesConfig: map[string]any{
				featureKeyWebSearchEmulation:         map[string]any{PlatformAnthropic: true, PlatformOpenAI: false},
				featureKeyBedrockCCCompat:            true,
				featureKeyCodexImageGenerationBridge: map[string]any{PlatformOpenAI: true},
			},
			AccountStatsPricingRules: []AccountStatsPricingRule{{
				ID: 1, ChannelID: 1, AccountIDs: []int64{7}, GroupIDs: []int64{gpGroupAnthropic},
				Pricing: []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.001, "gpt-5.6-luna")},
			}},
		},
		{
			ID: 2, Name: "apply", Status: StatusActive,
			BillingModelSource: BillingModelSourceRequested, ApplyPricingToAccountStats: true,
			GroupIDs: []int64{gpGroupApply},
			ModelPricing: []ChannelModelPricing{
				gpTokenPricing(PlatformOpenAI, 0.2e-6, "gpt-5.6-luna"),
				gpTokenPricing(PlatformOpenAI, 0.7e-6, "gpt-5.6-luna-high"),
			},
			FeaturesConfig: map[string]any{
				featureKeyWebSearchEmulation:         map[string]any{PlatformAnthropic: false},
				featureKeyBedrockCCCompat:            false,
				featureKeyCodexImageGenerationBridge: false,
			},
		},
		{
			ID: 3, Name: "upstream", Status: StatusActive,
			BillingModelSource: BillingModelSourceUpstream, RestrictModels: true,
			GroupIDs:     []int64{gpGroupUpstream},
			ModelPricing: []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.4e-6, "gpt-5.6-luna")},
		},
		{
			ID: 4, Name: "disabled", Status: StatusDisabled,
			BillingModelSource: BillingModelSourceUpstream, RestrictModels: true, ApplyPricingToAccountStats: true,
			GroupIDs:     []int64{gpGroupDisabled},
			ModelPricing: []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.4e-6, "gpt-5.6-luna")},
			ModelMapping: map[string]map[string]string{PlatformOpenAI: {"gpt-5.5": "gpt-5.6-luna"}},
			FeaturesConfig: map[string]any{
				featureKeyBedrockCCCompat:            true,
				featureKeyCodexImageGenerationBridge: map[string]any{PlatformOpenAI: true},
			},
		},
		{
			ID: 5, Name: "upstream-astra", Status: StatusActive,
			BillingModelSource: BillingModelSourceUpstream, RestrictModels: true,
			GroupIDs: []int64{gpGroupUpstreamAstra},
			ModelPricing: []ChannelModelPricing{
				gpTokenPricing(PlatformOpenAI, 0.4e-6, "gpt-5.6-luna"),
				gpTokenPricing(PlatformOpenAI, 1e-6, "gpt-6-astra"),
			},
		},
	}
	platforms := map[int64]string{
		gpGroupMain: PlatformOpenAI, gpGroupAnthropic: PlatformAnthropic, gpGroupApply: PlatformOpenAI,
		gpGroupUpstream: PlatformOpenAI, gpGroupUpstreamAstra: PlatformOpenAI, gpGroupDisabled: PlatformOpenAI,
	}
	cs := &ChannelService{}
	cs.cache.Store(populateChannelCache(channels, platforms))
	return cs
}

// gpErrChannelService 返回一个缓存加载必定失败的 ChannelService（只有第一次读取会报错，之后落到短 TTL 的空缓存）。
func gpErrChannelService() *ChannelService {
	return newTestChannelService(&mockChannelRepository{
		listAllFn: func(context.Context) ([]Channel, error) { return nil, errors.New("db down") },
	})
}

// gpForbiddenChannelService 返回一个一旦被读取就让测试失败的 ChannelService，
// 用来证明注入了 GroupPolicy 之后调用点不会绕过它直接读渠道。
func gpForbiddenChannelService(t *testing.T) *ChannelService {
	t.Helper()
	return newTestChannelService(&mockChannelRepository{
		listAllFn: func(context.Context) ([]Channel, error) {
			t.Error("channel service must not be read when a GroupPolicy is injected")
			return nil, errors.New("forbidden")
		},
	})
}

// ---------------------------------------------------------------------------
// 旧路径：改造前各调用点直接读 ChannelService 的代码，逐字保留
// ---------------------------------------------------------------------------

// preLookupChannelPricingNormalized 是 ModelPricingResolver.lookupChannelPricingNormalized 改造前的函数体。
func preLookupChannelPricingNormalized(ctx context.Context, cs *ChannelService, groupID int64, model string) *ChannelModelPricing {
	if cs == nil {
		return nil
	}
	if pricing := cs.GetChannelModelPricing(ctx, groupID, model); pricing != nil {
		return pricing
	}
	normalized := normalizeKnownOpenAICodexModel(model)
	if normalized == "" || strings.EqualFold(normalized, strings.TrimSpace(model)) {
		return nil
	}
	return cs.GetChannelModelPricing(ctx, groupID, normalized)
}

// preGroupReads 是两个网关里 channelService 读口包装函数在改造前的逻辑。
type preGroupReads struct{ cs *ChannelService }

func (o preGroupReads) resolveChannelMapping(ctx context.Context, groupID int64, model string) ChannelMappingResult {
	if o.cs == nil {
		return ChannelMappingResult{MappedModel: model}
	}
	return o.cs.ResolveChannelMapping(ctx, groupID, model)
}

func (o preGroupReads) isModelRestricted(ctx context.Context, groupID int64, model string) bool {
	if o.cs == nil {
		return false
	}
	return o.cs.IsModelRestricted(ctx, groupID, model)
}

func (o preGroupReads) resolveChannelMappingAndRestrict(ctx context.Context, groupID *int64, model string) (ChannelMappingResult, bool) {
	if o.cs == nil {
		return ChannelMappingResult{MappedModel: model}, false
	}
	return o.cs.ResolveChannelMappingAndRestrict(ctx, groupID, model)
}

func (o preGroupReads) checkChannelPricingRestriction(ctx context.Context, groupID *int64, requestedModel string) bool {
	if groupID == nil || o.cs == nil || requestedModel == "" {
		return false
	}
	mapping := o.cs.ResolveChannelMapping(ctx, *groupID, requestedModel)
	billingModel := billingModelForRestriction(mapping.BillingModelSource, requestedModel, mapping.MappedModel)
	if billingModel == "" {
		return false
	}
	return o.cs.IsModelRestricted(ctx, *groupID, billingModel)
}

// isUpstreamModelRestricted 是 isUpstreamModelRestrictedByChannel 在算出上游模型之后的部分。
func (o preGroupReads) isUpstreamModelRestricted(ctx context.Context, groupID int64, upstreamModel string) bool {
	if o.cs == nil {
		return false
	}
	if upstreamModel == "" {
		return false
	}
	return o.cs.IsModelRestricted(ctx, groupID, upstreamModel)
}

func (o preGroupReads) needsUpstreamChannelRestrictionCheck(ctx context.Context, groupID *int64) bool {
	if groupID == nil || o.cs == nil {
		return false
	}
	ch, err := o.cs.GetChannelForGroup(ctx, *groupID)
	if err != nil {
		return false
	}
	if ch == nil || !ch.RestrictModels {
		return false
	}
	return ch.BillingModelSource == BillingModelSourceUpstream
}

func (o preGroupReads) bedrockCCCompatEnabled(ctx context.Context, platform string, groupID *int64) bool {
	if groupID == nil || o.cs == nil {
		return false
	}
	ch, err := o.cs.GetChannelForGroup(ctx, *groupID)
	if err != nil || ch == nil {
		return false
	}
	return ch.IsBedrockCCCompatEnabled(platform)
}

func (o preGroupReads) webSearchEmulationEnabled(ctx context.Context, platform string, groupID *int64) bool {
	if groupID == nil || o.cs == nil {
		return false
	}
	ch, err := o.cs.GetChannelForGroup(ctx, *groupID)
	if err != nil || ch == nil {
		return false
	}
	return ch.IsWebSearchEmulationEnabled(platform)
}

func (o preGroupReads) codexImageBridgeOverride(ctx context.Context, groupID *int64) *bool {
	if o.cs == nil || groupID == nil {
		return nil
	}
	ch, err := o.cs.GetChannelForGroup(ctx, *groupID)
	if err != nil {
		return nil
	}
	return ch.CodexImageGenerationBridgeOverride(PlatformOpenAI)
}

// preResolveAccountStatsCost 是 resolveAccountStatsCost 改造前的逻辑（直接读 ChannelService）。
func preResolveAccountStatsCost(
	ctx context.Context,
	channelService *ChannelService,
	billingService *BillingService,
	accountID int64,
	groupID int64,
	upstreamModel string,
	tokens UsageTokens,
	requestCount int,
	totalCost float64,
	serviceTier string,
	pricingAt time.Time,
) *float64 {
	if channelService == nil || upstreamModel == "" {
		return nil
	}
	channel, err := channelService.GetChannelForGroup(ctx, groupID)
	if err != nil || channel == nil {
		return nil
	}
	platform := channelService.GetGroupPlatform(ctx, groupID)
	if cost := tryCustomRules(channel.AccountStatsPricingRules, accountID, groupID, platform, upstreamModel, tokens, requestCount); cost != nil {
		return cost
	}
	if channel.ApplyPricingToAccountStats {
		cost := totalCost
		if cost <= 0 {
			return nil
		}
		return &cost
	}
	if billingService != nil {
		return tryModelFilePricing(billingService, upstreamModel, tokens, serviceTier, pricingAt)
	}
	return nil
}

// ---------------------------------------------------------------------------
// legacyPolicy 与 ChannelService 逐点比较
// ---------------------------------------------------------------------------

func TestLegacyPolicy_Mapping(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	p := newLegacyGroupPolicy(cs)

	var mapped, unmapped int
	for _, gid := range gpGroups {
		for _, model := range gpProbeModels {
			want := cs.ResolveChannelMapping(ctx, gid, model)
			require.Equal(t, want, p.Mapping(ctx, gid, model), "group=%d model=%q", gid, model)
			if want.Mapped {
				mapped++
			} else {
				unmapped++
			}
		}
	}
	require.NotZero(t, mapped, "the grid must contain mapped models")
	require.NotZero(t, unmapped, "the grid must contain unmapped models")

	// 精确映射（大小写与首尾空白不敏感）、通配符映射、计费来源与渠道 ID 原样带出。
	got := p.Mapping(ctx, gpGroupMain, " GPT-5.5 ")
	require.Equal(t, ChannelMappingResult{MappedModel: "gpt-5.6-luna", ChannelID: 1, Mapped: true, BillingModelSource: BillingModelSourceChannelMapped}, got)
	require.Equal(t, "gpt-5.4", p.Mapping(ctx, gpGroupMain, "gpt-5.3-codex").MappedModel)
	// 没有渠道、渠道停用：原样返回请求模型，计费来源为空串，渠道 ID 为 0。
	for _, gid := range []int64{gpGroupNone, gpGroupDisabled} {
		require.Equal(t, ChannelMappingResult{MappedModel: "gpt-5.5"}, p.Mapping(ctx, gid, "gpt-5.5"), "group=%d", gid)
	}
}

func TestLegacyPolicy_ModelAccessAndUpstreamAccess(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	p := newLegacyGroupPolicy(cs)

	var restrictedCount, allowedCount int
	for _, gid := range gpGroups {
		for _, model := range gpProbeModels {
			restricted := cs.IsModelRestricted(ctx, gid, model)
			got := p.ModelAccess(ctx, gid, model)
			require.Equal(t, !restricted, got.OK, "group=%d model=%q", gid, model)
			if restricted {
				restrictedCount++
				require.Equal(t, QuoteAccessReasonNotInAllowlist, got.Reason)
			} else {
				allowedCount++
				require.Empty(t, got.Reason)
			}
			require.Equal(t, got, p.UpstreamAccess(ctx, gid, model), "group=%d model=%q", gid, model)
		}
	}
	require.NotZero(t, restrictedCount)
	require.NotZero(t, allowedCount)

	// 查找方式与 checkRestricted 一致：大小写与首尾空白不敏感，通配符生效，不做 codex 归一化。
	require.True(t, p.ModelAccess(ctx, gpGroupMain, "gpt-5.6-luna").OK)
	require.True(t, p.ModelAccess(ctx, gpGroupMain, "  GPT-5.6-LUNA ").OK)
	require.True(t, p.ModelAccess(ctx, gpGroupMain, "GPT-5.4-Mini").OK)
	require.False(t, p.ModelAccess(ctx, gpGroupMain, "gpt-5.6-luna-high").OK, "ModelAccess must not apply codex normalization")
	require.False(t, p.ModelAccess(ctx, gpGroupMain, "unknown-model").OK)
	// 不限制模型、没有渠道、渠道停用：一律放行。
	for _, gid := range []int64{gpGroupApply, gpGroupDisabled, gpGroupNone} {
		require.True(t, p.ModelAccess(ctx, gid, "unknown-model").OK, "group=%d", gid)
	}
}

func TestLegacyPolicy_UpstreamCheck(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	p := newLegacyGroupPolicy(cs)
	pre := preGroupReads{cs: cs}

	for _, gid := range gpGroups {
		gid := gid
		got, err := p.UpstreamCheck(ctx, gid)
		require.NoError(t, err)
		require.Equal(t, pre.needsUpstreamChannelRestrictionCheck(ctx, &gid), got, "group=%d", gid)
	}
	for gid, want := range map[int64]bool{
		gpGroupMain: false, gpGroupApply: false, gpGroupUpstream: true,
		gpGroupUpstreamAstra: true, gpGroupDisabled: false, gpGroupNone: false,
	} {
		got, err := p.UpstreamCheck(ctx, gid)
		require.NoError(t, err)
		require.Equal(t, want, got, "group=%d", gid)
	}

	// 缓存加载失败：错误交给调用方，布尔值为 false。
	got, err := newLegacyGroupPolicy(gpErrChannelService()).UpstreamCheck(ctx, gpGroupUpstream)
	require.Error(t, err)
	require.False(t, got)
}

func TestLegacyPolicy_Feature(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	p := newLegacyGroupPolicy(cs)
	pre := preGroupReads{cs: cs}

	boolVal := func(v *bool) string {
		if v == nil {
			return "nil"
		}
		return fmt.Sprint(*v)
	}

	for _, gid := range gpGroups {
		gid := gid
		for _, platform := range gpPlatforms {
			web, err := p.Feature(ctx, gid, platform, GroupFeatureWebSearchEmulation)
			require.NoError(t, err)
			require.Equal(t, pre.webSearchEmulationEnabled(ctx, platform, &gid), web != nil && *web, "web_search group=%d platform=%q", gid, platform)

			bedrock, err := p.Feature(ctx, gid, platform, GroupFeatureBedrockCCCompat)
			require.NoError(t, err)
			require.Equal(t, pre.bedrockCCCompatEnabled(ctx, platform, &gid), bedrock != nil && *bedrock, "bedrock group=%d platform=%q", gid, platform)

			// codex 图片桥有「未设置」一档，必须逐位相同（nil 与 nil、或同一个布尔值）。
			codex, err := p.Feature(ctx, gid, platform, GroupFeatureCodexImageGenerationBridge)
			require.NoError(t, err)
			var want *bool
			if ch, chErr := cs.GetChannelForGroup(ctx, gid); chErr == nil {
				want = ch.CodexImageGenerationBridgeOverride(platform)
			}
			require.Equal(t, boolVal(want), boolVal(codex), "codex group=%d platform=%q", gid, platform)
		}
	}

	// 写死的期望。
	web, _ := p.Feature(ctx, gpGroupMain, PlatformAnthropic, GroupFeatureWebSearchEmulation)
	require.NotNil(t, web)
	require.True(t, *web)
	web, _ = p.Feature(ctx, gpGroupMain, PlatformOpenAI, GroupFeatureWebSearchEmulation)
	require.NotNil(t, web)
	require.False(t, *web)
	bedrock, _ := p.Feature(ctx, gpGroupMain, PlatformAnthropic, GroupFeatureBedrockCCCompat)
	require.NotNil(t, bedrock)
	require.True(t, *bedrock)
	codex, _ := p.Feature(ctx, gpGroupMain, PlatformOpenAI, GroupFeatureCodexImageGenerationBridge)
	require.NotNil(t, codex)
	require.True(t, *codex)
	codex, _ = p.Feature(ctx, gpGroupMain, PlatformAnthropic, GroupFeatureCodexImageGenerationBridge)
	require.Nil(t, codex, "platform without an entry means no override")
	codex, _ = p.Feature(ctx, gpGroupApply, PlatformOpenAI, GroupFeatureCodexImageGenerationBridge)
	require.NotNil(t, codex)
	require.False(t, *codex, "a bare false is an explicit override")
	// 没有渠道、渠道停用、未知开关：没有显式设置。
	for _, gid := range []int64{gpGroupNone, gpGroupDisabled} {
		for _, f := range []GroupFeature{GroupFeatureWebSearchEmulation, GroupFeatureBedrockCCCompat, GroupFeatureCodexImageGenerationBridge} {
			v, err := p.Feature(ctx, gid, PlatformOpenAI, f)
			require.NoError(t, err)
			require.Nil(t, v, "group=%d feature=%s", gid, f)
		}
	}
	unknown, err := p.Feature(ctx, gpGroupMain, PlatformOpenAI, GroupFeature("no_such_feature"))
	require.NoError(t, err)
	require.Nil(t, unknown)

	// 缓存加载失败：错误交给调用方。
	v, err := newLegacyGroupPolicy(gpErrChannelService()).Feature(ctx, gpGroupMain, PlatformOpenAI, GroupFeatureCodexImageGenerationBridge)
	require.Error(t, err)
	require.Nil(t, v)
}

func TestLegacyPolicy_PriceOverride(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	p := newLegacyGroupPolicy(cs)

	var hits, misses int
	for _, gid := range gpGroups {
		for _, model := range gpProbeModels {
			want := preLookupChannelPricingNormalized(ctx, cs, gid, model)
			got := p.PriceOverride(ctx, gid, model, time.Time{})
			require.Equal(t, want, got, "group=%d model=%q", gid, model)
			// 计费时点 legacy 不使用。
			require.Equal(t, got, p.PriceOverride(ctx, gid, model, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)))
			if got != nil {
				hits++
			} else {
				misses++
			}
		}
	}
	require.NotZero(t, hits)
	require.NotZero(t, misses)

	input := func(gid int64, model string) float64 {
		got := p.PriceOverride(ctx, gid, model, time.Time{})
		require.NotNil(t, got, "group=%d model=%q", gid, model)
		require.NotNil(t, got.InputPrice)
		return *got.InputPrice
	}
	require.InDelta(t, 0.4e-6, input(gpGroupMain, "gpt-5.6-luna"), 1e-15)
	require.InDelta(t, 0.4e-6, input(gpGroupMain, "  gpt-5.6-luna  "), 1e-15, "surrounding whitespace is trimmed by the cache lookup")
	require.InDelta(t, 0.4e-6, input(gpGroupMain, "GPT-5.6-LUNA"), 1e-15)
	require.InDelta(t, 0.4e-6, input(gpGroupMain, "gpt-5.6-luna-high"), 1e-15, "variant falls back to the base name through the codex normalization")
	require.InDelta(t, 0.3e-6, input(gpGroupMain, "GPT-5.4-Mini"), 1e-15, "wildcard entry")
	require.InDelta(t, 3e-6, input(gpGroupAnthropic, "Claude-Sonnet-4-5"), 1e-15, "claude dot and hyphen spellings are the same entry")
	require.InDelta(t, 0.7e-6, input(gpGroupApply, "gpt-5.6-luna-high"), 1e-15, "a literal entry wins over the base name")
	require.InDelta(t, 0.2e-6, input(gpGroupApply, "gpt-5.6-luna"), 1e-15)

	require.Nil(t, p.PriceOverride(ctx, gpGroupMain, "unknown-model", time.Time{}))
	require.Nil(t, p.PriceOverride(ctx, gpGroupNone, "gpt-5.6-luna", time.Time{}), "no channel")
	require.Nil(t, p.PriceOverride(ctx, gpGroupDisabled, "gpt-5.6-luna", time.Time{}), "disabled channel behaves like no channel")
	require.Nil(t, p.PriceOverride(ctx, gpGroupMain, "claude-sonnet-4-5", time.Time{}), "pricing entries are per platform")
}

func TestLegacyPolicy_CostModeAndRules(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	p := newLegacyGroupPolicy(cs)

	for gid, want := range map[int64]MatrixCostMode{
		gpGroupMain: MatrixCostCatalogUpstream, gpGroupAnthropic: MatrixCostCatalogUpstream,
		gpGroupApply: MatrixCostFollowBilling, gpGroupUpstream: MatrixCostCatalogUpstream,
		gpGroupUpstreamAstra: MatrixCostCatalogUpstream,
		gpGroupDisabled:      MatrixCostAccountRate, gpGroupNone: MatrixCostAccountRate,
	} {
		require.Equal(t, want, p.CostMode(ctx, gid), "group=%d", gid)
	}

	for _, gid := range gpGroups {
		ch, err := cs.GetChannelForGroup(ctx, gid)
		require.NoError(t, err)
		rules, platform := p.CostRules(ctx, gid)
		if ch == nil {
			require.Nil(t, rules, "group=%d", gid)
			require.Empty(t, platform)
			continue
		}
		require.Equal(t, ch.AccountStatsPricingRules, rules, "group=%d", gid)
		require.Equal(t, cs.GetGroupPlatform(ctx, gid), platform, "group=%d", gid)
	}
	rules, platform := p.CostRules(ctx, gpGroupAnthropic)
	require.Len(t, rules, 1)
	require.Equal(t, PlatformAnthropic, platform)

	// 返回的是副本：调用方改动不会污染缓存里的规则。
	rules[0].AccountIDs[0] = 12345
	again, _ := p.CostRules(ctx, gpGroupAnthropic)
	require.Equal(t, []int64{7}, again[0].AccountIDs)

	// 缓存加载失败等同没有渠道。
	require.Equal(t, MatrixCostAccountRate, newLegacyGroupPolicy(gpErrChannelService()).CostMode(ctx, gpGroupMain))
	errRules, errPlatform := newLegacyGroupPolicy(gpErrChannelService()).CostRules(ctx, gpGroupMain)
	require.Nil(t, errRules)
	require.Empty(t, errPlatform)
}

func TestLegacyPolicy_ErrorPathsMatchChannelService(t *testing.T) {
	ctx := context.Background()
	// 每个断言用一个全新的 ChannelService：加载失败只会让第一次读取报错。
	require.Equal(t, ChannelMappingResult{MappedModel: "gpt-5.5"}, newLegacyGroupPolicy(gpErrChannelService()).Mapping(ctx, gpGroupMain, "gpt-5.5"))
	require.True(t, newLegacyGroupPolicy(gpErrChannelService()).ModelAccess(ctx, gpGroupMain, "unknown-model").OK, "a failed cache load admits the model, as IsModelRestricted does")
	require.True(t, newLegacyGroupPolicy(gpErrChannelService()).UpstreamAccess(ctx, gpGroupMain, "unknown-model").OK)
	require.Nil(t, newLegacyGroupPolicy(gpErrChannelService()).PriceOverride(ctx, gpGroupMain, "gpt-5.6-luna", time.Time{}))
}

func TestLegacyPolicy_ExtraMultiplierAndStage(t *testing.T) {
	ctx := context.Background()
	p := newLegacyGroupPolicy(newGroupPolicyFixture())
	for _, gid := range gpGroups {
		for _, model := range gpProbeModels {
			require.Equal(t, 1.0, p.ExtraMultiplier(ctx, gid, model, time.Time{}), "group=%d model=%q", gid, model)
		}
		require.Equal(t, PricingStageLegacy, p.Stage(ctx, gid), "group=%d", gid)
	}
}

// ---------------------------------------------------------------------------
// 构造与选择
// ---------------------------------------------------------------------------

func TestNewLegacyGroupPolicy_NilChannelServiceIsNilPolicy(t *testing.T) {
	// 返回的必须是 nil 接口，而不是装着 nil 指针的接口：调用方靠 `== nil` 判断「没有渠道服务」。
	require.True(t, newLegacyGroupPolicy(nil) == nil)
	require.True(t, newLegacyGroupPolicy((*ChannelService)(nil)) == nil)
	require.True(t, resolveGroupPolicy(nil, nil) == nil)

	cs := newGroupPolicyFixture()
	require.Equal(t, legacyPolicy{cs: cs}, newLegacyGroupPolicy(cs))
	require.Equal(t, legacyPolicy{cs: cs}, resolveGroupPolicy(nil, cs))
}

func TestResolveGroupPolicy_OverrideWins(t *testing.T) {
	rec := &recordingGroupPolicy{}
	cs := newGroupPolicyFixture()
	require.Same(t, rec, resolveGroupPolicy(rec, cs))
	require.Same(t, rec, resolveGroupPolicy(rec, nil))
}

func TestGroupPolicyAccessors(t *testing.T) {
	cs := newGroupPolicyFixture()
	rec := &recordingGroupPolicy{}

	// 没有渠道服务、也没有注入：nil；接收者本身为 nil 也不能 panic（isCodexImageGenerationBridgeEnabled 对 s 做了判空）。
	require.True(t, (&GatewayService{}).groupPolicy() == nil)
	require.True(t, (&OpenAIGatewayService{}).groupPolicy() == nil)
	require.True(t, (&ModelPricingResolver{}).groupPolicy() == nil)
	require.True(t, (*GatewayService)(nil).groupPolicy() == nil)
	require.True(t, (*OpenAIGatewayService)(nil).groupPolicy() == nil)
	require.True(t, (*ModelPricingResolver)(nil).groupPolicy() == nil)

	// 由 channelService 现取，不缓存：之后改字段仍然生效。
	gw := &GatewayService{}
	require.True(t, gw.groupPolicy() == nil)
	gw.channelService = cs
	require.Equal(t, legacyPolicy{cs: cs}, gw.groupPolicy())

	require.Equal(t, legacyPolicy{cs: cs}, (&OpenAIGatewayService{channelService: cs}).groupPolicy())
	require.Equal(t, legacyPolicy{cs: cs}, NewModelPricingResolver(cs, nil).groupPolicy())

	// 注入的优先。
	require.Same(t, rec, (&GatewayService{channelService: cs, policyOverride: rec}).groupPolicy())
	require.Same(t, rec, (&OpenAIGatewayService{channelService: cs, policyOverride: rec}).groupPolicy())
	require.Same(t, rec, (&ModelPricingResolver{channelService: cs, policyOverride: rec}).groupPolicy())
}

// ---------------------------------------------------------------------------
// 记录调用的 GroupPolicy，用来证明各调用点确实走门面
// ---------------------------------------------------------------------------

type recordingGroupPolicy struct {
	calls []string

	mapping          ChannelMappingResult
	modelAccess      QuoteAccess
	upstreamAccess   QuoteAccess
	upstreamCheck    bool
	upstreamCheckErr error
	feature          *bool
	featureErr       error
	override         *ChannelModelPricing
	costMode         MatrixCostMode
	costRules        []AccountStatsPricingRule
	costPlatform     string
}

var _ GroupPolicy = (*recordingGroupPolicy)(nil)

func (r *recordingGroupPolicy) record(format string, args ...any) {
	r.calls = append(r.calls, fmt.Sprintf(format, args...))
}

// take 返回并清空已记录的调用。
func (r *recordingGroupPolicy) take() []string {
	calls := r.calls
	r.calls = nil
	return calls
}

func (r *recordingGroupPolicy) Mapping(_ context.Context, groupID int64, model string) ChannelMappingResult {
	r.record("Mapping(%d,%s)", groupID, model)
	return r.mapping
}

func (r *recordingGroupPolicy) ModelAccess(_ context.Context, groupID int64, model string) QuoteAccess {
	r.record("ModelAccess(%d,%s)", groupID, model)
	return r.modelAccess
}

func (r *recordingGroupPolicy) UpstreamAccess(_ context.Context, groupID int64, upstreamModel string) QuoteAccess {
	r.record("UpstreamAccess(%d,%s)", groupID, upstreamModel)
	return r.upstreamAccess
}

func (r *recordingGroupPolicy) UpstreamCheck(_ context.Context, groupID int64) (bool, error) {
	r.record("UpstreamCheck(%d)", groupID)
	return r.upstreamCheck, r.upstreamCheckErr
}

func (r *recordingGroupPolicy) Feature(_ context.Context, groupID int64, platform string, f GroupFeature) (*bool, error) {
	r.record("Feature(%d,%s,%s)", groupID, platform, f)
	return r.feature, r.featureErr
}

func (r *recordingGroupPolicy) PriceOverride(_ context.Context, groupID int64, model string, _ time.Time) *ChannelModelPricing {
	r.record("PriceOverride(%d,%s)", groupID, model)
	return r.override
}

func (r *recordingGroupPolicy) ExtraMultiplier(_ context.Context, groupID int64, model string, _ time.Time) float64 {
	r.record("ExtraMultiplier(%d,%s)", groupID, model)
	return 1
}

func (r *recordingGroupPolicy) CostMode(_ context.Context, groupID int64) MatrixCostMode {
	r.record("CostMode(%d)", groupID)
	return r.costMode
}

func (r *recordingGroupPolicy) CostRules(_ context.Context, groupID int64) ([]AccountStatsPricingRule, string) {
	r.record("CostRules(%d)", groupID)
	return r.costRules, r.costPlatform
}

func (r *recordingGroupPolicy) Stage(_ context.Context, groupID int64) PricingStage {
	r.record("Stage(%d)", groupID)
	return PricingStageLegacy
}
