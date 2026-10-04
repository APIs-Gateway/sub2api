//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 夹具辅助
// ---------------------------------------------------------------------------

func mxF(v float64) *float64 { return &v }
func mxI(v int) *int         { return &v }

func mxPricing(id int64, platform string, mode BillingMode, models ...string) ChannelModelPricing {
	return ChannelModelPricing{ID: id, Platform: platform, BillingMode: mode, Models: models}
}

func mxChannel(id int64, groups ...int64) *Channel {
	return &Channel{
		ID:                 id,
		Name:               fmt.Sprintf("ch-%d", id),
		Status:             StatusActive,
		BillingModelSource: BillingModelSourceChannelMapped,
		GroupIDs:           groups,
	}
}

func mxGroup(id int64, platform string) DeriveGroup { return DeriveGroup{ID: id, Platform: platform} }

// mxOKFacts 给每个模型名一份「有官方价、不是图片模型」的事实。
func mxOKFacts(models ...string) OfficialPriceFacts {
	facts := make(OfficialPriceFacts, len(models))
	for _, m := range models {
		facts[officialPriceFactKey(m)] = OfficialPriceFact{HasPrice: true}
	}
	return facts
}

func mxNoteCodes(notes []DerivationNote) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.Code)
	}
	return out
}

func mxFindCell(t *testing.T, cells []MatrixCell, key string, pattern bool) (MatrixCell, bool) {
	t.Helper()
	for _, c := range cells {
		if c.ModelKey == key && c.IsPattern == pattern {
			return c, true
		}
	}
	return MatrixCell{}, false
}

func mxCellKeys(cells []MatrixCell) []string {
	out := make([]string, 0, len(cells))
	for _, c := range cells {
		if c.IsPattern {
			out = append(out, c.ModelKey+"*")
		} else {
			out = append(out, c.ModelKey)
		}
	}
	return out
}

// mxPriceFieldsEmpty 判断 custom_price 里没有任何价格字段（只带 billing_mode）。
func mxPriceFieldsEmpty(p *MatrixCustomPrice) bool {
	if p == nil {
		return false
	}
	return p.InputPrice == nil && p.OutputPrice == nil && p.CacheWritePrice == nil && p.CacheReadPrice == nil &&
		p.ImageOutputPrice == nil && p.PerRequestPrice == nil && len(p.Intervals) == 0
}

// ---------------------------------------------------------------------------
// 金标准：无渠道的几种情形、配置项
// ---------------------------------------------------------------------------

func TestDeriveGroupState_NoChannelStatesYieldDefaults(t *testing.T) {
	ch := mxChannel(1, 10)
	ch.RestrictModels = true
	priced := mxPricing(1, PlatformOpenAI, BillingModeToken, "gpt-5.6-luna")
	priced.InputPrice = mxF(0.000001)
	ch.ModelPricing = []ChannelModelPricing{priced}
	ch.ApplyPricingToAccountStats = true
	ch.AccountStatsPricingRules = []AccountStatsPricingRule{{ID: 1, Name: "r", Pricing: []ChannelModelPricing{priced}}}

	inactive := ch.Clone()
	inactive.Status = StatusDisabled

	cases := []struct {
		name     string
		ch       *Channel
		group    DeriveGroup
		wantNote string
	}{
		{"没有渠道", nil, mxGroup(10, PlatformOpenAI), ""},
		{"渠道停用", inactive, mxGroup(10, PlatformOpenAI), noteChannelInactive},
		{"分组已软删除", ch, DeriveGroup{ID: 10, Platform: PlatformOpenAI, Deleted: true}, noteGroupDeleted},
		{"分组不在渠道里", ch, mxGroup(99, PlatformOpenAI), noteGroupNotInChannel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := DeriveGroupState(c.ch, c.group, nil)
			require.Equal(t, defaultMatrixGroupConfig(), st.Config)
			require.Nil(t, st.Config.BillingModelSource, "无渠道时 billing_model_source 必须是 NULL（S-1）")
			require.Zero(t, st.ChannelID)
			require.Empty(t, st.Cells)
			require.Empty(t, st.CostRules)
			require.NotEmpty(t, st.Revision)
			if c.wantNote == "" {
				require.Empty(t, st.Notes)
			} else {
				require.Contains(t, mxNoteCodes(st.Notes), c.wantNote)
			}
		})
	}
}

func TestDeriveGroupState_ConfigFromChannel(t *testing.T) {
	cases := []struct {
		name         string
		restrict     bool
		billing      string
		applyStats   bool
		wantAccess   MatrixAccessMode
		wantBilling  string
		wantCost     MatrixCostMode
		wantNoteCode string
	}{
		{"开放、空值归一为 channel_mapped、没勾成本", false, "", false, MatrixAccessOpen, BillingModelSourceChannelMapped, MatrixCostCatalogUpstream, ""},
		{"白名单、requested、勾了成本", true, BillingModelSourceRequested, true, MatrixAccessAllowlist, BillingModelSourceRequested, MatrixCostFollowBilling, ""},
		{"upstream", false, BillingModelSourceUpstream, false, MatrixAccessOpen, BillingModelSourceUpstream, MatrixCostCatalogUpstream, ""},
		{"channel_mapped", false, BillingModelSourceChannelMapped, false, MatrixAccessOpen, BillingModelSourceChannelMapped, MatrixCostCatalogUpstream, ""},
		{"未知取值按 channel_mapped 处理并留备注", false, "weird", false, MatrixAccessOpen, BillingModelSourceChannelMapped, MatrixCostCatalogUpstream, noteBillingSourceUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := mxChannel(1, 10)
			ch.RestrictModels = c.restrict
			ch.BillingModelSource = c.billing
			ch.ApplyPricingToAccountStats = c.applyStats
			st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)

			require.Equal(t, int64(1), st.ChannelID)
			require.Equal(t, c.wantAccess, st.Config.AccessMode)
			require.NotNil(t, st.Config.BillingModelSource)
			require.Equal(t, c.wantBilling, *st.Config.BillingModelSource)
			require.Equal(t, c.wantCost, st.Config.CostMode)
			require.Equal(t, []MatrixMappingEntry{}, st.Config.ModelMapping)
			require.Equal(t, map[string]any{}, st.Config.Features)
			if c.wantNoteCode != "" {
				require.Contains(t, mxNoteCodes(st.Notes), c.wantNoteCode)
			}
			// 纯函数：不修改传入的渠道（尤其不能就地归一化 BillingModelSource）。
			require.Equal(t, c.billing, ch.BillingModelSource)
		})
	}
}

