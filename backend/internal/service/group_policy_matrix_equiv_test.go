//go:build unit

package service

// W6 PR4-1：matrixPolicy 与 legacyPolicy 的逐点等价性测试。
//
// 思路：同一份渠道配置，一边直接走 legacyPolicy，一边先用 DeriveGroupState 派生出矩阵状态、
// 做成分组快照，再走 matrixPolicy，在「分组 x 模型」的网格上逐点比较每一个 GroupPolicy 方法。
// 这样验证的是整条链路：派生规则 + 快照编译 + 查找语义，任何一环与 legacy 不一致都会在网格上暴露。
//
// 已知并且有意保留的差异（测试里按类别比较，不是漏测）：
//   - ChannelID：legacy 返回渠道 ID，v2 恒为 0（设计 2.3）；
//   - Stage：legacy 恒为 legacy，这里的快照是 v2；
//   - 价格全空的 token 条目：legacy 返回「条目存在但没有价」，v2 的 inherit 是 nil（或在敏感时是空 custom），
//     两者解析出的价格相同，只是 PricingSource 标签不同（channel / litellm）；
//   - web_search_emulation 与 bedrock_cc_compat：legacy 有渠道时恒返回非 nil 的 *bool，v2 在开关不存在时返回 nil；
//     调用方对 nil 与 false 一视同仁（见 Feature 的三个调用点），所以比较生效值；
//   - codex_image_generation_bridge：派生把它归一成「openai 键的 bool」，所以只比较 openai 平台
//     （只有 OpenAI 网关会读它）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	mqGroupOpenEmpty  = int64(60) // 开放分组：价格全空的条目（字面名优先、通配符、图片、区间）
	mqGroupAllowEmpty = int64(61) // 白名单分组 + 计费来源 requested + 映射（含空目标）
	mqGroupAnthropic  = int64(62) // Anthropic 分组：白名单 + upstream + claude 点号写法
	mqGroupRules      = int64(63) // 多条成本核算规则
	mqGroupDisabled   = int64(64) // 渠道已停用
	mqGroupNone       = int64(65) // 不属于任何渠道
)

var mqGroups = []int64{mqGroupOpenEmpty, mqGroupAllowEmpty, mqGroupAnthropic, mqGroupRules, mqGroupDisabled, mqGroupNone}

var mqProbeModels = []string{
	"",
	"gpt-5.6-luna", "gpt-5.6-luna-high", "gpt-5.6-luna-xhigh", "GPT-5.6-LUNA-HIGH", "  gpt-5.6-luna-high  ", "gpt-5.6-luna-2026-08-01",
	"gpt-5.6-sol", "gpt-5.6-terra",
	"gpt-5.4", "gpt-5.4-high", "gpt-5.4-mini", "gpt-5.4-nano",
	"gpt-5.2", "gpt-5.2-mini", "gpt-5.2-mini-high", "gpt-5.2-nano",
	"gpt-5.5", "gpt-5.5-high", "gpt-5.3-codex",
	"gpt-image-2", "unknown-model",
	"claude-sonnet-4.5", "claude-sonnet-4-5", "claude-opus-4.5", "claude-opus-4-5", "Claude-Opus-4.1",
}

func mqEmptyToken(platform string, models ...string) ChannelModelPricing {
	return ChannelModelPricing{Platform: platform, BillingMode: BillingModeToken, Models: models}
}

