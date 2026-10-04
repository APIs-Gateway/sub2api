package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// W6 PR4-1：v2 引擎本体 matrixPolicy（设计文档 2.9、3.2、3.3）。
//
// matrixPolicy 用「分组快照」实现 GroupPolicy 的全部方法。快照是某个分组的 group_model_config 行、
// 全部单元格（model_group_prices）与成本核算行（cost_accounting_rules）的只读编译结果。
//
// 本 PR 没有任何生产路径构造它：所有分组仍走 legacyPolicy，stagedPolicy 与阶段切换是 PR5 的事。
// matrixPolicy 目前只被测试驱动（pricing_matrix_noreaders_test.go 把「没有生产读取方」固化成了测试）。

// QuoteAccessReasonClosedInGroup 模型在分组里被显式关闭（单元格 open=false）。
const QuoteAccessReasonClosedInGroup = "closed_in_group"

const (
	// matrixSnapshotTTL 分组快照的存活时间（设计 2.9）。写入后经 pubsub 主动失效，TTL 是兜底。
	matrixSnapshotTTL = 60 * time.Second
	// matrixSnapshotErrorTTL 加载失败后的重试间隔：这段时间内不再碰数据库，避免故障期间每个请求都去重试。
	matrixSnapshotErrorTTL = 5 * time.Second
	// matrixSnapshotDBTimeout 单次加载的数据库超时。加载用脱离请求取消信号的 ctx，避免请求被取消时留下残缺结果。
	matrixSnapshotDBTimeout = 10 * time.Second
	// matrixSnapshotNotifyTimeout 发布失效通知的超时。
	matrixSnapshotNotifyTimeout = 3 * time.Second
)

// MatrixSnapshotSource 分组快照的数据来源，是 PricingMatrixRepository 的读侧子集。
type MatrixSnapshotSource interface {
	// GetGroupMeta 返回分组的平台与软删除标记；不存在的分组不出现在结果里。
	GetGroupMeta(ctx context.Context, groupIDs []int64) (map[int64]DeriveGroup, error)
	// LoadGroupSnapshots 读取各分组的 group_model_config 行、全部单元格与成本核算行。
	LoadGroupSnapshots(ctx context.Context, groupIDs []int64) (map[int64]GroupStateSnapshot, error)
}

// MatrixSnapshotInvalidator 矩阵表被写入之后，用来失效 matrixPolicy 的分组快照。
// 写入方（派生钩子、后续 PR 的单元格写入与阶段切换）在事务提交之后调用。
type MatrixSnapshotInvalidator interface {
	InvalidateGroups(groupIDs ...int64)
}

// MatrixSnapshotStats 快照缓存的进程内计数。
type MatrixSnapshotStats struct {
	// Loads 成功加载的次数。
	Loads int64 `json:"loads"`
	// LoadFailures 加载失败的次数。
	LoadFailures int64 `json:"load_failures"`
	// StaleServed 加载失败后继续使用旧快照的次数。
	StaleServed int64 `json:"stale_served"`
	// ColdFallbacks 加载失败且没有旧快照、退回默认状态的次数。
	ColdFallbacks int64 `json:"cold_fallbacks"`
}

// matrixCellView 单元格在快照里的编译形态。
type matrixCellView struct {
	key          string // 归一化后的 model_key；通配符单元格是前缀
	patternOrder int    // 通配符单元格的匹配顺序，小者优先
	open         bool
	mode         MatrixPriceMode
	// extra 是 mode == extra 时的额外倍率，保证大于 0。
	extra float64
	// pricing 是 mode == custom 时的价格覆盖，只读；对外返回前必须 Clone。
	pricing       *ChannelModelPricing
	effectiveFrom *time.Time
	effectiveTo   *time.Time
}

// activeAt 价格覆盖（custom 价与额外倍率）在 at 时刻是否生效：effective_from <= at < effective_to。
// 窗口只作用于价格覆盖，不作用于开放状态（设计 2.4）；窗口外按 inherit 处理。
func (c *matrixCellView) activeAt(at time.Time) bool {
	if c.effectiveFrom != nil && at.Before(*c.effectiveFrom) {
		return false
	}
	if c.effectiveTo != nil && !at.Before(*c.effectiveTo) {
		return false
	}
	return true
}