func TestDeriveGroupState_DoesNotMutateInput(t *testing.T) {
	ch := mxChannel(1, 10)
	ch.BillingModelSource = ""
	ch.ModelMapping = map[string]map[string]string{PlatformOpenAI: {"B*": "x", "a": "y"}}
	ch.FeaturesConfig = map[string]any{featureKeyWebSearchEmulation: map[string]any{"openai": true, "bad": "x"}}
	p := mxPricing(1, PlatformOpenAI, "", "gpt-5.6-luna", "gpt-5.5*")
	p.Intervals = []PricingInterval{{MinTokens: 0, InputPrice: mxF(1)}}
	ch.ModelPricing = []ChannelModelPricing{p}
	ch.AccountStatsPricingRules = []AccountStatsPricingRule{
		{ID: 2, SortOrder: 5, Pricing: []ChannelModelPricing{p}},
		{ID: 1, SortOrder: 1, Pricing: []ChannelModelPricing{p}},
	}
	before := ch.Clone()

	_ = DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
	require.Equal(t, before, ch)
}

// ---------------------------------------------------------------------------
// 金标准：定价条目 → 单元格
// ---------------------------------------------------------------------------

func TestDeriveGroupState_PricedEntryBecomesCustomCell(t *testing.T) {
	ch := mxChannel(1, 10)
	p := mxPricing(5, PlatformOpenAI, "", " GPT-5.6-Luna ") // 计费模式为空 → token；名字要归一化
	p.InputPrice = mxF(0.000001)
	p.OutputPrice = mxF(0.000004)
	p.CacheReadPrice = mxF(0.0000001)
	p.ImageOutputPrice = mxF(0)
	p.Intervals = []PricingInterval{
		{ID: 9, PricingID: 5, MinTokens: 0, MaxTokens: mxI(200000), InputPrice: mxF(0.000001), SortOrder: 0},
		{ID: 10, PricingID: 5, MinTokens: 200000, InputPrice: mxF(0.000002), OutputPrice: mxF(0.000008), SortOrder: 1},
	}
	ch.ModelPricing = []ChannelModelPricing{p}

	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
	require.Len(t, st.Cells, 1)
	c := st.Cells[0]
	require.Equal(t, "gpt-5.6-luna", c.ModelKey)
	require.False(t, c.IsPattern)
	require.True(t, c.Open)
	require.Equal(t, MatrixPriceCustom, c.PriceMode)
	require.Nil(t, c.ExtraMultiplier, "迁移不产生 extra 单元格")
	require.Equal(t, MatrixSourceLegacyDerived, c.Source)
	require.NotNil(t, c.CustomPrice)
	require.Equal(t, BillingModeToken, c.CustomPrice.BillingMode)
	require.Equal(t, mxF(0.000001), c.CustomPrice.InputPrice)
	require.Equal(t, mxF(0), c.CustomPrice.ImageOutputPrice, "显式 0 必须保留（表示免费）")
	require.Len(t, c.CustomPrice.Intervals, 2)
	require.Equal(t, mxI(200000), c.CustomPrice.Intervals[0].MaxTokens)
	require.Equal(t, 1, c.CustomPrice.Intervals[1].SortOrder)

	// 还原成渠道定价条目后，价格字段与区间与原条目逐项相同（零差异的构造性保证）。
	back := c.CustomPrice.ToChannelModelPricing(PlatformOpenAI, []string{"gpt-5.6-luna"})
	require.Equal(t, MatrixCustomPriceFromPricing(p), MatrixCustomPriceFromPricing(back))
	require.Equal(t, p.InputPrice, back.InputPrice)
	require.Equal(t, BillingModeToken, back.BillingMode)
	require.Len(t, back.Intervals, 2)
	require.Equal(t, p.Intervals[1].OutputPrice, back.Intervals[1].OutputPrice)

	// JSON 往返：custom_price 写进 jsonb 再读出来不变。
	raw, err := json.Marshal(c.CustomPrice)
	require.NoError(t, err)
	var decoded MatrixCustomPrice
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, *c.CustomPrice, decoded)
}

func TestDeriveGroupState_ImageEntryWithPricedIntervalsIsCustom(t *testing.T) {
	// 线上 image 条目的形状：主表价格全空，三个区间各带 per_request_price。
	ch := mxChannel(1, 10)
	p := mxPricing(28, PlatformOpenAI, BillingModeImage, "gpt-image-2", "gpt-image-2.5-flare")
	p.Intervals = []PricingInterval{
		{TierLabel: "1K", PerRequestPrice: mxF(1.8), SortOrder: 0},
		{TierLabel: "2K", PerRequestPrice: mxF(1.8), SortOrder: 1},
		{TierLabel: "4K", PerRequestPrice: mxF(1.8), SortOrder: 2},
	}
	ch.ModelPricing = []ChannelModelPricing{p}
	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)

	require.Equal(t, []string{"gpt-image-2", "gpt-image-2.5-flare"}, mxCellKeys(st.Cells))
	for _, c := range st.Cells {
		require.Equal(t, MatrixPriceCustom, c.PriceMode)
		require.Equal(t, BillingModeImage, c.CustomPrice.BillingMode)
		require.Len(t, c.CustomPrice.Intervals, 3)
		require.Equal(t, "2K", c.CustomPrice.Intervals[1].TierLabel)
		require.Equal(t, mxF(1.8), c.CustomPrice.Intervals[2].PerRequestPrice)
	}
}