func mqChannels() ([]Channel, map[int64]string) {
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: mpInt(1000), TierLabel: "short", InputPrice: mpF(1e-6), OutputPrice: mpF(4e-6), SortOrder: 0},
		{MinTokens: 1000, TierLabel: "long", InputPrice: mpF(2e-6), OutputPrice: mpF(8e-6), SortOrder: 1},
	}
	channels := []Channel{
		{
			ID: 11, Name: "open-empty", Status: StatusActive,
			BillingModelSource: BillingModelSourceChannelMapped,
			GroupIDs:           []int64{mqGroupOpenEmpty},
			ModelPricing: []ChannelModelPricing{
				gpTokenPricing(PlatformOpenAI, 0.4e-6, "gpt-5.6-luna"),
				mqEmptyToken(PlatformOpenAI, "gpt-5.6-luna-high", "gpt-5.4"),
				gpTokenPricing(PlatformOpenAI, 0.3e-6, "gpt-5.2*"),
				mqEmptyToken(PlatformOpenAI, "gpt-5.2-mini"),
				{Platform: PlatformOpenAI, Models: []string{"gpt-image-2"}, BillingMode: BillingModeImage},
				{Platform: PlatformOpenAI, Models: []string{"gpt-5.5"}, BillingMode: BillingModeToken, Intervals: intervals},
			},
		},
		{
			ID: 12, Name: "allow-empty", Status: StatusActive,
			BillingModelSource: BillingModelSourceRequested, RestrictModels: true,
			GroupIDs: []int64{mqGroupAllowEmpty},
			ModelPricing: []ChannelModelPricing{
				mqEmptyToken(PlatformOpenAI, "gpt-5.6-luna"),
				mqEmptyToken(PlatformOpenAI, "gpt-5.4*"),
				gpTokenPricing(PlatformOpenAI, 0.9e-6, "gpt-5.5"),
				gpTokenPricing(PlatformAnthropic, 3e-6, "claude-sonnet-4.5"), // 平台不符：两边都只在本平台内查找
			},
			ModelMapping: map[string]map[string]string{
				PlatformOpenAI: {"gpt-5.3-codex": "gpt-5.5", "gpt-5.6*": "gpt-5.6-luna", "gpt-5.6-sol": ""},
			},
		},
		{
			ID: 13, Name: "anthropic", Status: StatusActive,
			BillingModelSource: BillingModelSourceUpstream, RestrictModels: true,
			GroupIDs: []int64{mqGroupAnthropic},
			ModelPricing: []ChannelModelPricing{
				mqEmptyToken(PlatformAnthropic, "claude-sonnet-4.5"),
				gpTokenPricing(PlatformAnthropic, 15e-6, "claude-opus-4*"),
			},
			ModelMapping: map[string]map[string]string{
				PlatformAnthropic: {"claude-opus-4-5": "claude-sonnet-4-5"},
				PlatformOpenAI:    {"gpt-5.5": "gpt-5.4"}, // 不属于分组平台，没有运行时效果
			},
			FeaturesConfig: map[string]any{
				featureKeyWebSearchEmulation: map[string]any{PlatformAnthropic: true},
				featureKeyBedrockCCCompat:    true,
			},
		},
		{
			ID: 14, Name: "rules", Status: StatusActive,
			BillingModelSource: BillingModelSourceChannelMapped,
			GroupIDs:           []int64{mqGroupRules},
			ModelPricing:       []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.4e-6, "gpt-5.6-luna")},
			AccountStatsPricingRules: []AccountStatsPricingRule{
				{ID: 3, ChannelID: 14, Name: "r3", SortOrder: 1, AccountIDs: []int64{7},
					Pricing: []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.003, "gpt-5.4*")}},
				{ID: 4, ChannelID: 14, Name: "r4", SortOrder: 1, AccountIDs: []int64{7},
					Pricing: []ChannelModelPricing{gpTokenPricing("", 0.004, "gpt-5.4-mini"), gpTokenPricing("", 0.005, "gpt-5.6-luna")}},
				{ID: 5, ChannelID: 14, Name: "r5", SortOrder: 2, GroupIDs: []int64{mqGroupRules},
					Pricing: []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.006, "gpt-5.6-luna")}},
				{ID: 6, ChannelID: 14, Name: "r6", SortOrder: 3, // 账号与分组都为空：永不匹配
					Pricing: []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.007, "gpt-5.5")}},
			},
		},
		{
			ID: 15, Name: "disabled", Status: StatusDisabled,
			BillingModelSource: BillingModelSourceUpstream, RestrictModels: true, ApplyPricingToAccountStats: true,
			GroupIDs:     []int64{mqGroupDisabled},
			ModelPricing: []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.4e-6, "gpt-5.6-luna")},
			ModelMapping: map[string]map[string]string{PlatformOpenAI: {"gpt-5.5": "gpt-5.6-luna"}},
			FeaturesConfig: map[string]any{
				featureKeyBedrockCCCompat:            true,
				featureKeyCodexImageGenerationBridge: map[string]any{PlatformOpenAI: true},
			},
		},
	}
	platforms := map[int64]string{
		mqGroupOpenEmpty: PlatformOpenAI, mqGroupAllowEmpty: PlatformOpenAI, mqGroupAnthropic: PlatformAnthropic,
		mqGroupRules: PlatformOpenAI, mqGroupDisabled: PlatformOpenAI, mqGroupNone: PlatformOpenAI,
	}
	return channels, platforms
}

