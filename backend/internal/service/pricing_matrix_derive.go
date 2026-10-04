package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// 派生：从渠道到矩阵（设计文档 4.2）。
//
// DeriveGroupState 是纯函数：没有 I/O、不读时钟、不读全局状态，不修改传入的渠道。
// 「模型有没有官方价、是不是图片模型」这两条会随价格数据变动的事实，由调用方通过
// OfficialPriceFacts 显式传入（CHECK_OPUS_3 3.4），用到的那部分事实的摘要折进 Revision。
//
// 每条规则的出处（设计文档章节）写在各函数注释里；与设计的出入在 PR 正文里逐条列出。

const (
	matrixModelKeyMaxLen = 200 // model_group_prices.model_key 的列宽
	matrixRuleNameMaxLen = 100 // cost_accounting_rules.name 的列宽
)

// 派生备注的代码。
const (
	noteGroupDeleted            = "group_deleted"
	noteChannelInactive         = "channel_inactive"
	noteGroupNotInChannel       = "group_not_in_channel"
	noteBillingSourceUnknown    = "billing_model_source_unknown"
	noteMappingPlatformIgnored  = "mapping_platform_ignored"
	notePricingPlatformMismatch = "pricing_platform_mismatch"
	noteEmptyModelName          = "empty_model_name"
	noteModelKeyTooLong         = "model_key_too_long"
	noteExactDuplicate          = "exact_duplicate_overridden"
	noteWildcardDuplicate       = "wildcard_duplicate_ignored"
	noteEmptyCustomRequestMode  = "empty_custom_request_mode"
	noteEmptyCustomSensitive    = "empty_custom_sensitive"
	noteEmptyCustomWildcard     = "empty_custom_wildcard"
	noteInheritRowKept          = "inherit_row_kept_for_literal_priority"
	noteInheritRowsOmitted      = "inherit_rows_omitted"
	noteRuleNameTruncated       = "cost_rule_name_truncated"
)

// 空价 token 条目被判为「选路敏感」的原因（设计文档 2.4、CHECK_OPUS_3 3.3、3.5）。
const (
	sensitivePlatform = "platform_not_openai"
	sensitiveDeepSeek = "deepseek"
	sensitiveRouting  = "routing_sensitive"
	sensitiveUnknown  = "facts_unknown"
	sensitiveImage    = "image_capable"
	sensitiveNoPrice  = "no_official_price"
)

func defaultMatrixGroupConfig() MatrixGroupConfig {
	return MatrixGroupConfig{
		AccessMode:         MatrixAccessOpen,
		BillingModelSource: nil,
		ModelMapping:       []MatrixMappingEntry{},
		Features:           map[string]any{},
		CostMode:           MatrixCostAccountRate,
	}
}

// matrixBillingMode 把空的计费模式规范成 token。
func matrixBillingMode(p ChannelModelPricing) BillingMode {
	if p.BillingMode == "" {
		return BillingModeToken
	}
	return p.BillingMode
}

// matrixPricingPriceEmpty 判断定价条目是否「价格全空」：主表六个价格字段全空，
// 且没有任何带价的区间（盘点 SQL A1 的同一口径，设计文档 2.4）。
func matrixPricingPriceEmpty(p ChannelModelPricing) bool {
	if p.InputPrice != nil || p.OutputPrice != nil || p.CacheWritePrice != nil ||
		p.CacheReadPrice != nil || p.ImageOutputPrice != nil || p.PerRequestPrice != nil {
		return false
	}
	for _, iv := range p.Intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil || iv.CacheWritePrice != nil ||
			iv.CacheReadPrice != nil || iv.PerRequestPrice != nil {
			return false
		}
	}
	return true
}