func TestDeriveGroupState_EmptyRequestModeEntriesStayEmptyCustom(t *testing.T) {
	ch := mxChannel(1, 10)
	perRequest := mxPricing(1, PlatformOpenAI, BillingModePerRequest, "model-a")
	image := mxPricing(2, PlatformOpenAI, BillingModeImage, "model-b")
	// 区间存在但全空：也算价格全空。
	image.Intervals = []PricingInterval{{TierLabel: "1K"}}
	ch.ModelPricing = []ChannelModelPricing{perRequest, image}
	// 即使事实表明「不敏感」，per_request / image 条目也必须保持空 custom（现状按 0 元出图）。
	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), mxOKFacts("model-a", "model-b"))

	a, ok := mxFindCell(t, st.Cells, "model-a", false)
	require.True(t, ok)
	require.Equal(t, MatrixPriceCustom, a.PriceMode)
	require.Equal(t, &MatrixCustomPrice{BillingMode: BillingModePerRequest}, a.CustomPrice)
	b, ok := mxFindCell(t, st.Cells, "model-b", false)
	require.True(t, ok)
	require.Equal(t, &MatrixCustomPrice{BillingMode: BillingModeImage}, b.CustomPrice)
	require.Contains(t, mxNoteCodes(st.Notes), noteEmptyCustomRequestMode)
}

// ---------------------------------------------------------------------------
// 金标准：价格全空的 token 条目分情况派生（BK-1、CHECK_OPUS_3 3.3、3.5）
// ---------------------------------------------------------------------------

func TestDeriveGroupState_EmptyTokenEntrySensitivity(t *testing.T) {
	const model = "gpt-5.6-luna"
	cases := []struct {
		name       string
		platform   string
		model      string
		billing    string
		mapping    map[string]map[string]string
		facts      OfficialPriceFacts
		wantCustom bool
		wantReason string
	}{
		{name: "openai、有官方价、不是图片、无映射 → 不敏感（开放分组不写行）", platform: PlatformOpenAI, model: model, facts: mxOKFacts(model)},
		{name: "分组平台是 anthropic → 敏感", platform: PlatformAnthropic, model: "claude-opus-4-5", facts: mxOKFacts("claude-opus-4-5"), wantCustom: true, wantReason: sensitivePlatform},
		{name: "分组平台是 gemini → 敏感", platform: PlatformGemini, model: "gemini-2.5-pro", facts: mxOKFacts("gemini-2.5-pro"), wantCustom: true, wantReason: sensitivePlatform},
		{name: "DeepSeek 系列 → 敏感", platform: PlatformOpenAI, model: "deepseek-v4-flash", facts: mxOKFacts("deepseek-v4-flash"), wantCustom: true, wantReason: sensitiveDeepSeek},
		{name: "DeepSeek 名字里带前缀也算（宽松包含判断）", platform: PlatformOpenAI, model: "vendor/DeepSeek-v4", facts: mxOKFacts("vendor/DeepSeek-v4"), wantCustom: true, wantReason: sensitiveDeepSeek},
		{name: "官方价里是图片模型 → 敏感", platform: PlatformOpenAI, model: "gpt-image-2.5-flare", facts: OfficialPriceFacts{"gpt-image-2.5-flare": {HasPrice: true, ImageCapable: true}}, wantCustom: true, wantReason: sensitiveImage},
		{name: "事实里没有这个模型 → 敏感（拿不准一律敏感）", platform: PlatformOpenAI, model: model, facts: OfficialPriceFacts{}, wantCustom: true, wantReason: sensitiveUnknown},
		{name: "事实为 nil → 敏感", platform: PlatformOpenAI, model: model, facts: nil, wantCustom: true, wantReason: sensitiveUnknown},
		{name: "官方价里没有这个模型 → 敏感（保持空 custom，CHECK_OPUS_3 3.5）", platform: PlatformOpenAI, model: model, facts: OfficialPriceFacts{model: {HasPrice: false}}, wantCustom: true, wantReason: sensitiveNoPrice},
		{name: "计费来源 requested → 敏感（图片选路看首个候选，CHECK_OPUS_3 3.3）", platform: PlatformOpenAI, model: model, billing: BillingModelSourceRequested, facts: mxOKFacts(model), wantCustom: true, wantReason: sensitiveRouting},
		{name: "渠道对该平台有映射 → 敏感", platform: PlatformOpenAI, model: model, mapping: map[string]map[string]string{PlatformOpenAI: {"a": "b"}}, facts: mxOKFacts(model), wantCustom: true, wantReason: sensitiveRouting},
		{name: "只有别的平台的映射 → 不影响本平台", platform: PlatformOpenAI, model: model, mapping: map[string]map[string]string{PlatformAnthropic: {"a": "b"}}, facts: mxOKFacts(model)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := mxChannel(1, 10)
			if c.billing != "" {
				ch.BillingModelSource = c.billing
			}
			ch.ModelMapping = c.mapping
			ch.ModelPricing = []ChannelModelPricing{mxPricing(1, c.platform, BillingModeToken, c.model)}

			// 开放分组
			st := DeriveGroupState(ch, mxGroup(10, c.platform), c.facts)
			key := normalizeChannelPricingModelName(c.model)
			cell, found := mxFindCell(t, st.Cells, key, false)
			if c.wantCustom {
				require.True(t, found, "敏感条目必须写出空 custom 单元格")
				require.Equal(t, MatrixPriceCustom, cell.PriceMode)
				require.Equal(t, &MatrixCustomPrice{BillingMode: BillingModeToken}, cell.CustomPrice)
				var reasons []string
				for _, n := range st.Notes {
					if n.Code == noteEmptyCustomSensitive {
						reasons = append(reasons, n.Message)
					}
				}
				require.Len(t, reasons, 1)
				require.Contains(t, reasons[0], c.wantReason)
			} else {
				require.False(t, found, "开放分组里不敏感的空价 token 条目不写行")
				require.Contains(t, mxNoteCodes(st.Notes), noteInheritRowsOmitted)
			}

			// 白名单分组：不敏感的写 open=true 加 inherit，敏感的仍是空 custom
			ch.RestrictModels = true
			st = DeriveGroupState(ch, mxGroup(10, c.platform), c.facts)
			cell, found = mxFindCell(t, st.Cells, key, false)
			require.True(t, found, "白名单分组必须写行，否则会被挡")
			require.True(t, cell.Open)
			if c.wantCustom {
				require.Equal(t, MatrixPriceCustom, cell.PriceMode)
			} else {
				require.Equal(t, MatrixPriceInherit, cell.PriceMode)
				require.Nil(t, cell.CustomPrice)
			}
		})
	}
}