func mpInt(v int) *int { return &v }

func mqFixture() *ChannelService {
	channels, platforms := mqChannels()
	cs := &ChannelService{}
	cs.cache.Store(populateChannelCache(channels, platforms))
	return cs
}

// mqSnapshot 把派生结果做成库里的现状（分组 id 要落到每一行上，快照编译时按它过滤成本核算规则）。
func mqSnapshot(d DerivedGroupState, stage PricingStage) GroupStateSnapshot {
	snap := GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: d.GroupID, MatrixGroupConfig: d.Config, PricingStage: stage, Revision: 1}}
	for i, c := range d.Cells {
		snap.Cells = append(snap.Cells, StoredMatrixCell{ID: int64(1000 + i), GroupID: d.GroupID, Revision: 1, MatrixCell: c})
	}
	for i, r := range d.CostRules {
		snap.Rules = append(snap.Rules, StoredMatrixCostRule{ID: int64(2000 + i), ScopeGroupID: d.GroupID, Source: MatrixSourceLegacyDerived, MatrixCostRule: r})
	}
	return snap
}

// mqFactsFn 决定派生时空价条目的官方价事实：mqFactsKnown 给每个模型「有官方价、不是图片模型」，
// 这时不敏感的空价 token 条目派生成 inherit；mqFactsUnknown 一个事实都没有，空价条目一律保持空 custom。
type mqFactsFn func(ch *Channel, platform string) OfficialPriceFacts

func mqFactsKnown(ch *Channel, platform string) OfficialPriceFacts {
	return mxOKFacts(CollectDeriveFactModels(ch, platform)...)
}

func mqFactsUnknown(ch *Channel, platform string) OfficialPriceFacts {
	return NewOfficialPriceFacts(nil, CollectDeriveFactModels(ch, platform))
}

// mqBuildMatrix 用渠道缓存里的渠道与分组平台派生出每个分组的矩阵状态，装成 matrixPolicy。
func mqBuildMatrix(t *testing.T, cs *ChannelService, groups []int64, facts mqFactsFn) (*matrixPolicy, *mpFakeSource) {
	t.Helper()
	cache := cs.cache.Load().(*channelCache)
	src := &mpFakeSource{metas: map[int64]DeriveGroup{}, snaps: map[int64]GroupStateSnapshot{}}
	for _, gid := range groups {
		platform := cache.groupPlatform[gid]
		if platform == "" {
			platform = PlatformOpenAI
		}
		ch := cache.channelByGroupID[gid] // 没有渠道时为 nil；停用的渠道也在里面，派生按「无渠道」处理
		derived := DeriveGroupState(ch, DeriveGroup{ID: gid, Platform: platform}, facts(ch, platform))
		src.metas[gid] = DeriveGroup{ID: gid, Platform: platform}
		src.snaps[gid] = mqSnapshot(derived, PricingStageV2)
	}
	p, _ := newMPForTest(src, nil)
	return p, src
}

type mqGridStats struct {
	mapped, unmapped          int
	restricted, allowed       int
	upstreamChecks            int
	overridden                int
	legacyEmptyEntry          int // legacy 返回了「价格全空」的条目
	matrixEmptyEntry          int // v2 返回了空 custom（敏感的空价条目）
	nilWhereLegacyEmpty       int // legacy 返回空条目、v2 返回 nil（inherit）
	costHits, costMisses      int
	featuresSet, featuresNone int
}

func mqEffective(v *bool) bool { return v != nil && *v }

func mqPriceSig(p *ChannelModelPricing) MatrixCustomPrice { return MatrixCustomPriceFromPricing(*p) }

