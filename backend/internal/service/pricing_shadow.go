package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// W6 PR5：影子比对的计数、采样与限流（设计 4.4、2.7）。
//
// 指标是进程内计数（项目里没有 Prometheus，与 W6 PR1 的无价计数同一做法），经管理端只读接口读出：
//   - pricing_shadow_compared_total{group_id}：实际执行过的比对次数；
//   - pricing_shadow_diff_total{group_id, kind, class}：发现的差异次数。class=translation 的必须为 0，
//     class=expected 是 v2 新语义带来的预期差异（单元格 open=false 的例外），不计入切换门槛；
//   - pricing_shadow_skipped_total{reason}：因为各种原因没有比对的次数（限流、快照兜底、沿用旧快照等）；
//   - pricing_shadow_panics_total：影子路径里被吞掉的 panic 次数。
// 差异样本写 pricing_shadow_diffs（有上限、保留 14 天），计数不靠这张表。

// 差异的类别（kind）。
const (
	ShadowKindAccess      = "access"
	ShadowKindMapping     = "mapping"
	ShadowKindFeature     = "feature"
	ShadowKindCost        = "cost"
	ShadowKindAccountCost = "account_cost"
)

// 差异的分类（class，设计 S-8）。
const (
	// ShadowClassTranslation 渠道到矩阵的翻译差异，切换门槛要求为 0。
	ShadowClassTranslation = "translation"
	// ShadowClassExpected v2 新语义带来的预期差异，单独计数、列清单，不计入门槛。
	ShadowClassExpected = "expected"
)

// 没有比对的原因（skipped 计数的维度）。
const (
	ShadowSkipRateLimited  = "rate_limited"
	ShadowSkipDegraded     = "snapshot_degraded"
	ShadowSkipStale        = "snapshot_stale"
	ShadowSkipRecentChange = "recent_change"
	ShadowSkipCold         = "snapshot_cold"
)

const (
	// 比对的全局限速（次/秒，每个进程）。逐次调用的比对（准入、映射、功能）只是几次 map 查找，上限放宽；
	// 成本比对要多算一遍完整的计费，上限收紧。超出的比对直接跳过并计数，不排队、不影响请求。
	shadowCallMaxPerSecond = 200
	shadowCostMaxPerSecond = 50

	// shadowRecentChangeGrace 渠道缓存或矩阵快照失效之后多久内不比对：渠道保存与派生写入之间有时间差
	// （派生钩子是保存之后的 best-effort 写入，PR4-1 审查给 PR5 第 5 条），这段时间两边的数据本来就不同步。
	shadowRecentChangeGrace = 20 * time.Second

	// 差异样本的采样上限：同一个（分组、类别、分类、模型）10 分钟内最多一条，全进程每小时最多 300 条，
	// 所以 14 天保留期内表里最多约 10 万行。
	shadowSampleMinInterval = 10 * time.Minute
	shadowSampleMaxPerHour  = 300
	shadowSampleKeyLimit    = 4096

	// 计数器维度的基数上限，防止异常数据撑爆内存；超出的维度并入 shadowOverflowKey。
	shadowCounterMaxKeys = 1024
	shadowViewMaxBytes   = 4096
)

// PricingShadowSample 一条差异样本，对应 pricing_shadow_diffs 的一行。
type PricingShadowSample struct {
	CreatedAt  time.Time       `json:"created_at"`
	GroupID    int64           `json:"group_id"`
	Model      string          `json:"model"`
	Kind       string          `json:"kind"`
	Class      string          `json:"class"`
	UsageRef   string          `json:"usage_ref,omitempty"`
	LegacyView json.RawMessage `json:"legacy_view"`
	V2View     json.RawMessage `json:"v2_view"`
}

// PricingShadowSink 接收差异样本。Enqueue 不阻塞，返回 false 表示丢弃（队列已满）。
type PricingShadowSink interface {
	Enqueue(sample PricingShadowSample) bool
}

// PricingShadowDiffCount 一个（分组、类别、分类）的差异次数。
type PricingShadowDiffCount struct {
	GroupID int64  `json:"group_id"`
	Kind    string `json:"kind"`
	Class   string `json:"class"`
	Count   int64  `json:"count"`
}