func TestDeriveGroupState_LiteralNamePriorityKeepsInheritRow(t *testing.T) {
	// 前置条件：变体名会被归一化到基名（与 resolver 第二步一致）。
	require.Equal(t, "gpt-5.6-luna", normalizeKnownOpenAICodexModel("gpt-5.6-luna-high"))

	const base, variant, other = "gpt-5.6-luna", "gpt-5.6-luna-high", "gpt-5.4-mini"
	t.Run("带价的基名 + 价格全空的变体：变体必须显式写 inherit，否则会被基名的价格接管", func(t *testing.T) {
		ch := mxChannel(1, 10)
		priced := mxPricing(1, PlatformOpenAI, BillingModeToken, base)
		priced.InputPrice = mxF(0.000002)
		ch.ModelPricing = []ChannelModelPricing{priced, mxPricing(2, PlatformOpenAI, BillingModeToken, variant, other)}
		st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), mxOKFacts(variant, other))

		v, ok := mxFindCell(t, st.Cells, variant, false)
		require.True(t, ok)
		require.Equal(t, MatrixPriceInherit, v.PriceMode)
		require.True(t, v.Open)
		_, ok = mxFindCell(t, st.Cells, other, false)
		require.False(t, ok, "没有 custom 单元格会接管的名字照旧不写行")
		require.Contains(t, mxNoteCodes(st.Notes), noteInheritRowKept)
	})
	t.Run("带价的通配符 + 价格全空的精确名：精确名显式写 inherit（字面名先于通配符）", func(t *testing.T) {
		ch := mxChannel(1, 10)
		wild := mxPricing(1, PlatformOpenAI, BillingModeToken, "gpt-5*")
		wild.OutputPrice = mxF(0.00001)
		ch.ModelPricing = []ChannelModelPricing{wild, mxPricing(2, PlatformOpenAI, BillingModeToken, base, other, "o3")}
		st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), mxOKFacts(base, other, "o3"))

		for _, key := range []string{base, other} {
			c, ok := mxFindCell(t, st.Cells, key, false)
			require.True(t, ok, key)
			require.Equal(t, MatrixPriceInherit, c.PriceMode, key)
		}
		_, ok := mxFindCell(t, st.Cells, "o3", false)
		require.False(t, ok, "通配符前缀 gpt-5 匹配不到 o3，不需要行")
		w, ok := mxFindCell(t, st.Cells, "gpt-5", true)
		require.True(t, ok)
		require.Equal(t, MatrixPriceCustom, w.PriceMode)
	})
	t.Run("价格全空的基名 + 带价的变体：基名不会被变体接管，照旧不写行", func(t *testing.T) {
		ch := mxChannel(1, 10)
		priced := mxPricing(1, PlatformOpenAI, BillingModeToken, variant)
		priced.InputPrice = mxF(0.000002)
		ch.ModelPricing = []ChannelModelPricing{priced, mxPricing(2, PlatformOpenAI, BillingModeToken, base)}
		st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), mxOKFacts(base))
		_, ok := mxFindCell(t, st.Cells, base, false)
		require.False(t, ok)
	})
}

// ---------------------------------------------------------------------------
// 金标准：精确名与通配符的去重、归一化、顺序
// ---------------------------------------------------------------------------

func TestDeriveGroupState_ExactNamesNormalizedAndLastWins(t *testing.T) {
	ch := mxChannel(1, 10)
	first := mxPricing(1, PlatformAnthropic, BillingModeToken, "Claude-Opus-4.5", "claude-sonnet-4.5")
	first.InputPrice = mxF(1)
	second := mxPricing(2, PlatformAnthropic, BillingModeToken, " claude-opus-4-5 ")
	second.InputPrice = mxF(2)
	ch.ModelPricing = []ChannelModelPricing{first, second}

	st := DeriveGroupState(ch, mxGroup(10, PlatformAnthropic), nil)
	require.Equal(t, []string{"claude-opus-4-5", "claude-sonnet-4-5"}, mxCellKeys(st.Cells))
	opus, _ := mxFindCell(t, st.Cells, "claude-opus-4-5", false)
	require.Equal(t, mxF(2), opus.CustomPrice.InputPrice, "同名精确模型后者覆盖前者")
	sonnet, _ := mxFindCell(t, st.Cells, "claude-sonnet-4-5", false)
	require.Equal(t, mxF(1), sonnet.CustomPrice.InputPrice)
	require.Contains(t, mxNoteCodes(st.Notes), noteExactDuplicate)
}