// officialPriceFactKey 官方价事实的键：去空白、小写。
func officialPriceFactKey(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// NewOfficialPriceFacts 向事实来源查询给定模型名，组成派生用的价格快照。
// src 为 nil 时返回空快照（所有模型按「事实未知」处理，即按敏感派生）。
func NewOfficialPriceFacts(src OfficialPriceFactSource, models []string) OfficialPriceFacts {
	out := make(OfficialPriceFacts, len(models))
	if src == nil {
		return out
	}
	for _, m := range models {
		key := officialPriceFactKey(m)
		if _, seen := out[key]; seen {
			continue
		}
		out[key] = src.LookupOfficialPriceFact(strings.TrimSpace(m))
	}
	return out
}

// CollectDeriveFactModels 返回派生某个平台的分组时会查询官方价事实的模型名（去重、按字典序）：
// 只有 openai 平台、价格全空的 token 精确条目会走到事实判定，其余路径不读事实。
func CollectDeriveFactModels(ch *Channel, platform string) []string {
	if ch == nil || platform != PlatformOpenAI {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for i := range ch.ModelPricing {
		p := &ch.ModelPricing[i]
		if !isPlatformPricingMatch(platform, p.Platform) ||
			matrixBillingMode(*p) != BillingModeToken || !matrixPricingPriceEmpty(*p) {
			continue
		}
		for _, name := range p.Models {
			if strings.HasSuffix(name, "*") {
				continue
			}
			key := officialPriceFactKey(name)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, strings.TrimSpace(name))
		}
	}
	sort.Strings(out)
	return out
}

// DeriveGroupState 由渠道与分组派生矩阵侧的完整状态（设计文档 4.2）。
//
// 无渠道的几种情形（分组已软删除、没有渠道、渠道停用、分组不在渠道里）一律得到默认状态：
// open、billing_model_source 为 NULL、无映射、无功能、account_rate、没有单元格与成本核算规则。
// 停用渠道按「无渠道」处理，与 GetChannelForGroup 返回 nil 一致（2.3）。
func DeriveGroupState(ch *Channel, group DeriveGroup, facts OfficialPriceFacts) DerivedGroupState {
	st := DerivedGroupState{
		GroupID:   group.ID,
		Config:    defaultMatrixGroupConfig(),
		Cells:     []MatrixCell{},
		CostRules: []MatrixCostRule{},
		Notes:     []DerivationNote{},
	}
	consulted := make(map[string]string)

	switch {
	case group.Deleted:
		st.Notes = append(st.Notes, DerivationNote{
			Level: DerivationNoteWarn, Code: noteGroupDeleted,
			Message: "分组已软删除，按无渠道处理；它在非 v2 阶段的派生行会被清掉",
		})
	case ch == nil:
		// 分组没有渠道：默认状态。
	case !ch.IsActive():
		st.Notes = append(st.Notes, DerivationNote{
			Level: DerivationNoteInfo, Code: noteChannelInactive,
			Message: fmt.Sprintf("渠道 #%d 已停用，按无渠道处理", ch.ID),
		})
	case !matrixChannelHasGroup(ch, group.ID):
		st.Notes = append(st.Notes, DerivationNote{
			Level: DerivationNoteWarn, Code: noteGroupNotInChannel,
			Message: fmt.Sprintf("分组不在渠道 #%d 的分组列表里，按无渠道处理", ch.ID),
		})
	default:
		deriveFromActiveChannel(&st, ch, group, facts, consulted)
	}

	st.Revision = matrixDeriveRevision(st, consulted)
	return st
}

func matrixChannelHasGroup(ch *Channel, groupID int64) bool {
	for _, id := range ch.GroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}

func deriveFromActiveChannel(st *DerivedGroupState, ch *Channel, group DeriveGroup, facts OfficialPriceFacts, consulted map[string]string) {
	st.ChannelID = ch.ID
	cfg := &st.Config

	if ch.RestrictModels {
		cfg.AccessMode = MatrixAccessAllowlist
	}

	bms := ch.BillingModelSource
	switch bms {
	case BillingModelSourceRequested, BillingModelSourceUpstream, BillingModelSourceChannelMapped:
	case "":
		// 空值与 populateChannelCache 的归一化一致：当作 channel_mapped。
		bms = BillingModelSourceChannelMapped
	default:
		// 未知取值在网关里的处理与 channel_mapped 相同（billingModelForRestriction 的默认分支）。
		st.Notes = append(st.Notes, DerivationNote{
			Level: DerivationNoteWarn, Code: noteBillingSourceUnknown,
			Message: fmt.Sprintf("渠道的 billing_model_source 取值 %q 不在已知范围内，按 channel_mapped 处理", ch.BillingModelSource),
		})
		bms = BillingModelSourceChannelMapped
	}
	cfg.BillingModelSource = &bms

	cfg.ModelMapping = deriveMatrixMapping(ch.ModelMapping[group.Platform])
	otherPlatforms := make([]string, 0, len(ch.ModelMapping))
	for platform, m := range ch.ModelMapping {
		if platform != group.Platform && len(m) > 0 {
			otherPlatforms = append(otherPlatforms, platform)
		}
	}
	sort.Strings(otherPlatforms)
	for _, platform := range otherPlatforms {
		st.Notes = append(st.Notes, DerivationNote{
			Level: DerivationNoteInfo, Code: noteMappingPlatformIgnored,
			Message: fmt.Sprintf("渠道里平台 %q 的映射不属于分组平台 %q，没有运行时效果，不派生", platform, group.Platform),
		})
	}

	cfg.Features = deriveMatrixFeatures(ch.FeaturesConfig)

	if ch.ApplyPricingToAccountStats {
		cfg.CostMode = MatrixCostFollowBilling
	} else {
		cfg.CostMode = MatrixCostCatalogUpstream
	}

	deriver := &matrixCellDeriver{
		group:     group,
		allowlist: cfg.AccessMode == MatrixAccessAllowlist,
		routing:   bms == BillingModelSourceRequested || len(cfg.ModelMapping) > 0,
		facts:     facts,
		consulted: consulted,
	}
	st.Cells = deriver.derive(ch)
	st.Notes = append(st.Notes, deriver.notes...)

	var ruleNotes []DerivationNote
	st.CostRules, ruleNotes = deriveMatrixCostRules(ch)
	st.Notes = append(st.Notes, ruleNotes...)
}

// deriveMatrixMapping 把渠道映射（取分组平台那一项）派生成有序数组（PR1b-1 / #1538 的规则，设计文档 2.3、4.8）：
//   - 精确名在前，按 src 的字节序依次写入，小写后同键的后者覆盖前者；
//   - 通配符在后，前缀（小写）长者优先，同长按字典序；
//     前缀小写后相同的合并成一条，按 src 字节序靠后的覆盖。
//
// 保留胜出者原始的 src；dst 原样保留（包括空串）。
func deriveMatrixMapping(src map[string]string) []MatrixMappingEntry {
	if len(src) == 0 {
		return []MatrixMappingEntry{}
	}
	srcs := make([]string, 0, len(src))
	for k := range src {
		srcs = append(srcs, k)
	}
	sort.Strings(srcs)

	exact := make(map[string]MatrixMappingEntry)
	wildcards := make(map[string]MatrixMappingEntry)
	for _, k := range srcs {
		entry := MatrixMappingEntry{Src: k, Dst: src[k]}
		if strings.HasSuffix(k, "*") {
			wildcards[strings.ToLower(strings.TrimSuffix(k, "*"))] = entry
		} else {
			exact[strings.ToLower(k)] = entry
		}
	}

	out := make([]MatrixMappingEntry, 0, len(exact)+len(wildcards))
	exactKeys := make([]string, 0, len(exact))
	for k := range exact {
		exactKeys = append(exactKeys, k)
	}
	sort.Strings(exactKeys)
	for _, k := range exactKeys {
		out = append(out, exact[k])
	}
	prefixes := make([]string, 0, len(wildcards))
	for p := range wildcards {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if len(prefixes[i]) != len(prefixes[j]) {
			return len(prefixes[i]) > len(prefixes[j])
		}
		return prefixes[i] < prefixes[j]
	})
	for _, p := range prefixes {
		out = append(out, wildcards[p])
	}
	return out
}

