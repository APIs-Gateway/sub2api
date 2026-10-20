//go:build unit

package service

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 属性测试：用固定种子随机生成渠道，检查派生结果的结构不变量，
// 以及它与 legacy 缓存（populateChannelCache）逐探针的等价性。

var (
	mxModelPool = []string{
		"gpt-5.6-luna", "gpt-5.6-luna-high", "gpt-5.5", "gpt-5.5-high", "gpt-5.4-mini", "gpt-5.4-high",
		"o3", " Gpt-5.6-Luna ", "GPT-5.5", "deepseek-v4-flash",
	}
	mxWildcardPool = []string{"gpt-5*", "gpt-5.6*", "gpt-5.6-luna*", "o*", "*", "GPT-5.5*", "deepseek*"}
	mxProbePool    = []string{
		"gpt-5.6-luna", "gpt-5.6-luna-high", "gpt-5.6-luna-xhigh", "GPT-5.6-LUNA", "gpt-5.5", "gpt-5.5-high",
		"gpt-5.5-medium", "gpt-5.4-mini", "gpt-5.4-high", "gpt-5.4", "gpt-5", "o3", "o4-mini", "deepseek-v4-flash",
		"claude-opus-4-5", "unknown-model",
	}
	mxMappingSrcPool = []string{
		"a", "A", "gpt-5*", "GPT-5*", "gpt-5.6*", "gpt-5.6-luna*", "o*", "gpt-5.6-luna", "Gpt-5.6-Luna", "x*", "gpt-5.5",
	}
	mxMappingDstPool = []string{"t1", "t2", "t3", ""}
)

func mxRandomChannel(r *rand.Rand) *Channel {
	ch := mxChannel(1, 10)
	ch.RestrictModels = r.Intn(2) == 0
	ch.BillingModelSource = []string{BillingModelSourceChannelMapped, BillingModelSourceUpstream, BillingModelSourceRequested, ""}[r.Intn(4)]
	ch.ApplyPricingToAccountStats = r.Intn(2) == 0

	n := 1 + r.Intn(5)
	for i := 0; i < n; i++ {
		platform := PlatformOpenAI
		if r.Intn(6) == 0 {
			platform = PlatformAnthropic
		}
		mode := BillingModeToken
		switch r.Intn(10) {
		case 0:
			mode = BillingModePerRequest
		case 1:
			mode = BillingModeImage
		case 2:
			mode = "" // 空计费模式等同 token
		}
		p := mxPricing(int64(i+1), platform, mode)
		for j, models := 0, 1+r.Intn(3); j < models; j++ {
			if r.Intn(3) == 0 {
				p.Models = append(p.Models, mxWildcardPool[r.Intn(len(mxWildcardPool))])
			} else {
				p.Models = append(p.Models, mxModelPool[r.Intn(len(mxModelPool))])
			}
		}
		switch r.Intn(4) {
		case 0: // 价格全空
		case 1:
			p.InputPrice = mxF(float64(1+r.Intn(9)) / 1e6)
		case 2:
			p.OutputPrice = mxF(float64(1+r.Intn(9)) / 1e6)
			p.CacheReadPrice = mxF(0)
		case 3:
			p.Intervals = []PricingInterval{{MinTokens: 0, InputPrice: mxF(float64(1+r.Intn(9)) / 1e6)}}
		}
		ch.ModelPricing = append(ch.ModelPricing, p)
	}

	if r.Intn(3) == 0 {
		mapping := map[string]string{}
		for j, k := 0, 1+r.Intn(4); j < k; j++ {
			mapping[mxMappingSrcPool[r.Intn(len(mxMappingSrcPool))]] = mxMappingDstPool[r.Intn(len(mxMappingDstPool))]
		}
		ch.ModelMapping = map[string]map[string]string{PlatformOpenAI: mapping}
	}
	if r.Intn(4) == 0 {
		for j, k := 0, 1+r.Intn(3); j < k; j++ {
			rule := AccountStatsPricingRule{ID: int64(10 + r.Intn(5)), SortOrder: r.Intn(3), Name: fmt.Sprintf("rule-%d", j)}
			if r.Intn(2) == 0 {
				pr := mxPricing(0, PlatformOpenAI, BillingModeToken, mxModelPool[r.Intn(len(mxModelPool))])
				pr.InputPrice = mxF(1)
				rule.Pricing = []ChannelModelPricing{pr}
			}
			ch.AccountStatsPricingRules = append(ch.AccountStatsPricingRules, rule)
		}
	}
	return ch
}