func TestDeriveGroupState_WildcardsKeepEncounterOrderAndFirstDuplicateWins(t *testing.T) {
	ch := mxChannel(1, 10)
	a := mxPricing(1, PlatformAnthropic, BillingModeToken, "Claude-Opus-4.5*", "claude-haiku*")
	a.InputPrice = mxF(1)
	b := mxPricing(2, PlatformAnthropic, BillingModeToken, "claude-opus-4-5*", "claude-sonnet*", "*")
	b.InputPrice = mxF(2)
	ch.ModelPricing = []ChannelModelPricing{a, b}

	st := DeriveGroupState(ch, mxGroup(10, PlatformAnthropic), nil)
	var patterns []MatrixCell
	for _, c := range st.Cells {
		if c.IsPattern {
			patterns = append(patterns, c)
		}
	}
	require.Len(t, patterns, 4)
	wantKeys := []string{"claude-opus-4-5", "claude-haiku", "claude-sonnet", ""}
	for i, p := range patterns {
		require.Equal(t, wantKeys[i], p.ModelKey)
		require.Equal(t, i, p.PatternOrder, "pattern_order 按出现顺序从 0 起连续")
		require.Equal(t, MatrixPriceCustom, p.PriceMode)
	}
	require.Equal(t, mxF(1), patterns[0].CustomPrice.InputPrice, "同一前缀重复时 legacy 取第一个")
	require.Contains(t, mxNoteCodes(st.Notes), noteWildcardDuplicate)
	// 通配符单元格排在精确名之后。
	require.True(t, st.Cells[len(st.Cells)-1].IsPattern)
}

func TestDeriveGroupState_EmptyWildcardStaysEmptyCustom(t *testing.T) {
	ch := mxChannel(1, 10)
	ch.ModelPricing = []ChannelModelPricing{mxPricing(1, PlatformOpenAI, BillingModeToken, "gpt-5.6*")}
	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), mxOKFacts())
	require.Len(t, st.Cells, 1)
	require.True(t, st.Cells[0].IsPattern)
	require.Equal(t, MatrixPriceCustom, st.Cells[0].PriceMode)
	require.Equal(t, &MatrixCustomPrice{BillingMode: BillingModeToken}, st.Cells[0].CustomPrice)
	require.Contains(t, mxNoteCodes(st.Notes), noteEmptyCustomWildcard)
}

func TestDeriveGroupState_OtherPlatformEntriesAreNotDerived(t *testing.T) {
	ch := mxChannel(1, 10)
	other := mxPricing(7, PlatformAnthropic, BillingModeToken, "claude-opus-4-5")
	other.InputPrice = mxF(1)
	mine := mxPricing(8, PlatformOpenAI, BillingModeToken, "gpt-5.5")
	mine.InputPrice = mxF(2)
	ch.ModelPricing = []ChannelModelPricing{other, mine}
	ch.ModelMapping = map[string]map[string]string{PlatformAnthropic: {"x": "y"}}

	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
	require.Equal(t, []string{"gpt-5.5"}, mxCellKeys(st.Cells))
	require.Contains(t, mxNoteCodes(st.Notes), notePricingPlatformMismatch)
	require.Contains(t, mxNoteCodes(st.Notes), noteMappingPlatformIgnored)
	require.Empty(t, st.Config.ModelMapping)
}

func TestDeriveGroupState_SkipsEmptyAndOverlongNames(t *testing.T) {
	ch := mxChannel(1, 10)
	long := strings.Repeat("a", matrixModelKeyMaxLen+1)
	p := mxPricing(1, PlatformOpenAI, BillingModeToken, "  ", long, long+"*", "ok")
	p.InputPrice = mxF(1)
	ch.ModelPricing = []ChannelModelPricing{p}
	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
	require.Equal(t, []string{"ok"}, mxCellKeys(st.Cells))
	codes := mxNoteCodes(st.Notes)
	require.Contains(t, codes, noteEmptyModelName)
	require.Contains(t, codes, noteModelKeyTooLong)
}

// ---------------------------------------------------------------------------
// 金标准：映射（PR1b-1 / #1538 之后的确定性规则）
// ---------------------------------------------------------------------------

func TestDeriveMatrixMapping_Golden(t *testing.T) {
	cases := []struct {
		name string
		src  map[string]string
		want []MatrixMappingEntry
	}{
		{"空", nil, []MatrixMappingEntry{}},
		{"精确名在前（按小写键排序），通配符在后", map[string]string{"gpt-5*": "w", "b": "2", "a": "1"},
			[]MatrixMappingEntry{{Src: "a", Dst: "1"}, {Src: "b", Dst: "2"}, {Src: "gpt-5*", Dst: "w"}}},
		{"通配符前缀长者优先，同长按字典序", map[string]string{"gpt-*": "C", "gpt-5.6*": "A", "gpt-5*": "B", "abc*": "D", "abd*": "E", "ab*": "F"},
			[]MatrixMappingEntry{{Src: "gpt-5.6*", Dst: "A"}, {Src: "gpt-5*", Dst: "B"}, {Src: "gpt-*", Dst: "C"}, {Src: "abc*", Dst: "D"}, {Src: "abd*", Dst: "E"}, {Src: "ab*", Dst: "F"}}},
		{"只差大小写的精确名：按原始 src 字节序靠后者覆盖（'f' > 'F'）", map[string]string{"Foo": "upper", "foo": "lower", "fOo": "mixed"},
			[]MatrixMappingEntry{{Src: "foo", Dst: "lower"}}},
		{"只差大小写的精确名：大写在字节序上靠前，小写靠后", map[string]string{"GPT-5": "x", "gpt-5": "y"},
			[]MatrixMappingEntry{{Src: "gpt-5", Dst: "y"}}},
		{"前缀小写后相同的通配符合并，靠后者覆盖并保留它的原始 src", map[string]string{"GPT-5*": "up", "gpt-5*": "low"},
			[]MatrixMappingEntry{{Src: "gpt-5*", Dst: "low"}}},
		{"空 dst 原样保留", map[string]string{"a": "", "b*": ""},
			[]MatrixMappingEntry{{Src: "a", Dst: ""}, {Src: "b*", Dst: ""}}},
		{"精确名里的 * 不在末尾时仍是精确名", map[string]string{"a*b": "x"},
			[]MatrixMappingEntry{{Src: "a*b", Dst: "x"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, deriveMatrixMapping(c.src))
		})
	}
}