// PricingShadowComparedCount 一个分组的比对次数。
type PricingShadowComparedCount struct {
	GroupID int64 `json:"group_id"`
	Count   int64 `json:"count"`
}

// PricingShadowStats 影子比对的进程内计数。进程重启后清零，多实例各算各的。
type PricingShadowStats struct {
	// ComparedTotal 对应指标 pricing_shadow_compared_total{group_id}。
	ComparedTotal []PricingShadowComparedCount `json:"pricing_shadow_compared_total"`
	// DiffTotal 对应指标 pricing_shadow_diff_total{group_id, kind, class}。
	DiffTotal []PricingShadowDiffCount `json:"pricing_shadow_diff_total"`
	// SkippedTotal 对应指标 pricing_shadow_skipped_total{reason}。
	SkippedTotal map[string]int64 `json:"pricing_shadow_skipped_total"`
	// PanicsTotal 对应指标 pricing_shadow_panics_total。
	PanicsTotal int64 `json:"pricing_shadow_panics_total"`
	// SamplesDropped 因为队列已满而没有写入的差异样本数。
	SamplesDropped int64 `json:"samples_dropped"`
}

type shadowDiffKey struct {
	GroupID int64
	Kind    string
	Class   string
}

// shadowRateGate 每秒窗口计数的限速器。窗口切换时的竞争只会让一两次判断偏松或偏紧，不影响「有上限」。
type shadowRateGate struct {
	limit  int64
	window atomic.Int64
	count  atomic.Int64
}

func (g *shadowRateGate) allow(now time.Time) bool {
	sec := now.Unix()
	if w := g.window.Load(); w != sec && g.window.CompareAndSwap(w, sec) {
		g.count.Store(0)
	}
	return g.count.Add(1) <= g.limit
}

// shadowSampler 控制差异样本写入的速度。
type shadowSampler struct {
	mu       sync.Mutex
	last     map[shadowSampleKey]time.Time
	hourFrom time.Time
	hourN    int
}

type shadowSampleKey struct {
	GroupID int64
	Kind    string
	Class   string
	Model   string
}

func (s *shadowSampler) allow(key shadowSampleKey, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = make(map[shadowSampleKey]time.Time)
	}
	if now.Sub(s.hourFrom) >= time.Hour {
		s.hourFrom, s.hourN = now, 0
	}
	if s.hourN >= shadowSampleMaxPerHour {
		return false
	}
	if at, ok := s.last[key]; ok && now.Sub(at) < shadowSampleMinInterval {
		return false
	}
	if len(s.last) >= shadowSampleKeyLimit {
		for k, at := range s.last {
			if now.Sub(at) >= shadowSampleMinInterval {
				delete(s.last, k)
			}
		}
		if len(s.last) >= shadowSampleKeyLimit {
			return false
		}
	}
	s.last[key] = now
	s.hourN++
	return true
}

// pricingShadowHub 影子比对的计数、限速与采样。所有方法并发安全。
type pricingShadowHub struct {
	sink PricingShadowSink
	now  func() time.Time

	callGate shadowRateGate
	costGate shadowRateGate
	sampler  shadowSampler

	compared sync.Map // int64 -> *atomic.Int64
	diffs    sync.Map // shadowDiffKey -> *atomic.Int64
	skipped  sync.Map // string -> *atomic.Int64
	keys     atomic.Int64

	panics  atomic.Int64
	dropped atomic.Int64
}

func newPricingShadowHub(sink PricingShadowSink) *pricingShadowHub {
	h := &pricingShadowHub{sink: sink, now: time.Now}
	h.callGate.limit = shadowCallMaxPerSecond
	h.costGate.limit = shadowCostMaxPerSecond
	return h
}

func addCounter(m *sync.Map, key any, keys *atomic.Int64, overflow any) {
	if existing, ok := m.Load(key); ok {
		if c, ok := existing.(*atomic.Int64); ok {
			c.Add(1)
		}
		return
	}
	if keys.Load() >= shadowCounterMaxKeys {
		key = overflow
	}
	actual, loaded := m.LoadOrStore(key, &atomic.Int64{})
	if !loaded {
		keys.Add(1)
	}
	if c, ok := actual.(*atomic.Int64); ok {
		c.Add(1)
	}
}