// matrixSnapshot 一个分组的只读快照。构造之后不再修改，可以被多个 goroutine 同时读取。
type matrixSnapshot struct {
	platform string
	stage    PricingStage

	accessMode MatrixAccessMode
	// billingModelSource 为空串表示分组没有渠道（设计 S-1）：Mapping 原样返回空串。
	billingModelSource string
	costMode           MatrixCostMode
	features           map[string]any

	mappingExact     map[string]string
	mappingWildcards []*wildcardMappingEntry

	exact    map[string]*matrixCellView
	patterns []*matrixCellView // 按 pattern_order 升序，先匹配者优先

	costRules []AccountStatsPricingRule

	// loadErr 非空表示这是加载失败时的兜底快照（没有可用的旧快照时退回默认状态）。
	loadErr error
}

// buildMatrixSnapshot 把库里的分组现状编译成快照。纯函数，不做 I/O。
// 分组不存在或已软删除时，调用方传入空的 GroupStateSnapshot，得到默认状态。
func buildMatrixSnapshot(groupID int64, platform string, snap GroupStateSnapshot) *matrixSnapshot {
	cfg := defaultMatrixGroupConfig()
	stage := PricingStageLegacy
	if snap.Config != nil {
		cfg = normalizeMatrixConfig(snap.Config.MatrixGroupConfig)
		if snap.Config.PricingStage != "" {
			stage = snap.Config.PricingStage
		}
	}

	s := &matrixSnapshot{
		platform:     platform,
		stage:        stage,
		accessMode:   MatrixAccessOpen,
		costMode:     MatrixCostAccountRate,
		features:     deepCopyFeaturesConfig(cfg.Features),
		mappingExact: make(map[string]string),
		exact:        make(map[string]*matrixCellView),
	}
	if cfg.AccessMode == MatrixAccessAllowlist {
		s.accessMode = MatrixAccessAllowlist
	}
	switch cfg.CostMode {
	case MatrixCostCatalogUpstream, MatrixCostFollowBilling:
		s.costMode = cfg.CostMode
	}
	if cfg.BillingModelSource != nil {
		s.billingModelSource = *cfg.BillingModelSource
	}

	s.compileMapping(cfg.ModelMapping)
	s.compileCells(platform, snap.Cells)
	s.compileCostRules(groupID, snap.Rules)
	return s
}

// compileMapping 编译映射数组。规则与 expandMappingToCache（PR1b-1 / #1538）一致：
// 精确名按小写比较，同键后者覆盖前者；通配符前缀小写，同前缀后者覆盖，按前缀长度降序、同长按字典序排列。
// dst 原样保留，包括空串。
func (s *matrixSnapshot) compileMapping(entries []MatrixMappingEntry) {
	byPrefix := make(map[string]*wildcardMappingEntry)
	for _, e := range entries {
		if !strings.HasSuffix(e.Src, "*") {
			s.mappingExact[strings.ToLower(e.Src)] = e.Dst
			continue
		}
		prefix := strings.ToLower(strings.TrimSuffix(e.Src, "*"))
		if existing, dup := byPrefix[prefix]; dup {
			existing.target = e.Dst
			continue
		}
		w := &wildcardMappingEntry{prefix: prefix, target: e.Dst}
		byPrefix[prefix] = w
		s.mappingWildcards = append(s.mappingWildcards, w)
	}
	sortWildcardMappings(s.mappingWildcards)
}