// mqCompareGrid 在 分组 x 模型 的网格上逐点比较 legacyPolicy 与 matrixPolicy。
func mqCompareGrid(t *testing.T, legacy, matrix GroupPolicy, groups []int64, models []string) mqGridStats {
	t.Helper()
	ctx := context.Background()
	var st mqGridStats
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 200}

	for _, gid := range groups {
		// 分组级：UpstreamCheck、CostMode、功能开关。
		wantCheck, wantErr := legacy.UpstreamCheck(ctx, gid)
		gotCheck, gotErr := matrix.UpstreamCheck(ctx, gid)
		require.NoError(t, wantErr)
		require.NoError(t, gotErr)
		require.Equal(t, wantCheck, gotCheck, "UpstreamCheck group=%d", gid)
		if wantCheck {
			st.upstreamChecks++
		}
		require.Equal(t, legacy.CostMode(ctx, gid), matrix.CostMode(ctx, gid), "CostMode group=%d", gid)
		require.Equal(t, PricingStageLegacy, legacy.Stage(ctx, gid))
		require.Equal(t, PricingStageV2, matrix.Stage(ctx, gid))

		for _, platform := range gpPlatforms {
			for _, f := range []GroupFeature{GroupFeatureWebSearchEmulation, GroupFeatureBedrockCCCompat} {
				want, err := legacy.Feature(ctx, gid, platform, f)
				require.NoError(t, err)
				got, err := matrix.Feature(ctx, gid, platform, f)
				require.NoError(t, err)
				require.Equal(t, mqEffective(want), mqEffective(got), "Feature %s group=%d platform=%q", f, gid, platform)
				if mqEffective(want) {
					st.featuresSet++
				} else {
					st.featuresNone++
				}
			}
		}
		wantBridge, err := legacy.Feature(ctx, gid, PlatformOpenAI, GroupFeatureCodexImageGenerationBridge)
		require.NoError(t, err)
		gotBridge, err := matrix.Feature(ctx, gid, PlatformOpenAI, GroupFeatureCodexImageGenerationBridge)
		require.NoError(t, err)
		require.Equal(t, wantBridge, gotBridge, "codex bridge group=%d", gid)

		for _, model := range models {
			// 映射：除渠道 ID 外逐字段相同。
			want := legacy.Mapping(ctx, gid, model)
			want.ChannelID = 0
			require.Equal(t, want, matrix.Mapping(ctx, gid, model), "Mapping group=%d model=%q", gid, model)
			if want.Mapped {
				st.mapped++
			} else {
				st.unmapped++
			}

			// 准入。
			wantAccess := legacy.ModelAccess(ctx, gid, model)
			require.Equal(t, wantAccess, matrix.ModelAccess(ctx, gid, model), "ModelAccess group=%d model=%q", gid, model)
			require.Equal(t, legacy.UpstreamAccess(ctx, gid, model), matrix.UpstreamAccess(ctx, gid, model), "UpstreamAccess group=%d model=%q", gid, model)
			if wantAccess.OK {
				st.allowed++
			} else {
				st.restricted++
			}

			// 价格覆盖：有价的必须逐字段相同；价格全空的条目，v2 可以是 nil（inherit）或同样的空条目。
			wantPrice := legacy.PriceOverride(ctx, gid, model, mpT0)
			gotPrice := matrix.PriceOverride(ctx, gid, model, mpT0)
			switch {
			case wantPrice == nil:
				require.Nil(t, gotPrice, "PriceOverride group=%d model=%q: legacy has no entry", gid, model)
			case matrixPricingPriceEmpty(*wantPrice) && matrixBillingMode(*wantPrice) == BillingModeToken:
				st.legacyEmptyEntry++
				if gotPrice == nil {
					st.nilWhereLegacyEmpty++
				} else {
					st.matrixEmptyEntry++
					require.Equal(t, mqPriceSig(wantPrice), mqPriceSig(gotPrice), "PriceOverride group=%d model=%q", gid, model)
				}
			default:
				st.overridden++
				require.NotNil(t, gotPrice, "PriceOverride group=%d model=%q: legacy has a priced entry", gid, model)
				require.Equal(t, mqPriceSig(wantPrice), mqPriceSig(gotPrice), "PriceOverride group=%d model=%q", gid, model)
			}

			// 额外倍率：派生数据里没有 extra 单元格，两边都是 1。
			require.Equal(t, 1.0, legacy.ExtraMultiplier(ctx, gid, model, mpT0))
			require.Equal(t, 1.0, matrix.ExtraMultiplier(ctx, gid, model, mpT0), "ExtraMultiplier group=%d model=%q", gid, model)

			// 账号成本：整条 resolveAccountStatsCost（规则优先、再看成本模式）的结果相同。
			for _, accountID := range []int64{7, 8} {
				for _, total := range []float64{0, 5} {
					wantCost := resolveAccountStatsCost(ctx, legacy, nil, accountID, gid, model, tokens, 1, total, "", time.Time{})
					gotCost := resolveAccountStatsCost(ctx, matrix, nil, accountID, gid, model, tokens, 1, total, "", time.Time{})
					require.Equal(t, wantCost, gotCost, "account cost group=%d account=%d model=%q total=%v", gid, accountID, model, total)
					if wantCost != nil {
						st.costHits++
					} else {
						st.costMisses++
					}
				}
			}
		}
	}
	return st
}