func (h *pricingShadowHub) noteCompared(groupID int64) {
	addCounter(&h.compared, groupID, &h.keys, int64(0))
}

func (h *pricingShadowHub) noteSkipped(reason string) {
	addCounter(&h.skipped, reason, &h.keys, "_other")
}

func (h *pricingShadowHub) notePanic(where string, r any) {
	n := h.panics.Add(1)
	// 影子路径的 panic 不影响请求，只记指标；日志限频：前 5 次、之后每 1000 次一条。
	if n <= 5 || n%1000 == 0 {
		slog.Error("pricing shadow comparison panicked", "where", where, "panic", r, "total", n)
	}
}

// guard 运行 fn，吞掉 panic 并计数。返回 fn 是否正常返回。
func (h *pricingShadowHub) guard(where string, fn func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			h.notePanic(where, r)
			ok = false
		}
	}()
	fn()
	return true
}

// noteDiff 记一次差异：计数加一，并按采样规则写样本。views 序列化失败只丢样本，不影响计数。
func (h *pricingShadowHub) noteDiff(groupID int64, kind, class, model, usageRef string, legacyView, v2View any) {
	addCounter(&h.diffs, shadowDiffKey{GroupID: groupID, Kind: kind, Class: class}, &h.keys, shadowDiffKey{Kind: "_other", Class: "_other"})
	if h.sink == nil {
		return
	}
	now := h.now()
	if !h.sampler.allow(shadowSampleKey{GroupID: groupID, Kind: kind, Class: class, Model: model}, now) {
		return
	}
	lv, lerr := marshalShadowView(legacyView)
	vv, verr := marshalShadowView(v2View)
	if lerr != nil || verr != nil {
		return
	}
	if !h.sink.Enqueue(PricingShadowSample{
		CreatedAt: now, GroupID: groupID, Model: truncateShadowString(model, 200), Kind: kind, Class: class,
		UsageRef: truncateShadowString(usageRef, 64), LegacyView: lv, V2View: vv,
	}) {
		h.dropped.Add(1)
	}
	slog.Warn("pricing shadow diff", "group_id", groupID, "kind", kind, "class", class, "model", model)
}

func marshalShadowView(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(raw) > shadowViewMaxBytes {
		return json.Marshal(map[string]any{"truncated": true})
	}
	return raw, nil
}