// compileCells 编译单元格。非法的价格模式（extra 缺倍率或倍率不大于 0、custom 缺价格）按 inherit 处理：
// 数据库约束（mgp_extra_chk、mgp_custom_chk）保证不会出现，这里只是不让坏数据变成 panic 或零价。
func (s *matrixSnapshot) compileCells(platform string, cells []StoredMatrixCell) {
	for i := range cells {
		c := &cells[i]
		key := normalizeChannelPricingModelName(c.ModelKey)
		view := &matrixCellView{
			key:           key,
			patternOrder:  c.PatternOrder,
			open:          c.Open,
			mode:          MatrixPriceInherit,
			effectiveFrom: c.EffectiveFrom,
			effectiveTo:   c.EffectiveTo,
		}
		switch c.PriceMode {
		case MatrixPriceExtra:
			if c.ExtraMultiplier != nil && *c.ExtraMultiplier > 0 && !math.IsInf(*c.ExtraMultiplier, 0) {
				view.mode = MatrixPriceExtra
				view.extra = *c.ExtraMultiplier
			}
		case MatrixPriceCustom:
			if c.CustomPrice != nil {
				pricing := c.CustomPrice.ToChannelModelPricing(platform, []string{key})
				view.mode = MatrixPriceCustom
				view.pricing = &pricing
			}
		}
		if c.IsPattern {
			s.patterns = append(s.patterns, view)
			continue
		}
		s.exact[key] = view
	}
	// pattern_order 相同的（正常数据里不会有）按前缀字典序，保证结果确定。
	sort.SliceStable(s.patterns, func(i, j int) bool {
		if s.patterns[i].patternOrder != s.patterns[j].patternOrder {
			return s.patterns[i].patternOrder < s.patterns[j].patternOrder
		}
		return s.patterns[i].key < s.patterns[j].key
	})
}

// compileCostRules 编译成本核算规则，转成 tryCustomRules 直接能用的形态。
//
// 匹配语义（PR2 审查提醒 2.4）：
//   - 规则只属于本分组（scope_group_id 等于分组 id），由快照的取数范围保证，这里再过滤一次；
//   - 账号命中（account_ids）或规则内分组命中（group_ids），两者都为空的规则不匹配
//     （matchAccountStatsRule 已经是这个语义）；
//   - 规则内：先精确名后通配符、精确名先到先得、不做 claude 点号归一化、platform 为空匹配任意平台
//     （findPricingForModel 已经是这个语义），所以直接复用现有的 tryCustomRules；
//   - 规则之间按（sort_order，source_ordinal，id）排序，source_ordinal 为空的（manual）排在后面。
func (s *matrixSnapshot) compileCostRules(groupID int64, stored []StoredMatrixCostRule) {
	rules := make([]StoredMatrixCostRule, 0, len(stored))
	for _, r := range stored {
		if r.ScopeGroupID != groupID || !r.Enabled {
			continue
		}
		rules = append(rules, r)
	}
	ordinal := func(r StoredMatrixCostRule) int {
		if r.SourceOrdinal <= 0 {
			return math.MaxInt
		}
		return r.SourceOrdinal
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].SortOrder != rules[j].SortOrder {
			return rules[i].SortOrder < rules[j].SortOrder
		}
		if oi, oj := ordinal(rules[i]), ordinal(rules[j]); oi != oj {
			return oi < oj
		}
		return rules[i].ID < rules[j].ID
	})

	for _, r := range rules {
		rule := AccountStatsPricingRule{
			ID:         r.ID,
			ChannelID:  r.SourceChannelID,
			Name:       r.Name,
			GroupIDs:   append([]int64{}, r.GroupIDs...),
			AccountIDs: append([]int64{}, r.AccountIDs...),
			SortOrder:  r.SortOrder,
			Pricing:    make([]ChannelModelPricing, 0, len(r.Prices)),
		}
		for _, p := range r.Prices {
			rule.Pricing = append(rule.Pricing, p.Price.ToChannelModelPricing(p.Platform, p.Models))
		}
		s.costRules = append(s.costRules, rule)
	}
}

// lookupName 按单个名字查单元格：先精确名，再通配符（按 pattern_order，先匹配者优先）。
// 名字先过 normalizeChannelPricingModelName（去首尾空白、转小写、claude 名点号写成连字符），
// 与 lookupPricingAcrossPlatforms 完全一致。命中任何模式的单元格（包括 inherit、open=false）都返回。
func (s *matrixSnapshot) lookupName(model string) *matrixCellView {
	key := normalizeChannelPricingModelName(model)
	if cell, ok := s.exact[key]; ok {
		return cell
	}
	for _, cell := range s.patterns {
		if strings.HasPrefix(key, cell.key) {
			return cell
		}
	}
	return nil
}