// deriveMatrixFeatures 复刻三个功能开关当前实际生效的值（设计文档 2.3 的 S-2、Q9）：
//   - web_search_emulation：只保留按账号平台的 map 里值为 bool 的项；裸 bool 视为关，迁成空 map；
//   - bedrock_cc_compat：只保留裸 bool；按平台的 map 一律迁成 false；
//   - codex_image_generation_bridge：取裸 bool 或 map 里的 openai 键，归一成 bool；没有就不写。
//
// 开关不存在时不写对应的键；channels.features 文本字段不迁。
func deriveMatrixFeatures(fc map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := fc[featureKeyWebSearchEmulation]; ok {
		perPlatform := map[string]any{}
		if m, isMap := v.(map[string]any); isMap {
			for platform, raw := range m {
				if enabled, isBool := raw.(bool); isBool {
					perPlatform[platform] = enabled
				}
			}
		}
		out[featureKeyWebSearchEmulation] = perPlatform
	}
	if v, ok := fc[featureKeyBedrockCCCompat]; ok {
		enabled, _ := v.(bool)
		out[featureKeyBedrockCCCompat] = enabled
	}
	if override := platformBoolOverride(fc, featureKeyCodexImageGenerationBridge, PlatformOpenAI); override != nil {
		out[featureKeyCodexImageGenerationBridge] = *override
	}
	return out
}

