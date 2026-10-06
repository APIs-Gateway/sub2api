package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// W6 PR7a：v2 阶段在运行时额外要做的两件事（设计 2.2、3.2「准入」、5.2「运行时」）。
//
//  1. 模型目录状态：v2 分组的准入在单元格判定之后叠加目录，显式 draft、retired 的模型不放行，未登记视同 active（S-3、Q2）。
//     目录读取走带缓存的读取方（runtimeCatalog），热路径不碰数据库；读不到目录时放行并限速记 Warn。
//  2. 白名单分组的无价检查（RuntimeAccess）：调度前能拿到的候选链里任一有价就算有价（R2-S-5）；
//     无价时先观测（日志加计数），全局开关 billing_unpriced_policy = block_allowlist 才拦，且只拦白名单的 v2 分组。
//
// legacy、shadow 分组两件事都不做（route 返回 legacyPolicy），所以它们的准入结果与改动前逐位相同。

// ---------------------------------------------------------------------------
// 带缓存的目录读取方
// ---------------------------------------------------------------------------

const (
	// runtimeCatalogTTL 目录条目在进程内的缓存时间。目录状态变更（上线、下线）最多这么久之后在本实例生效。
	runtimeCatalogTTL = 15 * time.Second
	// runtimeCatalogErrorTTL 加载失败后沿用旧数据的时间，避免故障期间每个请求都去读库。
	runtimeCatalogErrorTTL = 5 * time.Second
	// runtimeCatalogDBTimeout 单次加载目录的数据库超时。
	runtimeCatalogDBTimeout = 5 * time.Second
)

// runtimeCatalogSource 是运行时目录读取方对目录服务的最小依赖。
type runtimeCatalogSource interface {
	List(ctx context.Context, filter ModelCatalogFilter) ([]ModelCatalogEntry, error)
}

type runtimeCatalogEntry struct {
	entries   []ModelCatalogEntry
	expiresAt time.Time
}

// runtimeCatalog 按平台缓存目录条目。读取按平台整表加载（目录是几百行的小表），singleflight 合并并发加载。
// 加载失败时沿用旧数据；没有旧数据就把错误交给调用方（调用方放行并告警）。同时满足 PriceQuoter 的 quoteCatalogReader。
type runtimeCatalog struct {
	src runtimeCatalogSource
	ttl time.Duration
	now func() time.Time

	mu         sync.RWMutex
	byPlatform map[string]*runtimeCatalogEntry
	sf         singleflight.Group
}

func newRuntimeCatalog(src runtimeCatalogSource) *runtimeCatalog {
	return &runtimeCatalog{src: src, ttl: runtimeCatalogTTL, now: time.Now, byPlatform: make(map[string]*runtimeCatalogEntry)}
}

var _ quoteCatalogReader = (*runtimeCatalog)(nil)

// Resolve 按 model_key 或别名查同平台的目录条目；未登记返回 nil。
func (c *runtimeCatalog) Resolve(ctx context.Context, platform, model string) (*ModelCatalogEntry, error) {
	entries, err := c.platformEntries(ctx, strings.TrimSpace(platform))
	if err != nil {
		return nil, err
	}
	return ResolveCatalogEntry(entries, model), nil
}

func (c *runtimeCatalog) platformEntries(ctx context.Context, platform string) ([]ModelCatalogEntry, error) {
	now := c.now()
	c.mu.RLock()
	cached := c.byPlatform[platform]
	c.mu.RUnlock()
	if cached != nil && now.Before(cached.expiresAt) {
		return cached.entries, nil
	}
	v, err, _ := c.sf.Do(platform, func() (any, error) {
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtimeCatalogDBTimeout)
		defer cancel()
		return c.src.List(dbCtx, ModelCatalogFilter{Platform: platform})
	})
	if err != nil {
		if cached == nil {
			return nil, err
		}
		stale := &runtimeCatalogEntry{entries: cached.entries, expiresAt: c.now().Add(runtimeCatalogErrorTTL)}
		c.mu.Lock()
		c.byPlatform[platform] = stale
		c.mu.Unlock()
		return stale.entries, nil
	}
	entries, _ := v.([]ModelCatalogEntry)
	fresh := &runtimeCatalogEntry{entries: entries, expiresAt: c.now().Add(c.ttl)}
	c.mu.Lock()
	c.byPlatform[platform] = fresh
	c.mu.Unlock()
	return entries, nil
}