// lookupCell 价格覆盖与额外倍率共用的单元格查找（设计 3.2、3.3、PR2 审查提醒 2.1）。
// 只查一次，返回整个单元格：
//  1. 先查字面名（精确名，再通配符）；字面名命中任何模式的单元格，包括 inherit，立即停止，
//     不再回落到基名，否则 PR2 显式写下的 inherit 行就白写了；
//  2. 未命中时，用 codex 归一化后的名字再查一次（gpt-5.6-luna-high 回落到 gpt-5.6-luna），
//     与 legacyPolicy.PriceOverride 的两步一致，包括 EqualFold 与 TrimSpace 的短路。
func (s *matrixSnapshot) lookupCell(model string) *matrixCellView {
	if cell := s.lookupName(model); cell != nil {
		return cell
	}
	normalized := normalizeKnownOpenAICodexModel(model)
	if normalized == "" || strings.EqualFold(normalized, strings.TrimSpace(model)) {
		return nil
	}
	return s.lookupName(normalized)
}

// lookupMapping 解析映射目标，规则与 lookupMappingAcrossPlatforms 一致：
// 先精确名，命中就停止（即使 dst 是空串，也不再查通配符）；再通配符，取第一个前缀命中的
// （前缀长者在前），同样即使 target 是空串也停止。返回空串表示没有映射。
func (s *matrixSnapshot) lookupMapping(modelLower string) string {
	if dst, ok := s.mappingExact[modelLower]; ok {
		return dst
	}
	for _, w := range s.mappingWildcards {
		if strings.HasPrefix(modelLower, w.prefix) {
			return w.target
		}
	}
	return ""
}

// matrixSnapshotEntry 缓存里的一项。
type matrixSnapshotEntry struct {
	snap      *matrixSnapshot
	expiresAt time.Time
	// invalidated 为 true 表示已被失效，下次读取必须重新加载；快照本身保留，供加载失败时兜底。
	invalidated bool
}

// matrixPolicy 用分组快照实现 GroupPolicy（设计 3.3）。
//
// 缓存（设计 2.9）：每个分组一份快照，singleflight 加载，TTL 60 秒；写入矩阵表之后经 pubsub 失效。
// pubsub 复用 ChannelService 缓存失效的同一套通知机制（ChannelCachePubSub）：收到通知就丢弃本进程里
// 全部分组的快照。所以渠道保存、分组改平台或删除时 ChannelService 发出的通知，也会让快照重新加载。
//
// 快照加载失败的语义（Opus 审查提醒 8；legacy 是写入 5 秒空缓存，效果等于「没有渠道」）：
//  1. 有旧快照（哪怕已过期或已被失效）：继续使用旧快照，并在 5 秒后重试。旧数据比没有数据好：
//     数据库抖动时不会让计费价、准入与映射突然退回「官方价、全放行」。
//  2. 没有旧快照（进程刚启动、该分组第一次读取就失败）：退回默认状态，也就是 legacy 在这种情况下的
//     行为：开放、没有映射、没有价格覆盖与额外倍率、成本模式 account_rate，阶段为 legacy；
//     同样 5 秒后重试。这是 fail-open，选它是因为拒绝全部请求的代价更大，且与 legacy 一致。
//     UpstreamCheck 与 Feature 本来就能返回错误，这时把加载错误交给调用方，由调用方按自己的既有口径处理。
//  3. 加载失败都会记 Warn 日志并计数（Stats）。PR5 的影子比对遇到「v2 一侧兜底」的分组应当跳过这次比较，
//     否则数据库抖动期间会报出翻译差异之外的噪声：用 SnapshotDegraded 判断。
//
// 加载失败从不向调用方抛错，也不会缓存一份「空」的结果超过 5 秒。
type matrixPolicy struct {
	src    MatrixSnapshotSource
	pubsub ChannelCachePubSub
	// now 与 TTL 可由测试替换。
	now    func() time.Time
	ttl    time.Duration
	errTTL time.Duration

	mu         sync.RWMutex
	entries    map[int64]*matrixSnapshotEntry
	generation uint64
	sf         singleflight.Group

	loads         atomic.Int64
	loadFailures  atomic.Int64
	staleServed   atomic.Int64
	coldFallbacks atomic.Int64
}