// mxCellLiteralLookup 单元格的字面名查找：精确名，其次按 pattern_order 第一个命中的通配符。
func mxCellLiteralLookup(cells []MatrixCell, name string) *MatrixCell {
	key := normalizeChannelPricingModelName(name)
	for i := range cells {
		if !cells[i].IsPattern && cells[i].ModelKey == key {
			return &cells[i]
		}
	}
	var best *MatrixCell
	for i := range cells {
		if cells[i].IsPattern && strings.HasPrefix(key, cells[i].ModelKey) {
			if best == nil || cells[i].PatternOrder < best.PatternOrder {
				best = &cells[i]
			}
		}
	}
	return best
}

// mxCellTwoStepLookup 与 legacyPolicy.PriceOverride 一致的两步查找。
func mxCellTwoStepLookup(cells []MatrixCell, name string) *MatrixCell {
	if c := mxCellLiteralLookup(cells, name); c != nil {
		return c
	}
	normalized := normalizeKnownOpenAICodexModel(name)
	if normalized == "" || strings.EqualFold(normalized, strings.TrimSpace(name)) {
		return nil
	}
	return mxCellLiteralLookup(cells, normalized)
}

func mxLegacyLiteral(cache *channelCache, name string) *ChannelModelPricing {
	return lookupPricingAcrossPlatforms(cache, 10, PlatformOpenAI, strings.ToLower(name))
}

func mxLegacyTwoStep(cache *channelCache, name string) *ChannelModelPricing {
	if p := mxLegacyLiteral(cache, name); p != nil {
		return p
	}
	normalized := normalizeKnownOpenAICodexModel(name)
	if normalized == "" || strings.EqualFold(normalized, strings.TrimSpace(name)) {
		return nil
	}
	return mxLegacyLiteral(cache, normalized)
}

func mxFactsForPools() OfficialPriceFacts { return mxOKFacts(mxModelPool...) }

// 属性：对随机渠道，派生出的单元格与 legacy 缓存在每个探针上等价。
func TestDeriveGroupState_PropertyEquivalentToLegacyCache(t *testing.T) {
	r := rand.New(rand.NewSource(20261004))
	const rounds = 1500
	checkedPriced, checkedOmitted, checkedKeptInherit := 0, 0, 0

	for i := 0; i < rounds; i++ {
		ch := mxRandomChannel(r)
		for _, facts := range []OfficialPriceFacts{mxFactsForPools(), nil} {
			st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), facts)
			cache := populateChannelCache([]Channel{*ch.Clone()}, map[int64]string{10: PlatformOpenAI})
			allowlist := st.Config.AccessMode == MatrixAccessAllowlist
			desc := fmt.Sprintf("round %d channel %+v", i, *ch)
			msg := func(probe string) string { return "probe " + probe + " " + desc }

			for _, probe := range mxProbePool {
				legacy := mxLegacyTwoStep(cache, probe)
				cell := mxCellTwoStepLookup(st.Cells, probe)

				switch {
				case legacy == nil:
					require.Nil(t, cell, msg(probe))
				case cell == nil:
					// 只允许是开放分组里价格全空、且派生判为不敏感的 token 条目（省略的 inherit 行）。
					require.False(t, allowlist, "白名单分组必须写行："+msg(probe))
					require.True(t, matrixPricingPriceEmpty(*legacy), msg(probe))
					require.Equal(t, BillingModeToken, matrixBillingMode(*legacy), msg(probe))
					checkedOmitted++
				default:
					require.True(t, cell.Open, msg(probe))
					if !matrixPricingPriceEmpty(*legacy) {
						require.Equal(t, MatrixPriceCustom, cell.PriceMode, msg(probe))
						require.NotNil(t, cell.CustomPrice, msg(probe))
						require.Equal(t, MatrixCustomPriceFromPricing(*legacy), *cell.CustomPrice, "有价条目必须原样带走："+msg(probe))
						checkedPriced++
						break
					}
					if matrixBillingMode(*legacy) != BillingModeToken {
						// 价格全空的按次/图片条目一律保持空 custom（BK-1）。
						require.Equal(t, MatrixPriceCustom, cell.PriceMode, msg(probe))
						require.Equal(t, matrixBillingMode(*legacy), cell.CustomPrice.BillingMode, msg(probe))
						require.True(t, mxPriceFieldsEmpty(cell.CustomPrice), msg(probe))
						break
					}
					switch cell.PriceMode {
					case MatrixPriceInherit:
						require.Nil(t, cell.CustomPrice, msg(probe))
						checkedKeptInherit++
					case MatrixPriceCustom:
						require.True(t, mxPriceFieldsEmpty(cell.CustomPrice), "空价条目的 custom 不能带价："+msg(probe))
					default:
						t.Fatalf("意外的 price_mode %q: %s", cell.PriceMode, msg(probe))
					}
				}

				if allowlist {
					// 白名单分组的准入：legacy 的 checkRestricted 只做字面名查找，派生出的单元格同样如此。
					require.Equal(t, mxLegacyLiteral(cache, probe) == nil, mxCellLiteralLookup(st.Cells, probe) == nil,
						"字面名准入必须一致："+msg(probe))
				}
			}
		}
	}
	// 防止测试退化成空转：随机数据必须真的覆盖到这几类情形。
	require.Positive(t, checkedPriced)
	require.Positive(t, checkedOmitted)
	require.Positive(t, checkedKeptInherit)
}