// mqResolvedFields 取出价格解析结果里与价格有关的字段。不含 Source：价格全空的 legacy 条目解析出的来源是 channel，
// v2 的 inherit 是 litellm / fallback，价格相同、标签不同。
func mqResolvedFields(r *ResolvedPricing) any {
	var base ModelPricing
	hasBase := r.BasePricing != nil
	if hasBase {
		base = *r.BasePricing
	}
	return struct {
		Mode        BillingMode
		HasBase     bool
		Base        ModelPricing
		Intervals   []PricingInterval
		Tiers       []PricingInterval
		PerRequest  float64
		CacheBreakd bool
	}{r.Mode, hasBase, base, r.Intervals, r.RequestTiers, r.DefaultPerRequestPrice, r.SupportsCacheBreakdown}
}

type mqFixtureCase struct {
	name   string
	cs     *ChannelService
	groups []int64
	models []string
}

func mqFixtureCases() []mqFixtureCase {
	return []mqFixtureCase{
		{name: "PR3 fixture", cs: newGroupPolicyFixture(), groups: gpGroups, models: gpProbeModels},
		{name: "empty-priced entries, wildcards, mappings, rules", cs: mqFixture(), groups: mqGroups, models: mqProbeModels},
	}
}

func TestMatrixPolicy_EquivalentToLegacyPolicy(t *testing.T) {
	factVariants := []struct {
		name  string
		facts mqFactsFn
	}{
		{"facts known", mqFactsKnown},
		{"facts unknown", mqFactsUnknown},
	}
	for _, fx := range mqFixtureCases() {
		for _, fv := range factVariants {
			t.Run(fx.name+"/"+fv.name, func(t *testing.T) {
				matrix, _ := mqBuildMatrix(t, fx.cs, fx.groups, fv.facts)
				st := mqCompareGrid(t, newLegacyGroupPolicy(fx.cs), matrix, fx.groups, fx.models)

				// 网格不能是空壳：每一类结果都要出现。
				require.NotZero(t, st.mapped, "the grid must contain mapped models")
				require.NotZero(t, st.unmapped)
				require.NotZero(t, st.restricted, "the grid must contain restricted models")
				require.NotZero(t, st.allowed)
				require.NotZero(t, st.upstreamChecks)
				require.NotZero(t, st.overridden)
				require.NotZero(t, st.costHits)
				require.NotZero(t, st.costMisses)
				require.NotZero(t, st.featuresSet)
				require.NotZero(t, st.featuresNone)
			})
		}
	}
}