var _ GroupPolicy = (*matrixPolicy)(nil)
var _ MatrixSnapshotInvalidator = (*matrixPolicy)(nil)

// NewMatrixGroupPolicy 构造 matrixPolicy。pubsub 可以为 nil（不跨实例失效，只靠本地失效与 TTL）。
// 本 PR 里只有测试构造它，生产路径没有接线（接线是 PR5 的 stagedPolicy）。
func NewMatrixGroupPolicy(src MatrixSnapshotSource, pubsub ChannelCachePubSub) *matrixPolicy {
	p := &matrixPolicy{
		src:     src,
		pubsub:  pubsub,
		now:     time.Now,
		ttl:     matrixSnapshotTTL,
		errTTL:  matrixSnapshotErrorTTL,
		entries: make(map[int64]*matrixSnapshotEntry),
	}
	if pubsub != nil {
		// 收到其他实例（或本实例）的通知只清本地，不再转发，避免通知回环（与 ChannelService.clearCache 同理）。
		pubsub.SubscribeUpdates(context.Background(), p.invalidateAll)
	}
	return p
}

// Stats 返回快照缓存的进程内计数。
func (p *matrixPolicy) Stats() MatrixSnapshotStats {
	return MatrixSnapshotStats{
		Loads:         p.loads.Load(),
		LoadFailures:  p.loadFailures.Load(),
		StaleServed:   p.staleServed.Load(),
		ColdFallbacks: p.coldFallbacks.Load(),
	}
}

// InvalidateGroups 写入矩阵表之后调用：丢弃本进程里这些分组的快照，并通知其他实例。
// 通知没有载荷，其他实例会丢弃全部分组的快照（与渠道缓存的通知机制相同），代价只是多一次按分组的重新加载。
// 没有传入分组时什么也不做。
func (p *matrixPolicy) InvalidateGroups(groupIDs ...int64) {
	if len(groupIDs) == 0 {
		return
	}
	p.invalidate(groupIDs)
	p.notify()
}

// InvalidateAll 丢弃本进程里全部分组的快照，并通知其他实例。
func (p *matrixPolicy) InvalidateAll() {
	p.invalidate(nil)
	p.notify()
}

// invalidateAll 丢弃本进程里全部分组的快照，不发通知。订阅回调用它。
func (p *matrixPolicy) invalidateAll() {
	p.invalidate(nil)
}

// invalidate 把指定分组（groupIDs 为 nil 表示全部）的缓存项标记为已失效。
// 代数加一，让加载到一半的请求不会把加载前读到的旧数据存进缓存；新的读取也不会并入旧代数的 singleflight。
func (p *matrixPolicy) invalidate(groupIDs []int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation++
	mark := func(id int64) {
		if e, ok := p.entries[id]; ok && !e.invalidated {
			p.entries[id] = &matrixSnapshotEntry{snap: e.snap, expiresAt: e.expiresAt, invalidated: true}
		}
	}
	if groupIDs == nil {
		for id := range p.entries {
			mark(id)
		}
		return
	}
	for _, id := range groupIDs {
		mark(id)
	}
}

func (p *matrixPolicy) notify() {
	if p.pubsub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), matrixSnapshotNotifyTimeout)
	defer cancel()
	if err := p.pubsub.NotifyUpdate(ctx); err != nil {
		slog.Warn("failed to publish group matrix cache invalidation", "error", err)
	}
}