// 属性：映射派生与 legacy 缓存在每个探针上一致（#1538 之后 legacy 对冲突映射同样确定）。
func TestDeriveGroupState_PropertyMappingEquivalentToLegacyCache(t *testing.T) {
	r := rand.New(rand.NewSource(1538))
	for i := 0; i < 400; i++ {
		src := map[string]string{}
		for j, k := 0, 1+r.Intn(6); j < k; j++ {
			src[mxMappingSrcPool[r.Intn(len(mxMappingSrcPool))]] = mxMappingDstPool[r.Intn(len(mxMappingDstPool))]
		}
		ch := mxChannel(1, 10)
		ch.ModelMapping = map[string]map[string]string{PlatformOpenAI: src}
		st := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
		cache := populateChannelCache([]Channel{*ch.Clone()}, map[int64]string{10: PlatformOpenAI})

		for _, probe := range append([]string{"a", "A", "x1", "o1", "gpt-5.6", "gpt-5.6-luna-high"}, mxProbePool...) {
			want := lookupMappingAcrossPlatforms(cache, 10, PlatformOpenAI, strings.ToLower(probe))
			require.Equal(t, want, mxMappingLookup(st.Config.ModelMapping, probe), "round %d probe %q src %v", i, probe, src)
		}

		// 输出本身满足排序规则：精确名在前；通配符前缀长者在前，同长按字典序；小写键唯一。
		seenExact := map[string]bool{}
		seenPrefix := map[string]bool{}
		inWildcards := false
		prevPrefix := ""
		for _, e := range st.Config.ModelMapping {
			if strings.HasSuffix(e.Src, "*") {
				prefix := strings.ToLower(strings.TrimSuffix(e.Src, "*"))
				require.False(t, seenPrefix[prefix], "通配符前缀（小写）必须唯一")
				seenPrefix[prefix] = true
				if inWildcards {
					require.True(t, len(prevPrefix) > len(prefix) || (len(prevPrefix) == len(prefix) && prevPrefix < prefix),
						"通配符顺序：前缀长者在前，同长按字典序")
				}
				inWildcards = true
				prevPrefix = prefix
				continue
			}
			require.False(t, inWildcards, "精确名必须全部排在通配符之前")
			key := strings.ToLower(e.Src)
			require.False(t, seenExact[key], "精确名（小写）必须唯一")
			seenExact[key] = true
		}
	}
}