// deriveMatrixCostRules 把渠道里的每条账号统计定价规则派生成该分组的一行（设计文档 2.5，R2-BK-1）：
// source_ordinal 是规则在渠道内按（sort_order, id）排名后的名次，从 1 起；
// group_ids、account_ids 与价格行原样复制，区间并入 price；
// 规则内部的匹配方式（先到先得、不做 claude 点号归一化、platform 为空匹配任意平台）不在派生里体现。
func deriveMatrixCostRules(ch *Channel) ([]MatrixCostRule, []DerivationNote) {
	rules := make([]AccountStatsPricingRule, len(ch.AccountStatsPricingRules))
	copy(rules, ch.AccountStatsPricingRules)
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].SortOrder != rules[j].SortOrder {
			return rules[i].SortOrder < rules[j].SortOrder
		}
		return rules[i].ID < rules[j].ID
	})

	var notes []DerivationNote
	out := make([]MatrixCostRule, 0, len(rules))
	for i, r := range rules {
		name := r.Name
		if len([]rune(name)) > matrixRuleNameMaxLen {
			name = string([]rune(name)[:matrixRuleNameMaxLen])
			notes = append(notes, DerivationNote{
				Level: DerivationNoteWarn, Code: noteRuleNameTruncated,
				Message: fmt.Sprintf("成本核算规则 #%d 的名称超过 %d 个字符，已截断", r.ID, matrixRuleNameMaxLen),
			})
		}
		rule := MatrixCostRule{
			Name:            name,
			SourceChannelID: ch.ID,
			SourceOrdinal:   i + 1,
			GroupIDs:        append([]int64{}, r.GroupIDs...),
			AccountIDs:      append([]int64{}, r.AccountIDs...),
			SortOrder:       r.SortOrder,
			Enabled:         true,
			Prices:          make([]MatrixCostRulePrice, 0, len(r.Pricing)),
		}
		for _, p := range r.Pricing {
			rule.Prices = append(rule.Prices, MatrixCostRulePrice{
				Platform: p.Platform,
				Models:   append([]string{}, p.Models...),
				Price:    MatrixCustomPriceFromPricing(p),
			})
		}
		out = append(out, rule)
	}
	return out, notes
}