func truncateShadowString(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= max {
		return s
	}
	// 按 rune 边界截断，不把多字节字符切成半个。
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Stats 返回当前进程的计数快照，排序固定。
func (h *pricingShadowHub) Stats() PricingShadowStats {
	out := PricingShadowStats{
		ComparedTotal: []PricingShadowComparedCount{},
		DiffTotal:     []PricingShadowDiffCount{},
		SkippedTotal:  map[string]int64{},
		PanicsTotal:   h.panics.Load(),
		SamplesDropped: h.dropped.Load(),
	}
	h.compared.Range(func(k, v any) bool {
		gid, ok1 := k.(int64)
		c, ok2 := v.(*atomic.Int64)
		if ok1 && ok2 {
			out.ComparedTotal = append(out.ComparedTotal, PricingShadowComparedCount{GroupID: gid, Count: c.Load()})
		}
		return true
	})
	h.diffs.Range(func(k, v any) bool {
		key, ok1 := k.(shadowDiffKey)
		c, ok2 := v.(*atomic.Int64)
		if ok1 && ok2 {
			out.DiffTotal = append(out.DiffTotal, PricingShadowDiffCount{GroupID: key.GroupID, Kind: key.Kind, Class: key.Class, Count: c.Load()})
		}
		return true
	})
	h.skipped.Range(func(k, v any) bool {
		name, ok1 := k.(string)
		c, ok2 := v.(*atomic.Int64)
		if ok1 && ok2 {
			out.SkippedTotal[name] = c.Load()
		}
		return true
	})
	sort.Slice(out.ComparedTotal, func(i, j int) bool { return out.ComparedTotal[i].GroupID < out.ComparedTotal[j].GroupID })
	sort.Slice(out.DiffTotal, func(i, j int) bool {
		a, b := out.DiffTotal[i], out.DiffTotal[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Class < b.Class
	})
	return out
}

// ---------------------------------------------------------------------------
// 差异样本的异步写入
// ---------------------------------------------------------------------------

// PricingShadowStore pricing_shadow_diffs 的数据访问。
type PricingShadowStore interface {
	// InsertDiffs 批量写入差异样本。
	InsertDiffs(ctx context.Context, samples []PricingShadowSample) error
	// PurgeDiffsBefore 删除创建时间早于 before 的样本，返回删除行数。
	PurgeDiffsBefore(ctx context.Context, before time.Time) (int64, error)
	// ListDiffs 按创建时间倒序读取样本，groupID 为 0 表示不限分组，limit 由调用方限定。
	ListDiffs(ctx context.Context, groupID int64, limit int) ([]PricingShadowSample, error)
}

const (
	// PricingShadowRetention 差异样本的保留期（设计 2.7）。
	PricingShadowRetention = 14 * 24 * time.Hour

	shadowQueueSize     = 256
	shadowFlushInterval = 2 * time.Second
	shadowPurgeInterval = time.Hour
	shadowStoreTimeout  = 10 * time.Second
	shadowListMaxLimit  = 200
)

// PricingShadowRecorder 把差异样本异步写进 pricing_shadow_diffs：有界队列，满了就丢（计数），
// 数据库慢或挂了只会让样本丢失，不会拖慢请求。同时负责按保留期清理旧样本。
type PricingShadowRecorder struct {
	store PricingShadowStore
	queue chan PricingShadowSample
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once

	writeFailures atomic.Int64
}

var _ PricingShadowSink = (*PricingShadowRecorder)(nil)

// NewPricingShadowRecorder 创建并启动后台写入协程（随进程存活；测试里用 Close 结束）。
func NewPricingShadowRecorder(store PricingShadowStore) *PricingShadowRecorder {
	r := &PricingShadowRecorder{
		store: store,
		queue: make(chan PricingShadowSample, shadowQueueSize),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go r.loop()
	return r
}

// Enqueue 实现 PricingShadowSink：队列满时返回 false。
func (r *PricingShadowRecorder) Enqueue(sample PricingShadowSample) bool {
	select {
	case r.queue <- sample:
		return true
	default:
		return false
	}
}

// Close 停止后台协程，并把队列里剩下的样本写完。可重复调用。
func (r *PricingShadowRecorder) Close() {
	r.once.Do(func() { close(r.stop) })
	<-r.done
}

// WriteFailures 返回写库失败的批次数。
func (r *PricingShadowRecorder) WriteFailures() int64 { return r.writeFailures.Load() }

// List 读取最近的差异样本，上限 200 条。
func (r *PricingShadowRecorder) List(ctx context.Context, groupID int64, limit int) ([]PricingShadowSample, error) {
	if limit <= 0 || limit > shadowListMaxLimit {
		limit = shadowListMaxLimit
	}
	return r.store.ListDiffs(ctx, groupID, limit)
}

func (r *PricingShadowRecorder) loop() {
	defer close(r.done)
	flush := time.NewTicker(shadowFlushInterval)
	purge := time.NewTicker(shadowPurgeInterval)
	defer flush.Stop()
	defer purge.Stop()
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("pricing shadow recorder panicked", "panic", rec)
		}
	}()

	var batch []PricingShadowSample
	write := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), shadowStoreTimeout)
		defer cancel()
		if err := r.store.InsertDiffs(ctx, batch); err != nil {
			r.writeFailures.Add(1)
			slog.Warn("write pricing shadow diffs failed", "samples", len(batch), "error", err)
		}
		batch = batch[:0]
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), shadowStoreTimeout)
		defer cancel()
		if _, err := r.store.PurgeDiffsBefore(ctx, time.Now().Add(-PricingShadowRetention)); err != nil {
			slog.Warn("purge pricing shadow diffs failed", "error", err)
		}
	}
	cleanup()
	for {
		select {
		case sample := <-r.queue:
			batch = append(batch, sample)
			if len(batch) >= shadowQueueSize {
				write()
			}
		case <-flush.C:
			write()
		case <-purge.C:
			cleanup()
		case <-r.stop:
			for {
				select {
				case sample := <-r.queue:
					batch = append(batch, sample)
				default:
					write()
					return
				}
			}
		}
	}
}