// 属性：派生是确定的，结构不变量成立，且不修改输入。
func TestDeriveGroupState_PropertyStructuralInvariants(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 400; i++ {
		ch := mxRandomChannel(r)
		before := ch.Clone()
		facts := mxFactsForPools()
		group := mxGroup(10, PlatformOpenAI)

		a := DeriveGroupState(ch, group, facts)
		b := DeriveGroupState(ch, group, facts)
		require.Equal(t, a, b, "同样的输入必须得到同样的结果")
		require.Equal(t, before, ch, "不修改传入的渠道")
		require.NotEmpty(t, a.Revision)

		seen := map[matrixCellKey]bool{}
		sawPattern := false
		patternOrder := 0
		prevExact := ""
		for _, c := range a.Cells {
			k := matrixCellKey{c.ModelKey, c.IsPattern}
			require.False(t, seen[k], "单元格键 (model_key, is_pattern) 必须唯一")
			seen[k] = true
			require.True(t, c.Open, "派生只写 open=true 的行")
			require.Equal(t, MatrixSourceLegacyDerived, c.Source)
			require.LessOrEqual(t, len(c.ModelKey), matrixModelKeyMaxLen)
			require.Equal(t, normalizeChannelPricingModelName(c.ModelKey), c.ModelKey, "model_key 已归一化")

			if c.IsPattern {
				sawPattern = true
				require.Equal(t, patternOrder, c.PatternOrder, "pattern_order 从 0 起连续")
				patternOrder++
				require.Equal(t, MatrixPriceCustom, c.PriceMode, "通配符一律是 custom")
			} else {
				require.False(t, sawPattern, "精确名排在通配符之前")
				require.Less(t, prevExact, c.ModelKey, "精确名按 model_key 升序")
				prevExact = c.ModelKey
				require.NotEqual(t, "", c.ModelKey)
			}

			switch c.PriceMode {
			case MatrixPriceCustom:
				require.NotNil(t, c.CustomPrice)
				require.Nil(t, c.ExtraMultiplier)
				require.NotEqual(t, BillingMode(""), c.CustomPrice.BillingMode, "custom_price 的 billing_mode 已规范化")
			case MatrixPriceInherit:
				require.Nil(t, c.CustomPrice)
				require.Nil(t, c.ExtraMultiplier)
				require.False(t, c.IsPattern)
			default:
				t.Fatalf("派生不应产生 %q 单元格", c.PriceMode)
			}
		}

		require.NotNil(t, a.Config.ModelMapping)
		require.NotNil(t, a.Config.BillingModelSource)
		for j, rule := range a.CostRules {
			require.Equal(t, j+1, rule.SourceOrdinal, "source_ordinal 从 1 起连续")
			require.Equal(t, ch.ID, rule.SourceChannelID)
			require.LessOrEqual(t, len([]rune(rule.Name)), matrixRuleNameMaxLen)
			if j > 0 {
				prev := a.CostRules[j-1]
				require.LessOrEqual(t, prev.SortOrder, rule.SortOrder, "成本核算规则按 sort_order 排名")
			}
		}
		require.Len(t, a.CostRules, len(ch.AccountStatsPricingRules))
	}
}

// 属性：白名单与开放分组的差别只在 inherit 行是否写出；有价与空 custom 的单元格两者相同。
func TestDeriveGroupState_PropertyAllowlistOnlyAddsInheritRows(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 300; i++ {
		ch := mxRandomChannel(r)
		ch.RestrictModels = false
		open := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), mxFactsForPools())
		ch.RestrictModels = true
		allow := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), mxFactsForPools())

		openByKey := map[matrixCellKey]MatrixCell{}
		for _, c := range open.Cells {
			openByKey[matrixCellKey{c.ModelKey, c.IsPattern}] = c
		}
		for _, c := range allow.Cells {
			if o, ok := openByKey[matrixCellKey{c.ModelKey, c.IsPattern}]; ok {
				require.Equal(t, o, c, "两种分组里都有的单元格必须相同")
			} else {
				require.Equal(t, MatrixPriceInherit, c.PriceMode, "白名单独有的只能是 inherit 行")
			}
		}
		require.GreaterOrEqual(t, len(allow.Cells), len(open.Cells))
	}
}