// snapshot 返回分组的快照，永不为 nil。
func (p *matrixPolicy) snapshot(ctx context.Context, groupID int64) *matrixSnapshot {
	now := p.now()
	p.mu.RLock()
	e := p.entries[groupID]
	gen := p.generation
	p.mu.RUnlock()
	if e != nil && !e.invalidated && now.Before(e.expiresAt) {
		return e.snap
	}

	v, _, _ := p.sf.Do(fmt.Sprintf("%d:%d", groupID, gen), func() (any, error) {
		return p.load(ctx, groupID, gen), nil
	})
	// load 永远返回非 nil 的 *matrixSnapshot，所以这里的断言不会失败。
	snap, _ := v.(*matrixSnapshot)
	return snap
}

// SnapshotDegraded 报告分组当前用的是不是加载失败时的默认状态兜底（语义第 2 条）。
// 使用旧快照（语义第 1 条）不算兜底：数据只是旧，不是缺。
func (p *matrixPolicy) SnapshotDegraded(ctx context.Context, groupID int64) bool {
	return p.snapshot(ctx, groupID).loadErr != nil
}

// load 从数据库加载一份快照并存进缓存；失败时按上面的语义兜底。gen 是调用方读到的缓存代数。
func (p *matrixPolicy) load(ctx context.Context, groupID int64, gen uint64) *matrixSnapshot {
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), matrixSnapshotDBTimeout)
	defer cancel()

	snap, err := p.fetch(dbCtx, groupID)
	now := p.now()
	if err == nil {
		p.loads.Add(1)
		p.store(gen, groupID, &matrixSnapshotEntry{snap: snap, expiresAt: now.Add(p.ttl)})
		return snap
	}

	p.loadFailures.Add(1)
	slog.Warn("failed to load group matrix snapshot", "group_id", groupID, "error", err)

	p.mu.RLock()
	prev := p.entries[groupID]
	p.mu.RUnlock()
	if prev != nil && prev.snap.loadErr == nil {
		p.staleServed.Add(1)
		p.store(gen, groupID, &matrixSnapshotEntry{snap: prev.snap, expiresAt: now.Add(p.errTTL)})
		return prev.snap
	}

	p.coldFallbacks.Add(1)
	fallback := buildMatrixSnapshot(groupID, "", GroupStateSnapshot{})
	fallback.loadErr = err
	p.store(gen, groupID, &matrixSnapshotEntry{snap: fallback, expiresAt: now.Add(p.errTTL)})
	return fallback
}

// store 存入缓存项。加载期间发生过失效（代数变了）就不存：这份数据可能早于那次写入。
func (p *matrixPolicy) store(gen uint64, groupID int64, e *matrixSnapshotEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.generation == gen {
		p.entries[groupID] = e
	}
}

// fetch 读取分组的元信息与快照数据并编译。分组不存在或已软删除时得到默认状态：
// 旧单元格由派生钩子清理，应用层读取时丢弃已删分组的行（设计 2.1）。
func (p *matrixPolicy) fetch(ctx context.Context, groupID int64) (*matrixSnapshot, error) {
	ids := []int64{groupID}
	metas, err := p.src.GetGroupMeta(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load group meta: %w", err)
	}
	meta, found := metas[groupID]
	if !found || meta.Deleted {
		return buildMatrixSnapshot(groupID, meta.Platform, GroupStateSnapshot{}), nil
	}
	snaps, err := p.src.LoadGroupSnapshots(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load group snapshot: %w", err)
	}
	return buildMatrixSnapshot(groupID, meta.Platform, snaps[groupID]), nil
}

// effectiveAt 计费时点为零值时取当前时间：resolver 目前不传时点（PR3 提醒 1），按当前时间判断生效窗口。
func (p *matrixPolicy) effectiveAt(at time.Time) time.Time {
	if at.IsZero() {
		return p.now()
	}
	return at
}

// Mapping 解析分组级模型映射。ChannelID 恒为 0：v2 分组不再写 usage_logs.channel_id（设计 2.3、附录 A 第 11 条）；
// 计费来源原样返回，没有渠道时是空串（设计 S-1）。
func (p *matrixPolicy) Mapping(ctx context.Context, groupID int64, model string) ChannelMappingResult {
	s := p.snapshot(ctx, groupID)
	result := ChannelMappingResult{MappedModel: model, BillingModelSource: s.billingModelSource}
	// 与 resolveMapping 一致：先去首尾空白再转小写，否则带空白的名字会绕过映射。
	modelLower := strings.ToLower(strings.TrimSpace(model))
	if mapped := s.lookupMapping(modelLower); mapped != "" {
		result.MappedModel = mapped
		result.Mapped = true
	}
	return result
}