// 空价条目：知道官方价事实时派生成 inherit（v2 返回 nil），不知道时保持空 custom（v2 返回空条目）。
// 两种情况解析出的价格都与 legacy 相同；上面的网格已经逐点比较过，这里把两种形态各锁一次。
func TestMatrixPolicy_EmptyPricedEntryShapes(t *testing.T) {
	ctx := context.Background()
	cs := mqFixture()
	legacy := newLegacyGroupPolicy(cs)

	known, _ := mqBuildMatrix(t, cs, mqGroups, mqFactsKnown)
	st := mqCompareGrid(t, legacy, known, mqGroups, mqProbeModels)
	require.NotZero(t, st.legacyEmptyEntry)
	require.NotZero(t, st.nilWhereLegacyEmpty, "with known facts empty-priced token entries become inherit")

	unknown, _ := mqBuildMatrix(t, cs, mqGroups, mqFactsUnknown)
	st = mqCompareGrid(t, legacy, unknown, mqGroups, mqProbeModels)
	require.NotZero(t, st.matrixEmptyEntry, "with unknown facts they stay as empty custom cells")

	// 字面名优先：变体名上的空价条目不会被基名的价格接管（legacy 返回空条目并停止，v2 的 inherit 单元格同样停止）。
	legacyHigh := legacy.PriceOverride(ctx, mqGroupOpenEmpty, "gpt-5.6-luna-high", mpT0)
	require.NotNil(t, legacyHigh)
	require.True(t, matrixPricingPriceEmpty(*legacyHigh))
	require.Nil(t, known.PriceOverride(ctx, mqGroupOpenEmpty, "gpt-5.6-luna-high", mpT0))
	// 没有字面名条目的变体回落到基名的价格。
	for _, p := range []GroupPolicy{legacy, known, unknown} {
		got := p.PriceOverride(ctx, mqGroupOpenEmpty, "gpt-5.6-luna-xhigh", mpT0)
		require.NotNil(t, got)
		require.InDelta(t, 0.4e-6, mpInput(t, got), 1e-15)
	}
	// 通配符覆盖下的空价条目：inherit 行必须写下来，否则通配符的价格会接管它。
	legacyMini := legacy.PriceOverride(ctx, mqGroupOpenEmpty, "gpt-5.2-mini", mpT0)
	require.NotNil(t, legacyMini)
	require.True(t, matrixPricingPriceEmpty(*legacyMini))
	require.Nil(t, known.PriceOverride(ctx, mqGroupOpenEmpty, "gpt-5.2-mini", mpT0))
	require.InDelta(t, 0.3e-6, mpInput(t, known.PriceOverride(ctx, mqGroupOpenEmpty, "gpt-5.2-nano", mpT0)), 1e-15)
}

// 价格解析器层面的等价：同一个 ModelPricingResolver，一次用 legacy，一次注入 matrixPolicy（并且证明它没有读渠道服务）。
func TestMatrixPolicy_ResolverPricesMatchLegacy(t *testing.T) {
	ctx := context.Background()
	bs := newTestBillingService()
	for _, fx := range mqFixtureCases() {
		for _, fv := range []struct {
			name  string
			facts mqFactsFn
		}{{"known", mqFactsKnown}, {"unknown", mqFactsUnknown}} {
			t.Run(fx.name+"/"+fv.name, func(t *testing.T) {
				matrix, _ := mqBuildMatrix(t, fx.cs, fx.groups, fv.facts)
				legacyResolver := &ModelPricingResolver{channelService: fx.cs, billingService: bs}
				matrixResolver := &ModelPricingResolver{channelService: gpForbiddenChannelService(t), policyOverride: matrix, billingService: bs}

				var channelPriced int
				for _, gid := range fx.groups {
					for _, model := range fx.models {
						want := legacyResolver.Resolve(ctx, PricingInput{Model: model, GroupID: &gid})
						got := matrixResolver.Resolve(ctx, PricingInput{Model: model, GroupID: &gid})
						require.Equal(t, mqResolvedFields(want), mqResolvedFields(got), "group=%d model=%q", gid, model)
						if want.Source == PricingSourceChannel && got.Source == PricingSourceChannel {
							channelPriced++
						}
					}
				}
				require.NotZero(t, channelPriced, "some resolutions must come from channel prices")
			})
		}
	}
}

// 只靠快照数据（不经过派生）构造出来的分组，同样能被 resolver 正确使用：extra 单元格不改变解析出的单价，
// 倍率由成本函数乘入（见 group_policy_matrix_cost_test.go）。
func TestMatrixPolicy_ExtraCellDoesNotChangeResolvedUnitPrice(t *testing.T) {
	ctx := context.Background()
	bs := newTestBillingService()
	p := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 3)}})
	r := &ModelPricingResolver{channelService: gpForbiddenChannelService(t), policyOverride: p, billingService: bs}
	plain := &ModelPricingResolver{channelService: gpForbiddenChannelService(t), policyOverride: newMPPolicyFor(GroupStateSnapshot{}), billingService: bs}

	gid := int64(1)
	for _, model := range []string{"gpt-5.4", "gpt-5.4-high", "gpt-5.4-mini"} {
		want := plain.Resolve(ctx, PricingInput{Model: model, GroupID: &gid})
		got := r.Resolve(ctx, PricingInput{Model: model, GroupID: &gid})
		require.Equal(t, mqResolvedFields(want), mqResolvedFields(got), model)
		require.Equal(t, want.Source, got.Source, model)
	}
}