// SetModelCatalog 给 stagedPolicy 接上目录，并返回带缓存的读取方（同一个对象也交给 PriceQuoter，两边口径一致）。
// 只在装配阶段调用。src 为 nil 时返回 nil，v2 准入不看目录。
func (s *stagedPolicy) SetModelCatalog(src runtimeCatalogSource) *runtimeCatalog {
	if s == nil || src == nil {
		return nil
	}
	s.catalog = newRuntimeCatalog(src)
	return s.catalog
}

// catalogAccess 在 active 是矩阵（v2）时叠加目录状态；其余情况放行。
func (s *stagedPolicy) catalogAccess(ctx context.Context, active GroupPolicy, groupID int64, model string) QuoteAccess {
	m, ok := active.(*matrixPolicy)
	if !ok || s.catalog == nil {
		return QuoteAccess{OK: true}
	}
	platform := m.snapshot(ctx, groupID).platform
	if platform == "" {
		return QuoteAccess{OK: true}
	}
	entry, err := s.catalog.Resolve(ctx, platform, model)
	if err != nil {
		s.runtime.logLimited(s.hub.now(), "catalog:"+platform, func() {
			slog.Warn("v2 admission: model catalog unavailable, treating models as active",
				"group_id", groupID, "platform", platform, "error", err)
		})
		return QuoteAccess{OK: true}
	}
	return catalogEntryAccess(entry)
}

// ---------------------------------------------------------------------------
// 白名单分组的无价检查
// ---------------------------------------------------------------------------

const (
	// QuoteAccessReasonUnpriced 白名单分组里的模型按候选链判断没有任何价格（只出现在运行时检查里）。
	QuoteAccessReasonUnpriced = "unpriced"

	runtimePricedTTL        = 30 * time.Second
	runtimePricedMaxEntries = 4096
	runtimePolicyTTL        = 15 * time.Second
	runtimeLogInterval      = time.Minute
	runtimeLogMaxKeys       = 1024
)

// RuntimePriceInputs 是网关交给 RuntimeAccess 的、策略自己没有的价格事实。
type RuntimePriceInputs struct {
	// OfficialState 查官方价（动态目录或内置兜底）里关于这个模型的事实（有没有价、token 价是否非零、是否图片模型），
	// 与保存时校验用同一份 OfficialPriceState；为 nil 按「官方没有价」。
	// PricingSnapshotID 是当前生效的价格快照 id（auto 模式为 0），HasPrice 缓存键的一部分。
	PricingSnapshotID int64
	// ReadPolicy 读取 billing_unpriced_policy 的当前值；为 nil 按 observe。
	ReadPolicy func(ctx context.Context) string
}

type runtimePricedKey struct {
	snapshotID int64
	groupID    int64
	revision   int64
	chain      string
}

type runtimePricedEntry struct {
	priced    bool
	expiresAt time.Time
}

// runtimePricingState 是无价检查的进程内状态：HasPrice 缓存、开关值缓存、日志限速与计数。
type runtimePricingState struct {
	mu       sync.Mutex
	priced   map[runtimePricedKey]runtimePricedEntry
	policy   string
	policyAt time.Time
	policyOK bool
	logged   map[string]time.Time

	observed atomic.Int64
	blocked  atomic.Int64
}

// UnpricedAdmissionStats 返回无价检查的进程内计数：观测到的无价请求数，以及其中被拦下的数量。
func (s *stagedPolicy) UnpricedAdmissionStats() (observed, blocked int64) {
	return s.runtime.observed.Load(), s.runtime.blocked.Load()
}

// logLimited 同一个 key 每分钟最多执行一次 fn，key 总数有上限。
func (r *runtimePricingState) logLimited(now time.Time, key string, fn func()) {
	r.mu.Lock()
	if r.logged == nil || len(r.logged) >= runtimeLogMaxKeys {
		r.logged = make(map[string]time.Time)
	}
	last, seen := r.logged[key]
	due := !seen || now.Sub(last) >= runtimeLogInterval
	if due {
		r.logged[key] = now
	}
	r.mu.Unlock()
	if due {
		fn()
	}
}

// pricedCached 按缓存键取 HasPrice 的结果，没有或过期就用 compute 算。
func (r *runtimePricingState) pricedCached(now time.Time, key runtimePricedKey, compute func() bool) bool {
	r.mu.Lock()
	if e, ok := r.priced[key]; ok && now.Before(e.expiresAt) {
		r.mu.Unlock()
		return e.priced
	}
	r.mu.Unlock()
	priced := compute()
	r.mu.Lock()
	if r.priced == nil || len(r.priced) >= runtimePricedMaxEntries {
		r.priced = make(map[runtimePricedKey]runtimePricedEntry)
	}
	r.priced[key] = runtimePricedEntry{priced: priced, expiresAt: now.Add(runtimePricedTTL)}
	r.mu.Unlock()
	return priced
}