// ModelAccess 判断请求模型（或计费模型）在分组里是否放行。只查字面名：先精确名，后通配符，
// 不做 codex 归一化（那是价格路径的第二步，准入若也归一化，白名单会比 legacy 宽），也不查目录别名（设计 3.3、S-3）。
// 名字的规范化与 checkRestricted 相同（去首尾空白、转小写、claude 名点号写成连字符）。
//
//   - 白名单分组：字面名命中一个 open=true 的单元格才放行，价格字段为空等于按官方价开放；
//   - 开放分组：除非字面名命中一个 open=false 的单元格（例外），都放行。
//
// 目录状态（draft、retired）不在这里判断：它属于 Quote.Access（PR4-2），运行时怎么接入留给 PR7。
func (p *matrixPolicy) ModelAccess(ctx context.Context, groupID int64, model string) QuoteAccess {
	s := p.snapshot(ctx, groupID)
	cell := s.lookupName(model)
	if s.accessMode == MatrixAccessAllowlist {
		switch {
		case cell == nil:
			return QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}
		case !cell.open:
			return QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}
		}
		return QuoteAccess{OK: true}
	}
	if cell != nil && !cell.open {
		return QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}
	}
	return QuoteAccess{OK: true}
}

// UpstreamAccess 判断账号映射之后的上游模型是否放行。与 ModelAccess 分开实现：
// 上游模型是内部实现细节，用户看不到也选不了，所以只有白名单分组才限制它，
// 并且只看白名单成员关系：字面名命中一个 open=true 的单元格。开放分组里 open=false 的例外是面向用户的
// 关闭，不约束上游模型；目录状态更不应该拦住上游模型（新模型上线前账号可以先映射过去）。
// 与 UpstreamCheck 对应：调度循环只在「白名单分组且计费来源是 upstream」时才会逐账号调用它。
func (p *matrixPolicy) UpstreamAccess(ctx context.Context, groupID int64, upstreamModel string) QuoteAccess {
	s := p.snapshot(ctx, groupID)
	if s.accessMode != MatrixAccessAllowlist {
		return QuoteAccess{OK: true}
	}
	cell := s.lookupName(upstreamModel)
	if cell == nil || !cell.open {
		return QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}
	}
	return QuoteAccess{OK: true}
}

// UpstreamCheck 调度循环是否需要逐账号检查上游模型：白名单分组，并且计费来源是 upstream
// （与 legacy 的「启用模型限制且计费来源是 upstream」对应）。兜底快照时返回加载错误。
func (p *matrixPolicy) UpstreamCheck(ctx context.Context, groupID int64) (bool, error) {
	s := p.snapshot(ctx, groupID)
	if s.loadErr != nil {
		return false, s.loadErr
	}
	return s.accessMode == MatrixAccessAllowlist && s.billingModelSource == BillingModelSourceUpstream, nil
}

// Feature 读取分组上的功能开关，形状按各开关实际生效的形状读（设计 2.3、S-2），与 Channel 上的读法一致：
//   - web_search_emulation：按账号平台的 map；开关存在但没有这个平台的项按关闭，开关不存在返回 nil；
//   - bedrock_cc_compat：裸 bool，不区分平台；
//   - codex_image_generation_bridge：裸 bool，或按平台的 map；没有设置返回 nil。
//
// 兜底快照时返回加载错误。
func (p *matrixPolicy) Feature(ctx context.Context, groupID int64, platform string, f GroupFeature) (*bool, error) {
	s := p.snapshot(ctx, groupID)
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	switch f {
	case GroupFeatureWebSearchEmulation:
		perPlatform, ok := s.features[featureKeyWebSearchEmulation].(map[string]any)
		if !ok {
			return nil, nil
		}
		enabled, _ := perPlatform[platform].(bool)
		return &enabled, nil
	case GroupFeatureBedrockCCCompat:
		enabled, ok := s.features[featureKeyBedrockCCCompat].(bool)
		if !ok {
			return nil, nil
		}
		return &enabled, nil
	case GroupFeatureCodexImageGenerationBridge:
		return platformBoolOverride(s.features, featureKeyCodexImageGenerationBridge, platform), nil
	default:
		return nil, nil
	}
}