// matrixCellDeriver 派生一个分组的单元格。
type matrixCellDeriver struct {
	group     DeriveGroup
	allowlist bool
	routing   bool // 计费来源为 requested，或渠道对该平台有映射（CHECK_OPUS_3 3.3）
	facts     OfficialPriceFacts
	consulted map[string]string
	notes     []DerivationNote
}

type matrixExactSrc struct {
	name  string
	entry *ChannelModelPricing
}

type matrixPatternSrc struct {
	prefix string
	name   string
	entry  *ChannelModelPricing
}

type matrixCellKind int

const (
	matrixCellCustom  matrixCellKind = iota // 写成 custom 单元格
	matrixCellInherit                       // 价格全空且不敏感：inherit
)

func (d *matrixCellDeriver) note(level, code, model, msg string) {
	d.notes = append(d.notes, DerivationNote{Level: level, Code: code, Model: model, Message: msg})
}

// derive 按设计文档 2.4、4.2 的规则派生单元格：
//   - 只取平台等于分组平台的定价条目，其余记备注；
//   - 精确名：key = normalizeChannelPricingModelName(名)，重复时后者覆盖前者；
//   - 通配符：raw 以 * 结尾，key = 归一化后的前缀，pattern_order 按出现顺序，同前缀取第一个；
//   - 有价条目 → custom；价格全空的 per_request / image 条目、通配符条目 → 空 custom；
//   - 价格全空的 token 条目：敏感 → 空 custom，不敏感 → inherit（开放分组不写行，白名单分组写 open=true 加 inherit）；
//   - 开放分组省略 inherit 行之前，要保证字面名优先不被基名的 custom 接管（CHECK_OPUS_3 3.2）。
func (d *matrixCellDeriver) derive(ch *Channel) []MatrixCell {
	exact := make(map[string]matrixExactSrc)
	var patterns []matrixPatternSrc
	seenPrefix := make(map[string]struct{})

	for i := range ch.ModelPricing {
		p := &ch.ModelPricing[i]
		if !isPlatformPricingMatch(d.group.Platform, p.Platform) {
			d.note(DerivationNoteInfo, notePricingPlatformMismatch, "",
				fmt.Sprintf("定价条目 #%d 的平台 %q 不等于分组平台 %q，没有运行时效果，不派生（%d 个模型名）", p.ID, p.Platform, d.group.Platform, len(p.Models)))
			continue
		}
		for _, name := range p.Models {
			if strings.HasSuffix(name, "*") {
				prefix := normalizeChannelPricingModelName(strings.TrimSuffix(name, "*"))
				if len(prefix) > matrixModelKeyMaxLen {
					d.note(DerivationNoteWarn, noteModelKeyTooLong, name, fmt.Sprintf("通配符前缀超过 %d 个字符，不派生", matrixModelKeyMaxLen))
					continue
				}
				if _, dup := seenPrefix[prefix]; dup {
					d.note(DerivationNoteInfo, noteWildcardDuplicate, name, "同一通配符前缀重复出现，legacy 先匹配者优先，后出现的不派生")
					continue
				}
				seenPrefix[prefix] = struct{}{}
				patterns = append(patterns, matrixPatternSrc{prefix: prefix, name: name, entry: p})
				continue
			}
			key := normalizeChannelPricingModelName(name)
			if key == "" {
				d.note(DerivationNoteWarn, noteEmptyModelName, name, "模型名为空，不派生")
				continue
			}
			if len(key) > matrixModelKeyMaxLen {
				d.note(DerivationNoteWarn, noteModelKeyTooLong, name, fmt.Sprintf("模型名超过 %d 个字符，不派生", matrixModelKeyMaxLen))
				continue
			}
			if prev, dup := exact[key]; dup {
				d.note(DerivationNoteInfo, noteExactDuplicate, key,
					fmt.Sprintf("同名模型重复出现（%q 与 %q 归一后同键），legacy 后者覆盖前者", prev.name, name))
			}
			exact[key] = matrixExactSrc{name: name, entry: p}
		}
	}

	keys := make([]string, 0, len(exact))
	for k := range exact {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	custom := matrixCustomIndex{exact: make(map[string]struct{})}
	cells := make([]MatrixCell, 0, len(exact)+len(patterns))
	var inheritKeys []string
	for _, key := range keys {
		cell, kind := d.exactCell(key, exact[key])
		if kind == matrixCellInherit {
			inheritKeys = append(inheritKeys, key)
			continue
		}
		custom.exact[key] = struct{}{}
		cells = append(cells, cell)
	}
	patternCells := make([]MatrixCell, 0, len(patterns))
	for i, pt := range patterns {
		custom.patterns = append(custom.patterns, pt.prefix)
		patternCells = append(patternCells, d.patternCell(i, pt))
	}

	omitted := 0
	for _, key := range inheritKeys {
		row := MatrixCell{ModelKey: key, Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived}
		switch {
		case d.allowlist:
			// 白名单分组必须写 open=true 加 inherit 的行，否则会被挡。
			cells = append(cells, row)
		case custom.twoStepHit(key):
			// 开放分组里省略这一行会让字面名优先失效：基名或通配符的 custom 单元格会接管它。
			d.note(DerivationNoteInfo, noteInheritRowKept, key, "开放分组里显式写 inherit 行，保证字面名优先不被基名或通配符的 custom 单元格接管")
			cells = append(cells, row)
		default:
			omitted++
		}
	}
	if omitted > 0 {
		d.note(DerivationNoteInfo, noteInheritRowsOmitted, "",
			fmt.Sprintf("开放分组里 %d 个价格全空且不敏感的 token 条目派生为 inherit，不写行（缺省即开放加继承）", omitted))
	}

	sort.SliceStable(cells, func(i, j int) bool { return cells[i].ModelKey < cells[j].ModelKey })
	return append(cells, patternCells...)
}

// exactCell 派生一个精确名单元格，kind 为 matrixCellInherit 时返回的 cell 只是占位，由调用方决定写不写。
func (d *matrixCellDeriver) exactCell(key string, src matrixExactSrc) (MatrixCell, matrixCellKind) {
	p := src.entry
	cell := MatrixCell{ModelKey: key, Open: true, Source: MatrixSourceLegacyDerived}

	if !matrixPricingPriceEmpty(*p) {
		price := MatrixCustomPriceFromPricing(*p)
		cell.PriceMode = MatrixPriceCustom
		cell.CustomPrice = &price
		return cell, matrixCellCustom
	}

	mode := matrixBillingMode(*p)
	if mode != BillingModeToken {
		// 价格全空的 per_request / image 条目：现状按 0 元出图，inherit 会改成按官方价收费（BK-1）。
		d.note(DerivationNoteInfo, noteEmptyCustomRequestMode, key, fmt.Sprintf("价格全空的 %s 条目保持空 custom", mode))
		return d.emptyCustom(cell, mode), matrixCellCustom
	}
	if reason := d.emptyTokenSensitivity(key, src.name); reason != "" {
		d.note(DerivationNoteInfo, noteEmptyCustomSensitive, key, "价格全空的 token 条目选路敏感，保持空 custom：原因 "+reason)
		return d.emptyCustom(cell, mode), matrixCellCustom
	}
	cell.PriceMode = MatrixPriceInherit
	return cell, matrixCellInherit
}

func (d *matrixCellDeriver) emptyCustom(cell MatrixCell, mode BillingMode) MatrixCell {
	cell.PriceMode = MatrixPriceCustom
	cell.CustomPrice = &MatrixCustomPrice{BillingMode: mode}
	return cell
}

// patternCell 派生一个通配符单元格：通配符一律写成 custom（匹配的模型集合未知，没法判定敏感）。
func (d *matrixCellDeriver) patternCell(order int, pt matrixPatternSrc) MatrixCell {
	cell := MatrixCell{
		ModelKey:     pt.prefix,
		IsPattern:    true,
		PatternOrder: order,
		Open:         true,
		PriceMode:    MatrixPriceCustom,
		Source:       MatrixSourceLegacyDerived,
	}
	if matrixPricingPriceEmpty(*pt.entry) {
		d.note(DerivationNoteInfo, noteEmptyCustomWildcard, pt.name, "价格全空的通配符条目保持空 custom（匹配的模型集合未知）")
		return d.emptyCustom(cell, matrixBillingMode(*pt.entry))
	}
	price := MatrixCustomPriceFromPricing(*pt.entry)
	cell.CustomPrice = &price
	return cell
}

// emptyTokenSensitivity 判断价格全空的 token 条目是否选路敏感，返回空串表示不敏感（设计文档 2.4，
// CHECK_OPUS_3 3.3、3.5）。拿不准的一律按敏感处理：
//   - 分组平台不是 openai（Anthropic、Gemini 网关的 token 路径与长上下文）；
//   - DeepSeek 系列（Source 非 LiteLLM 时不强制官方价、不叠加峰价）；
//   - 计费来源为 requested、或渠道对该平台有映射（图片选路看的是首个候选，不是图片模型本身）；
//   - 官方价事实里没有这个模型、是图片模型、或没有官方价（空 custom 对无价模型造出全零价，inherit 会报无价）。
func (d *matrixCellDeriver) emptyTokenSensitivity(key, name string) string {
	if d.group.Platform != PlatformOpenAI {
		return sensitivePlatform
	}
	if strings.Contains(key, "deepseek") {
		return sensitiveDeepSeek
	}
	if d.routing {
		return sensitiveRouting
	}
	factKey := officialPriceFactKey(name)
	fact, ok := d.facts[factKey]
	if !ok {
		d.consulted[factKey] = "unknown"
		return sensitiveUnknown
	}
	d.consulted[factKey] = fmt.Sprintf("price=%t,image=%t", fact.HasPrice, fact.ImageCapable)
	if fact.ImageCapable {
		return sensitiveImage
	}
	if !fact.HasPrice {
		return sensitiveNoPrice
	}
	return ""
}

// matrixCustomIndex 已写入的 custom 单元格索引，用来模拟 v2 的两步查找。
type matrixCustomIndex struct {
	exact    map[string]struct{}
	patterns []string
}

// hit 先查精确名，再查通配符前缀，与 lookupPricingAcrossPlatforms 的顺序一致。
func (x matrixCustomIndex) hit(name string) bool {
	key := normalizeChannelPricingModelName(name)
	if _, ok := x.exact[key]; ok {
		return true
	}
	for _, prefix := range x.patterns {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// twoStepHit 复刻 lookupChannelPricingNormalized 的两步：先字面名，未命中再用
// normalizeKnownOpenAICodexModel 归一化后的名字查一次。
func (x matrixCustomIndex) twoStepHit(name string) bool {
	if x.hit(name) {
		return true
	}
	normalized := normalizeKnownOpenAICodexModel(name)
	if normalized == "" || strings.EqualFold(normalized, strings.TrimSpace(name)) {
		return false
	}
	return x.hit(normalized)
}

// matrixDeriveRevision 配置、单元格、成本核算规则与所用官方价事实的摘要。
// 事实只含派生实际查询过的那些模型，价格数据变动导致判定改变时 revision 随之改变。
func matrixDeriveRevision(st DerivedGroupState, consulted map[string]string) string {
	facts := make([]string, 0, len(consulted))
	for k, v := range consulted {
		facts = append(facts, k+"="+v)
	}
	sort.Strings(facts)
	payload := struct {
		Config    MatrixGroupConfig `json:"config"`
		Cells     []MatrixCell      `json:"cells"`
		CostRules []MatrixCostRule  `json:"cost_rules"`
		Facts     []string          `json:"facts"`
	}{st.Config, st.Cells, st.CostRules, facts}
	sum := sha256.Sum256([]byte(matrixCanonicalJSON(payload)))
	return hex.EncodeToString(sum[:16])
}