// policyValue 返回 billing_unpriced_policy 的当前值（缓存 15 秒）。任何非 block_allowlist 的值都按 observe。
func (r *runtimePricingState) policyValue(ctx context.Context, now time.Time, read func(context.Context) string) string {
	r.mu.Lock()
	if r.policyOK && now.Sub(r.policyAt) < runtimePolicyTTL {
		v := r.policy
		r.mu.Unlock()
		return v
	}
	r.mu.Unlock()
	value := BillingUnpricedPolicyObserve
	if read != nil && read(ctx) == BillingUnpricedPolicyBlockAllowlist {
		value = BillingUnpricedPolicyBlockAllowlist
	}
	r.mu.Lock()
	r.policy, r.policyAt, r.policyOK = value, now, true
	r.mu.Unlock()
	return value
}

// RuntimeAccess 是调度阶段对白名单 v2 分组的无价检查（设计 5.2「运行时」）。candidates 是调度前能拿到的计费候选链
// （请求模型、映射后的模型、按计费来源构造的候选），任一有价就算有价（R2-S-5），偏向少拦。
//
// 只有路由到矩阵（v2）并且分组是白名单时才检查，其余一律 {OK: true} 且 Priced 为 nil：legacy、shadow、开放分组不受影响
// （开放分组始终放行加告警，由 PR1 的指标覆盖）。有价时 Priced 为 true；无价时 Priced 为 false、Reason 为 unpriced，
// 开关为 observe 时 OK 仍为 true（只记日志与计数），为 block_allowlist 时 OK 为 false。
func (s *stagedPolicy) RuntimeAccess(ctx context.Context, groupID int64, candidates []string, in RuntimePriceInputs) QuoteAccess {
	active, _ := s.route(ctx, groupID)
	m, ok := active.(*matrixPolicy)
	if !ok || len(candidates) == 0 {
		return QuoteAccess{OK: true}
	}
	snap := m.snapshot(ctx, groupID)
	if snap.accessMode != MatrixAccessAllowlist || snap.loadErr != nil {
		return QuoteAccess{OK: true}
	}
	now := s.hub.now()
	key := runtimePricedKey{snapshotID: in.PricingSnapshotID, groupID: groupID, revision: snap.revision, chain: strings.Join(candidates, "\x00")}
	priced := s.runtime.pricedCached(now, key, func() bool {
		for _, candidate := range candidates {
			if snap.hasPrice(candidate, now, in.OfficialState) {
				return true
			}
		}
		return false
	})
	if priced {
		yes := true
		return QuoteAccess{OK: true, Priced: &yes}
	}

	block := s.runtime.policyValue(ctx, now, in.ReadPolicy) == BillingUnpricedPolicyBlockAllowlist
	s.runtime.observed.Add(1)
	if block {
		s.runtime.blocked.Add(1)
	}
	s.runtime.logLimited(now, "unpriced:"+candidates[0], func() {
		slog.Warn("billing.unpriced_admission",
			"group_id", groupID, "model", candidates[0], "candidates", candidates, "blocked", block)
	})
	no := false
	return QuoteAccess{OK: !block, Reason: QuoteAccessReasonUnpriced, Priced: &no}
}

// hasPrice 判断分组快照里某个模型有没有价格，按计费模式区分（B1）：判定本身是保存时校验用的 exposurePriceVerdict，
// 这里不再另写一份。生效窗口外的 custom 按 inherit 处理。只有「没有任何价格来源」才算无价：
// 0 元（zero_price）不算，它由保存时校验与已知免费名单管，运行时不因此拦截。
// 按次、图片模式的 custom 看按次价；inherit、extra 的图片模型（无 token 价）算有价。
func (s *matrixSnapshot) hasPrice(model string, at time.Time, officialState func(string) OfficialPriceState) bool {
	var cp *MatrixCustomPrice
	if cell := s.lookupCell(model); cell != nil && cell.mode == MatrixPriceCustom && cell.pricing != nil && cell.activeAt(at) {
		c := MatrixCustomPriceFromPricing(*cell.pricing)
		cp = &c
	}
	reason, bad := exposurePriceVerdict(cp, func() OfficialPriceState {
		if officialState == nil {
			return OfficialPriceState{}
		}
		return officialState(model)
	})
	return !bad || reason != ExposureUnpriced
}