// PriceOverride 返回单元格里的绝对价（custom），其余模式（inherit、extra）与没有单元格都返回 nil。
// 单元格查找见 lookupCell：字面名命中任何模式的单元格都停止，所以字面名上的 inherit 或 extra 单元格
// 不会再回落到基名的 custom 价。生效窗口外按 inherit 处理。at 为零值时取当前时间。
func (p *matrixPolicy) PriceOverride(ctx context.Context, groupID int64, model string, at time.Time) *ChannelModelPricing {
	cell := p.snapshot(ctx, groupID).lookupCell(model)
	if cell == nil || cell.mode != MatrixPriceCustom || cell.pricing == nil || !cell.activeAt(p.effectiveAt(at)) {
		return nil
	}
	cp := cell.pricing.Clone()
	return &cp
}

// ExtraMultiplier 返回单元格里的额外倍率（extra），其余模式与没有单元格都是 1。
// 与 PriceOverride 共用同一套单元格查找与生效窗口规则（设计 3.2）：基名上设的倍率对带 effort 后缀的变体名同样生效，
// 字面名上的单元格（哪怕是 inherit）优先。
func (p *matrixPolicy) ExtraMultiplier(ctx context.Context, groupID int64, model string, at time.Time) float64 {
	cell := p.snapshot(ctx, groupID).lookupCell(model)
	if cell == nil || cell.mode != MatrixPriceExtra || !cell.activeAt(p.effectiveAt(at)) {
		return 1
	}
	return cell.extra
}

// CostMode 返回账号成本核算模式。
func (p *matrixPolicy) CostMode(ctx context.Context, groupID int64) MatrixCostMode {
	return p.snapshot(ctx, groupID).costMode
}

// CostRules 返回分组的成本核算规则与分组平台。规则已按（sort_order，source_ordinal，id）排好，
// 匹配方式见 compileCostRules。返回的是副本，调用方改动不会污染快照。
// 注意 CostMode 与 CostRules 是两次读取：两次之间快照刚好被替换时，一个请求可能拿到「旧模式加新规则」，
// 窗口是微秒级，只影响账号统计成本，与 legacy 一致（PR3 审查建议 1 提过合并成一个方法，留给 PR5 决定）。
func (p *matrixPolicy) CostRules(ctx context.Context, groupID int64) ([]AccountStatsPricingRule, string) {
	s := p.snapshot(ctx, groupID)
	if len(s.costRules) == 0 {
		return nil, s.platform
	}
	return cloneAccountStatsPricingRules(s.costRules), s.platform
}

// Stage 返回分组的价格体系阶段；没有配置行与兜底快照都是 legacy。
func (p *matrixPolicy) Stage(ctx context.Context, groupID int64) PricingStage {
	return p.snapshot(ctx, groupID).stage
}

// cloneAccountStatsPricingRules 深拷贝成本核算规则（与 Channel.Clone 里的拷贝方式一致）。
func cloneAccountStatsPricingRules(rules []AccountStatsPricingRule) []AccountStatsPricingRule {
	out := make([]AccountStatsPricingRule, len(rules))
	for i, rule := range rules {
		cp := rule
		cp.GroupIDs = append([]int64{}, rule.GroupIDs...)
		cp.AccountIDs = append([]int64{}, rule.AccountIDs...)
		cp.Pricing = make([]ChannelModelPricing, len(rule.Pricing))
		for j := range rule.Pricing {
			cp.Pricing[j] = rule.Pricing[j].Clone()
		}
		out[i] = cp
	}
	return out
}