// mxMappingLookup 按派生数组的语义查找：精确名（小写）优先，其次按数组顺序第一个匹配的通配符。
func mxMappingLookup(mapping []MatrixMappingEntry, model string) string {
	lower := strings.ToLower(model)
	for _, e := range mapping {
		if !strings.HasSuffix(e.Src, "*") && strings.ToLower(e.Src) == lower {
			return e.Dst
		}
	}
	for _, e := range mapping {
		if strings.HasSuffix(e.Src, "*") && strings.HasPrefix(lower, strings.ToLower(strings.TrimSuffix(e.Src, "*"))) {
			return e.Dst
		}
	}
	return ""
}

func TestDeriveGroupState_MappingMatchesLegacyLookupOnNonOverlappingData(t *testing.T) {
	ch := mxChannel(1, 10)
	ch.ModelMapping = map[string]map[string]string{PlatformOpenAI: {
		"Gpt-Alias":  "gpt-5.5",
		"o3-pro":     "o3",
		"claude-*":   "gpt-5.6-luna",
		"gemini-2*":  "gpt-5.4",
		"deepseek-*": "",
	}}
	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)

	cache := populateChannelCache([]Channel{*ch.Clone()}, map[int64]string{10: PlatformOpenAI})
	for _, probe := range []string{"gpt-alias", "GPT-ALIAS", "o3-pro", "o3", "claude-opus-4-5", "Claude-Sonnet", "gemini-2.5-pro", "gemini-3", "deepseek-v4", "unknown"} {
		want := lookupMappingAcrossPlatforms(cache, 10, PlatformOpenAI, strings.ToLower(probe))
		require.Equal(t, want, mxMappingLookup(st.Config.ModelMapping, probe), probe)
	}
}

func TestDeriveGroupState_ConflictingMappingsBuiltDirectlyInCache(t *testing.T) {
	// 保存接口的 validateNoConflictingMappings 会拦掉冲突映射，所以冲突形态只能直接构造渠道（PR1b-1）。
	conflicting := map[string]string{
		"gpt-5*":   "short",
		"gpt-5.6*": "long",
		"GPT-5.6*": "long-upper",
		"Foo":      "A",
		"foo":      "B",
	}
	require.Error(t, validateNoConflictingMappings(map[string]map[string]string{PlatformOpenAI: conflicting}),
		"前提：这组映射走保存接口会被拒绝")

	ch := mxChannel(1, 10)
	ch.ModelMapping = map[string]map[string]string{PlatformOpenAI: conflicting}
	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
	require.Equal(t, []MatrixMappingEntry{
		{Src: "foo", Dst: "B"},
		{Src: "gpt-5.6*", Dst: "long"},
		{Src: "gpt-5*", Dst: "short"},
	}, st.Config.ModelMapping)
	require.Equal(t, "long", mxMappingLookup(st.Config.ModelMapping, "gpt-5.6-luna"))
	require.Equal(t, "short", mxMappingLookup(st.Config.ModelMapping, "gpt-5.5"))
	require.Equal(t, "B", mxMappingLookup(st.Config.ModelMapping, "FOO"))

	// 直接构造缓存：#1538 之后 legacy 对同一份冲突映射给出同样的确定结果，派生与它逐探针一致。
	cache := populateChannelCache([]Channel{*ch.Clone()}, map[int64]string{10: PlatformOpenAI})
	for _, probe := range []string{"foo", "FOO", "gpt-5.6-luna", "GPT-5.6-LUNA", "gpt-5.5", "gpt-5", "gpt-4", "gpt-5.6"} {
		want := lookupMappingAcrossPlatforms(cache, 10, PlatformOpenAI, strings.ToLower(probe))
		require.Equal(t, want, mxMappingLookup(st.Config.ModelMapping, probe), probe)
	}
}

// ---------------------------------------------------------------------------
// 金标准：功能开关（S-2、Q9）
// ---------------------------------------------------------------------------

func TestDeriveMatrixFeatures_Golden(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{"没有开关", nil, map[string]any{}},
		{"没有相关键", map[string]any{"other": true}, map[string]any{}},
		{"web_search_emulation 按账号平台的 map：只保留 bool 项",
			map[string]any{featureKeyWebSearchEmulation: map[string]any{"openai": true, "anthropic": false, "bad": "yes", "n": 1.0}},
			map[string]any{featureKeyWebSearchEmulation: map[string]any{"openai": true, "anthropic": false}}},
		{"web_search_emulation 裸 bool 视为关，迁成空 map",
			map[string]any{featureKeyWebSearchEmulation: true},
			map[string]any{featureKeyWebSearchEmulation: map[string]any{}}},
		{"bedrock_cc_compat 裸 bool 保留", map[string]any{featureKeyBedrockCCCompat: true}, map[string]any{featureKeyBedrockCCCompat: true}},
		{"bedrock_cc_compat 按平台 map 一律迁成 false",
			map[string]any{featureKeyBedrockCCCompat: map[string]any{"anthropic": true}},
			map[string]any{featureKeyBedrockCCCompat: false}},
		{"codex 桥：裸 bool", map[string]any{featureKeyCodexImageGenerationBridge: true}, map[string]any{featureKeyCodexImageGenerationBridge: true}},
		{"codex 桥：map 里取 openai 键（线上形状）", map[string]any{featureKeyCodexImageGenerationBridge: map[string]any{"openai": false}}, map[string]any{featureKeyCodexImageGenerationBridge: false}},
		{"codex 桥：map 里没有 openai 键 → 不写", map[string]any{featureKeyCodexImageGenerationBridge: map[string]any{"anthropic": true}}, map[string]any{}},
		{"codex 桥：非 bool → 不写", map[string]any{featureKeyCodexImageGenerationBridge: "x"}, map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, deriveMatrixFeatures(c.in))
		})
	}
}

func TestDeriveMatrixFeatures_AgreesWithCurrentReaders(t *testing.T) {
	// 派生值与现有读取函数在各种形状下给出同一个结果。
	shapes := []map[string]any{
		nil,
		{featureKeyWebSearchEmulation: map[string]any{"anthropic": true}},
		{featureKeyWebSearchEmulation: map[string]any{"openai": false, "anthropic": true}},
		{featureKeyWebSearchEmulation: true},
		{featureKeyBedrockCCCompat: true},
		{featureKeyBedrockCCCompat: false},
		{featureKeyBedrockCCCompat: map[string]any{"anthropic": true}},
		{featureKeyCodexImageGenerationBridge: true},
		{featureKeyCodexImageGenerationBridge: map[string]any{"openai": true}},
		{featureKeyCodexImageGenerationBridge: map[string]any{"anthropic": true}},
	}
	for i, shape := range shapes {
		ch := &Channel{FeaturesConfig: shape}
		derived := deriveMatrixFeatures(shape)

		for _, platform := range []string{"openai", "anthropic", "gemini"} {
			wantWSE := ch.IsWebSearchEmulationEnabled(platform)
			gotWSE := false
			if m, ok := derived[featureKeyWebSearchEmulation].(map[string]any); ok {
				gotWSE, _ = m[platform].(bool)
			}
			require.Equal(t, wantWSE, gotWSE, "shape %d web_search_emulation %s", i, platform)

			wantBedrock := ch.IsBedrockCCCompatEnabled(platform)
			gotBedrock, _ := derived[featureKeyBedrockCCCompat].(bool)
			require.Equal(t, wantBedrock, gotBedrock, "shape %d bedrock_cc_compat", i)
		}

		want := ch.CodexImageGenerationBridgeOverride(PlatformOpenAI)
		got, present := derived[featureKeyCodexImageGenerationBridge].(bool)
		if want == nil {
			require.False(t, present, "shape %d codex 桥", i)
		} else {
			require.True(t, present, "shape %d codex 桥", i)
			require.Equal(t, *want, got, "shape %d codex 桥", i)
		}
	}
}

// ---------------------------------------------------------------------------
// 金标准：成本核算规则（R2-BK-1）
// ---------------------------------------------------------------------------

func TestDeriveGroupState_CostRules(t *testing.T) {
	ch := mxChannel(7, 10, 11)
	ch.ApplyPricingToAccountStats = true

	priced := mxPricing(0, PlatformOpenAI, BillingModeToken, "gpt-5.6-*", "gpt-5.5")
	priced.InputPrice = mxF(0.0000011)
	priced.Intervals = []PricingInterval{{MinTokens: 0, MaxTokens: mxI(100000), InputPrice: mxF(0.000001), SortOrder: 0}}
	anyPlatform := mxPricing(0, "", BillingModePerRequest, "img-*")
	anyPlatform.PerRequestPrice = mxF(0.5)

	ch.AccountStatsPricingRules = []AccountStatsPricingRule{
		{ID: 31, ChannelID: 7, Name: "second", GroupIDs: []int64{10}, AccountIDs: nil, SortOrder: 5, Pricing: []ChannelModelPricing{anyPlatform}},
		{ID: 30, ChannelID: 7, Name: "first", GroupIDs: []int64{11, 99}, AccountIDs: []int64{1, 2}, SortOrder: 1, Pricing: []ChannelModelPricing{priced, anyPlatform}},
		{ID: 32, ChannelID: 7, Name: "same-order-later-id", SortOrder: 5},
	}

	for _, gid := range []int64{10, 11} {
		st := DeriveGroupState(ch, mxGroup(gid, PlatformOpenAI), nil)
		require.Equal(t, MatrixCostFollowBilling, st.Config.CostMode)
		require.Len(t, st.CostRules, 3)

		// 按（sort_order, id）排名，从 1 起。
		require.Equal(t, []string{"first", "second", "same-order-later-id"}, []string{st.CostRules[0].Name, st.CostRules[1].Name, st.CostRules[2].Name})
		for i, r := range st.CostRules {
			require.Equal(t, int64(7), r.SourceChannelID)
			require.Equal(t, i+1, r.SourceOrdinal)
			require.True(t, r.Enabled)
			require.NotNil(t, r.GroupIDs)
			require.NotNil(t, r.AccountIDs)
			require.NotNil(t, r.Prices)
		}
		first := st.CostRules[0]
		require.Equal(t, []int64{11, 99}, first.GroupIDs, "规则内的分组命中条件原样复制（包括不属于本渠道的分组）")
		require.Equal(t, []int64{1, 2}, first.AccountIDs)
		require.Equal(t, 1, first.SortOrder)
		require.Len(t, first.Prices, 2)
		require.Equal(t, PlatformOpenAI, first.Prices[0].Platform)
		require.Equal(t, []string{"gpt-5.6-*", "gpt-5.5"}, first.Prices[0].Models, "规则内模型名原样保留，不做渠道定价的归一化")
		require.Equal(t, mxF(0.0000011), first.Prices[0].Price.InputPrice)
		require.Len(t, first.Prices[0].Price.Intervals, 1, "区间并入 price")
		require.Equal(t, "", first.Prices[1].Platform, "platform 为空表示匹配任意平台")
		require.Equal(t, BillingModePerRequest, first.Prices[1].Price.BillingMode)
		require.Empty(t, st.CostRules[2].Prices)
	}

	// 没勾「账号成本」时 cost_mode 是 catalog_upstream，规则照样派生。
	ch.ApplyPricingToAccountStats = false
	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
	require.Equal(t, MatrixCostCatalogUpstream, st.Config.CostMode)
	require.Len(t, st.CostRules, 3)
}

func TestDeriveGroupState_CostRuleNameTruncated(t *testing.T) {
	ch := mxChannel(1, 10)
	ch.AccountStatsPricingRules = []AccountStatsPricingRule{{ID: 1, Name: strings.Repeat("规", matrixRuleNameMaxLen+5)}}
	st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
	require.Len(t, st.CostRules, 1)
	require.Equal(t, matrixRuleNameMaxLen, len([]rune(st.CostRules[0].Name)))
	require.Contains(t, mxNoteCodes(st.Notes), noteRuleNameTruncated)
}

// ---------------------------------------------------------------------------
// 官方价事实、revision
// ---------------------------------------------------------------------------

type mxFactSource struct {
	facts map[string]OfficialPriceFact
	calls []string
}

func (s *mxFactSource) LookupOfficialPriceFact(model string) OfficialPriceFact {
	s.calls = append(s.calls, model)
	return s.facts[strings.ToLower(model)]
}

func TestOfficialPriceFacts(t *testing.T) {
	src := &mxFactSource{facts: map[string]OfficialPriceFact{"a": {HasPrice: true}, "b": {ImageCapable: true}}}
	facts := NewOfficialPriceFacts(src, []string{"A", " a ", "b", "c"})
	require.Equal(t, OfficialPriceFacts{"a": {HasPrice: true}, "b": {ImageCapable: true}, "c": {}}, facts)
	require.Equal(t, []string{"A", "b", "c"}, src.calls, "同一个名字（忽略大小写与空白）只查询一次")
	require.Empty(t, NewOfficialPriceFacts(nil, []string{"a"}))
}

func TestCollectDeriveFactModels(t *testing.T) {
	ch := mxChannel(1, 10)
	priced := mxPricing(1, PlatformOpenAI, BillingModeToken, "priced")
	priced.InputPrice = mxF(1)
	ch.ModelPricing = []ChannelModelPricing{
		priced,
		mxPricing(2, PlatformOpenAI, BillingModeToken, "Zeta", " alpha ", "ALPHA", "wild*", ""),
		mxPricing(3, PlatformOpenAI, BillingModePerRequest, "per-request"),
		mxPricing(4, PlatformAnthropic, BillingModeToken, "other-platform"),
	}
	require.Equal(t, []string{"Zeta", "alpha"}, CollectDeriveFactModels(ch, PlatformOpenAI))
	require.Nil(t, CollectDeriveFactModels(ch, PlatformAnthropic), "非 openai 平台不读事实")
	require.Nil(t, CollectDeriveFactModels(nil, PlatformOpenAI))
}

func TestDeriveGroupState_RevisionTracksStateAndUsedFacts(t *testing.T) {
	ch := mxChannel(1, 10)
	ch.ModelPricing = []ChannelModelPricing{mxPricing(1, PlatformOpenAI, BillingModeToken, "gpt-5.6-luna")}
	group := mxGroup(10, PlatformOpenAI)

	base := DeriveGroupState(ch, group, mxOKFacts("gpt-5.6-luna"))
	require.Equal(t, base.Revision, DeriveGroupState(ch, group, mxOKFacts("gpt-5.6-luna")).Revision, "同样的输入同一个 revision")
	require.Equal(t, base.Revision, DeriveGroupState(ch, group, mxOKFacts("gpt-5.6-luna", "unrelated")).Revision, "没用到的事实不进 revision")

	changedFacts := DeriveGroupState(ch, group, OfficialPriceFacts{"gpt-5.6-luna": {HasPrice: true, ImageCapable: true}})
	require.NotEqual(t, base.Revision, changedFacts.Revision, "价格数据变动导致判定改变时 revision 随之改变")
	unknown := DeriveGroupState(ch, group, nil)
	require.NotEqual(t, base.Revision, unknown.Revision)

	ch2 := ch.Clone()
	ch2.RestrictModels = true
	require.NotEqual(t, base.Revision, DeriveGroupState(ch2, group, mxOKFacts("gpt-5.6-luna")).Revision)
	require.NotEqual(t, base.Revision, DeriveGroupState(nil, group, nil).Revision)
}

// ---------------------------------------------------------------------------
// 属性测试
// ---------------------------------------------------------------------------

func TestMatrixCanonicalJSON_MarshalError(t *testing.T) {
	// NaN 不能序列化：返回的串带错误信息，不会让两侧因为都失败而被当成相等。
	bad := mxF(nanForTest())
	a := matrixCanonicalJSON(MatrixCell{CustomPrice: &MatrixCustomPrice{InputPrice: bad}})
	require.True(t, strings.HasPrefix(a, "!marshal-error"))
}

func nanForTest() float64 {
	zero := 0.0
	return zero / zero
}

func TestDeriveMatrixMapping_DeterministicAcrossMapIterationOrders(t *testing.T) {
	src := map[string]string{}
	for i := 0; i < 40; i++ {
		src[fmt.Sprintf("m%02d*", i%13)] = fmt.Sprintf("t%d", i)
		src[fmt.Sprintf("Exact%02d", i%7)] = fmt.Sprintf("e%d", i)
	}
	want := deriveMatrixMapping(src)
	for i := 0; i < 50; i++ {
		require.Equal(t, want, deriveMatrixMapping(src))
	}
}
