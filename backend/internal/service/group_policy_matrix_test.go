//go:build unit

package service

// W6 PR4-1：matrixPolicy 的语义测试与快照缓存测试。
//
// 语义测试直接构造分组快照（不经过派生），覆盖 lookupCell 的查找规则、映射、准入、生效窗口、功能开关与成本核算；
// 缓存测试覆盖 TTL、singleflight、失效、加载失败。与 legacyPolicy 的逐点等价性在 group_policy_matrix_equiv_test.go。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 假的数据源、假的 pubsub、可控的时钟
// ---------------------------------------------------------------------------

type mpFakeSource struct {
	mu    sync.Mutex
	metas map[int64]DeriveGroup
	snaps map[int64]GroupStateSnapshot

	metaErr error
	snapErr error
	// afterLoad 在 LoadGroupSnapshots 读完数据、返回之前执行（不持锁），用来模拟慢查询：
	// 调用方已经拿到了「当时」的数据，而此刻数据库可能已经被别人改了。
	afterLoad func()

	metaCalls atomic.Int32
	snapCalls atomic.Int32
}

func newMPSource(platform string, snaps map[int64]GroupStateSnapshot) *mpFakeSource {
	src := &mpFakeSource{metas: map[int64]DeriveGroup{}, snaps: map[int64]GroupStateSnapshot{}}
	for gid, snap := range snaps {
		src.metas[gid] = DeriveGroup{ID: gid, Platform: platform}
		src.snaps[gid] = snap
	}
	return src
}

func (f *mpFakeSource) setSnapshot(gid int64, snap GroupStateSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snaps[gid] = snap
}

func (f *mpFakeSource) setErrors(metaErr, snapErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metaErr, f.snapErr = metaErr, snapErr
}

func (f *mpFakeSource) GetGroupMeta(ctx context.Context, ids []int64) (map[int64]DeriveGroup, error) {
	f.metaCalls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.metaErr != nil {
		return nil, f.metaErr
	}
	out := map[int64]DeriveGroup{}
	for _, id := range ids {
		if g, ok := f.metas[id]; ok {
			out[id] = g
		}
	}
	return out, nil
}

func (f *mpFakeSource) LoadGroupSnapshots(ctx context.Context, ids []int64) (map[int64]GroupStateSnapshot, error) {
	f.snapCalls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	if f.snapErr != nil {
		err := f.snapErr
		f.mu.Unlock()
		return nil, err
	}
	out := map[int64]GroupStateSnapshot{}
	for _, id := range ids {
		if snap, ok := f.snaps[id]; ok {
			out[id] = snap
		}
	}
	f.mu.Unlock()
	if f.afterLoad != nil {
		f.afterLoad()
	}
	return out, nil
}

// mpFakePubSub 把通知同步分发给所有订阅者，模拟「同一个 Redis 频道上的多个实例」。
type mpFakePubSub struct {
	mu       sync.Mutex
	handlers []func()
	notifies int
}

func (f *mpFakePubSub) NotifyUpdate(context.Context) error {
	f.mu.Lock()
	f.notifies++
	handlers := append([]func(){}, f.handlers...)
	f.mu.Unlock()
	for _, h := range handlers {
		h()
	}
	return nil
}

func (f *mpFakePubSub) SubscribeUpdates(_ context.Context, handler func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append(f.handlers, handler)
}

func (f *mpFakePubSub) notifyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notifies
}

// fireFromOtherInstance 模拟收到其他实例发来的失效通知：只触发订阅者，不计入本实例发布的次数。
func (f *mpFakePubSub) fireFromOtherInstance() {
	f.mu.Lock()
	handlers := append([]func(){}, f.handlers...)
	f.mu.Unlock()
	for _, h := range handlers {
		h()
	}
}

type mpClock struct{ ns atomic.Int64 }

func (c *mpClock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *mpClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

var mpT0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func newMPForTest(src MatrixSnapshotSource, ps ChannelCachePubSub) (*matrixPolicy, *mpClock) {
	p := NewMatrixGroupPolicy(src, ps)
	clock := &mpClock{}
	clock.ns.Store(mpT0.UnixNano())
	p.now = clock.Now
	return p, clock
}

// ---------------------------------------------------------------------------
// 构造快照数据的小工具
// ---------------------------------------------------------------------------

func mpF(v float64) *float64 { return &v }
func mpS(v string) *string   { return &v }

func mpCellBase(key string, mode MatrixPriceMode) StoredMatrixCell {
	return StoredMatrixCell{GroupID: 1, MatrixCell: MatrixCell{ModelKey: key, Open: true, PriceMode: mode, Source: MatrixSourceManual}}
}

func mpInherit(key string) StoredMatrixCell { return mpCellBase(key, MatrixPriceInherit) }

func mpExtra(key string, x float64) StoredMatrixCell {
	c := mpCellBase(key, MatrixPriceExtra)
	c.ExtraMultiplier = mpF(x)
	return c
}

func mpCustom(key string, input float64) StoredMatrixCell {
	c := mpCellBase(key, MatrixPriceCustom)
	c.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: mpF(input), OutputPrice: mpF(input * 4)}
	return c
}

func mpPattern(c StoredMatrixCell, order int) StoredMatrixCell {
	c.IsPattern = true
	c.PatternOrder = order
	return c
}

func mpClosed(c StoredMatrixCell) StoredMatrixCell {
	c.Open = false
	return c
}

func mpWindow(c StoredMatrixCell, from, to *time.Time) StoredMatrixCell {
	c.EffectiveFrom, c.EffectiveTo = from, to
	return c
}

func mpStoredConfig(stage PricingStage, mutate func(*MatrixGroupConfig)) *StoredGroupConfig {
	cfg := defaultMatrixGroupConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	return &StoredGroupConfig{GroupID: 1, MatrixGroupConfig: cfg, PricingStage: stage}
}

// newMPPolicyFor 用一个分组的快照数据构造 matrixPolicy（分组平台 openai，分组 id 为 1）。
func newMPPolicyFor(snap GroupStateSnapshot) *matrixPolicy {
	p, _ := newMPForTest(newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: snap}), nil)
	return p
}

func mpInput(t *testing.T, p *ChannelModelPricing) float64 {
	t.Helper()
	require.NotNil(t, p)
	require.NotNil(t, p.InputPrice)
	return *p.InputPrice
}

// ---------------------------------------------------------------------------
// lookupCell：字面名优先，命中任何模式的单元格都停止
// ---------------------------------------------------------------------------

func TestMatrixPolicy_LiteralHitStopsLookupEvenForInherit(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{
		// A：基名带价，变体 high 显式 inherit。
		mpCustom("gpt-5.6-luna", 0.4e-6),
		mpInherit("gpt-5.6-luna-high"),
		// B：基名与 mini 各有额外倍率。
		mpExtra("gpt-5.4", 1.5),
		mpExtra("gpt-5.4-mini", 3),
		// C：基名带价，变体 high 是 extra 单元格：PriceOverride 对 extra 返回 nil，不能再落到基名的价。
		mpCustom("gpt-5.5", 0.9e-6),
		mpExtra("gpt-5.5-high", 2),
		// D：通配符 inherit 在字面名这一步就命中，不能再回落到归一化后的基名。
		mpCustom("gpt-5.2", 0.7e-6),
		mpPattern(mpInherit("gpt-5.2"), 0),
	}})
	zero := time.Time{}

	// A
	require.InDelta(t, 0.4e-6, mpInput(t, p.PriceOverride(ctx, 1, "gpt-5.6-luna", zero)), 1e-15)
	require.Nil(t, p.PriceOverride(ctx, 1, "gpt-5.6-luna-high", zero), "literal inherit cell must stop the lookup")
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "gpt-5.6-luna-high", zero))
	require.InDelta(t, 0.4e-6, mpInput(t, p.PriceOverride(ctx, 1, "gpt-5.6-luna-xhigh", zero)), 1e-15,
		"a variant without a literal cell falls back to the base cell")

	// B：变体名使用基名的额外倍率；字面名上的单元格优先。
	require.Equal(t, 1.5, p.ExtraMultiplier(ctx, 1, "gpt-5.4", zero))
	require.Equal(t, 1.5, p.ExtraMultiplier(ctx, 1, "gpt-5.4-high", zero))
	require.Equal(t, 3.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4-mini", zero))
	require.Equal(t, 3.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4-mini-high", zero), "the variant normalizes to gpt-5.4-mini, not to gpt-5.4")
	require.Nil(t, p.PriceOverride(ctx, 1, "gpt-5.4-mini", zero), "extra cells carry no price override")

	// C
	require.InDelta(t, 0.9e-6, mpInput(t, p.PriceOverride(ctx, 1, "gpt-5.5", zero)), 1e-15)
	require.Nil(t, p.PriceOverride(ctx, 1, "gpt-5.5-high", zero), "a literal extra cell must not fall back to the base custom price")
	require.Equal(t, 2.0, p.ExtraMultiplier(ctx, 1, "gpt-5.5-high", zero))
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "gpt-5.5", zero), "custom and extra are exclusive")

	// D
	require.InDelta(t, 0.7e-6, mpInput(t, p.PriceOverride(ctx, 1, "gpt-5.2", zero)), 1e-15, "exact beats the wildcard")
	require.Nil(t, p.PriceOverride(ctx, 1, "gpt-5.2-high", zero), "a literal wildcard hit also stops the lookup")
}

func TestMatrixPolicy_NoCellMeansOfficialPriceAndNoExtra(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{})
	require.Nil(t, p.PriceOverride(ctx, 1, "gpt-5.6-luna", time.Time{}))
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "gpt-5.6-luna", time.Time{}))
	require.Nil(t, p.PriceOverride(ctx, 1, "", time.Time{}))
	// 不属于任何配置的分组：同样是默认状态。
	require.Nil(t, p.PriceOverride(ctx, 404, "gpt-5.6-luna", time.Time{}))
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 404, "gpt-5.6-luna", time.Time{}))
}

func TestMatrixPolicy_PriceOverrideReturnsIndependentCopy(t *testing.T) {
	ctx := context.Background()
	cell := mpCustom("gpt-5.6-luna", 0.4e-6)
	cell.CustomPrice.Intervals = []MatrixPriceInterval{{MinTokens: 0, InputPrice: mpF(0.4e-6), TierLabel: "base"}}
	p := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{cell}})

	first := p.PriceOverride(ctx, 1, "gpt-5.6-luna", time.Time{})
	require.NotNil(t, first)
	require.Equal(t, BillingModeToken, first.BillingMode)
	require.Equal(t, PlatformOpenAI, first.Platform)
	require.Equal(t, []string{"gpt-5.6-luna"}, first.Models)
	require.Len(t, first.Intervals, 1)
	first.Intervals[0].TierLabel = "mutated"
	first.Models[0] = "mutated"

	second := p.PriceOverride(ctx, 1, "gpt-5.6-luna", time.Time{})
	require.Equal(t, "base", second.Intervals[0].TierLabel, "callers must not be able to pollute the snapshot")
	require.Equal(t, []string{"gpt-5.6-luna"}, second.Models)
}

// ---------------------------------------------------------------------------
// 通配符：单元格按 pattern_order，先匹配者优先；映射按前缀长度，长者优先。两套规则不共用。
// ---------------------------------------------------------------------------

func TestMatrixPolicy_CellWildcardsFollowPatternOrder(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{
		// 故意倒序给出：快照按 pattern_order 重排。
		mpPattern(mpExtra("gpt-5.4", 3), 1),
		mpPattern(mpExtra("gpt-5", 2), 0),
		mpInherit("gpt-5.4-mini"),
	}})
	zero := time.Time{}
	require.Equal(t, 2.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4-nano", zero), "the first wildcard by pattern_order wins, even if it is less specific")
	require.Equal(t, 2.0, p.ExtraMultiplier(ctx, 1, "gpt-5.2", zero))
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4-mini", zero), "an exact cell beats every wildcard")
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "claude-opus-4-5", zero))
}

func TestMatrixPolicy_MappingWildcardsFollowPrefixLength(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceChannelMapped)
		// 故意不按规范顺序给出：快照按「长者优先、同长字典序」重排。
		c.ModelMapping = []MatrixMappingEntry{
			{Src: "gpt-5*", Dst: "short"},
			{Src: "gpt-5.4-mini*", Dst: "longest"},
			{Src: "gpt-5.4*", Dst: "middle"},
			{Src: "o3-pro", Dst: "exact"},
			{Src: "o3*", Dst: "o3-wild"},
		}
	})})

	for model, want := range map[string]string{
		"gpt-5.4-mini-high": "longest",
		"gpt-5.4-nano":      "middle",
		"gpt-5.2":           "short",
		"o3-pro":            "exact",
		"O3-PRO":            "exact",
		"  o3-pro  ":        "exact",
		"o3-mini":           "o3-wild",
	} {
		got := p.Mapping(ctx, 1, model)
		require.True(t, got.Mapped, model)
		require.Equal(t, want, got.MappedModel, model)
		require.Equal(t, BillingModelSourceChannelMapped, got.BillingModelSource, model)
		require.Zero(t, got.ChannelID, "v2 groups never write a channel id")
	}
	unmapped := p.Mapping(ctx, 1, "claude-sonnet-4-5")
	require.Equal(t, ChannelMappingResult{MappedModel: "claude-sonnet-4-5", BillingModelSource: BillingModelSourceChannelMapped}, unmapped)
}

func TestMatrixPolicy_MappingEmptyDestinationStopsLookup(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceRequested)
		c.ModelMapping = []MatrixMappingEntry{
			{Src: "gpt-5.5", Dst: ""},        // 精确名命中，dst 为空：不映射，也不再查通配符
			{Src: "gpt-5.*", Dst: "gpt-5.4"}, // 否则 gpt-5.5 会被它映射走
			{Src: "gpt-6.1*", Dst: ""},       // 通配符的 target 为空同样停止
			{Src: "gpt-6*", Dst: "gpt-6-astra"},
		}
	})})

	for _, model := range []string{"gpt-5.5", "GPT-5.5", " gpt-5.5 ", "gpt-6.1-sol", "gpt-6.1"} {
		got := p.Mapping(ctx, 1, model)
		require.False(t, got.Mapped, model)
		require.Equal(t, model, got.MappedModel, model)
	}
	require.Equal(t, "gpt-5.4", p.Mapping(ctx, 1, "gpt-5.3-codex").MappedModel)
	require.Equal(t, "gpt-6-astra", p.Mapping(ctx, 1, "gpt-6-luna").MappedModel)
}

func TestMatrixPolicy_MappingCollisionsAndBillingSource(t *testing.T) {
	ctx := context.Background()
	// 小写后同键：数组里靠后的覆盖靠前的（派生按 src 字节序写入，所以后者就是字节序靠后的）。
	p := newMPPolicyFor(GroupStateSnapshot{Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceUpstream)
		c.ModelMapping = []MatrixMappingEntry{
			{Src: "GPT-5.5", Dst: "first"}, {Src: "gpt-5.5", Dst: "second"},
			{Src: "GPT-5.4*", Dst: "wild-first"}, {Src: "gpt-5.4*", Dst: "wild-second"},
		}
	})})
	require.Equal(t, "second", p.Mapping(ctx, 1, "gpt-5.5").MappedModel)
	require.Equal(t, "wild-second", p.Mapping(ctx, 1, "gpt-5.4-mini").MappedModel)
	require.Equal(t, BillingModelSourceUpstream, p.Mapping(ctx, 1, "gpt-5.5").BillingModelSource)

	// 没有渠道（billing_model_source 为 NULL）：原样返回空串与渠道 ID 0（设计 S-1）。
	none := newMPPolicyFor(GroupStateSnapshot{})
	require.Equal(t, ChannelMappingResult{MappedModel: "gpt-5.5"}, none.Mapping(ctx, 1, "gpt-5.5"))
}

// ---------------------------------------------------------------------------
// 准入
// ---------------------------------------------------------------------------

func TestMatrixPolicy_ModelAccessOpenGroup(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{
		mpClosed(mpInherit("gpt-5.4")),
		mpClosed(mpPattern(mpInherit("o1"), 0)),
		mpExtra("gpt-5.5", 2), // 开着的 extra 单元格不影响准入
	}})
	require.True(t, p.ModelAccess(ctx, 1, "unknown-model").OK, "open group admits everything without an exception")
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK)
	for _, model := range []string{"gpt-5.4", "GPT-5.4", "  gpt-5.4  ", "o1-mini"} {
		got := p.ModelAccess(ctx, 1, model)
		require.False(t, got.OK, model)
		require.Equal(t, QuoteAccessReasonClosedInGroup, got.Reason, model)
	}
	// 只查字面名：基名被关闭，不影响它的变体（变体名走的是价格路径的第二步，不是准入）。
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4-high").OK)
}

func TestMatrixPolicy_ModelAccessAllowlistGroup(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{
		Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }),
		Cells: []StoredMatrixCell{
			mpInherit("gpt-5.6-luna"), // 价格字段为空等于按官方价开放
			mpCustom("claude-sonnet-4-5", 3e-6),
			mpPattern(mpInherit("gpt-5.4"), 0),
			mpClosed(mpInherit("gpt-5.4-nano")), // 精确名先于通配符：关闭的精确名挡住通配符的放行
		},
	})
	for _, model := range []string{"gpt-5.6-luna", " GPT-5.6-LUNA ", "Claude-Sonnet-4.5", "gpt-5.4-mini"} {
		require.True(t, p.ModelAccess(ctx, 1, model).OK, model)
	}
	got := p.ModelAccess(ctx, 1, "unknown-model")
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}, got)
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}, p.ModelAccess(ctx, 1, ""))
	// 不做 codex 归一化：变体不会借基名的单元格放行。
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}, p.ModelAccess(ctx, 1, "gpt-5.6-luna-high"))
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}, p.ModelAccess(ctx, 1, "gpt-5.4-nano"))
}

func TestMatrixPolicy_UpstreamAccessIsSeparateFromModelAccess(t *testing.T) {
	ctx := context.Background()
	cells := []StoredMatrixCell{mpInherit("gpt-5.6-luna"), mpClosed(mpInherit("gpt-5.4"))}

	allow := newMPPolicyFor(GroupStateSnapshot{
		Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
			c.AccessMode = MatrixAccessAllowlist
			c.BillingModelSource = mpS(BillingModelSourceUpstream)
		}),
		Cells: cells,
	})
	require.True(t, allow.UpstreamAccess(ctx, 1, "gpt-5.6-luna").OK)
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}, allow.UpstreamAccess(ctx, 1, "unknown-model"))
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}, allow.UpstreamAccess(ctx, 1, "gpt-5.4"), "a closed cell is not an allowlist member")

	// 开放分组：open=false 的例外是面向用户的关闭，不约束账号映射后的上游模型。
	open := newMPPolicyFor(GroupStateSnapshot{Cells: cells})
	require.False(t, open.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.True(t, open.UpstreamAccess(ctx, 1, "gpt-5.4").OK)
	require.True(t, open.UpstreamAccess(ctx, 1, "unknown-model").OK)
}

func TestMatrixPolicy_UpstreamCheck(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		mode MatrixAccessMode
		bms  *string
		want bool
	}{
		{"allowlist + upstream", MatrixAccessAllowlist, mpS(BillingModelSourceUpstream), true},
		{"allowlist + channel_mapped", MatrixAccessAllowlist, mpS(BillingModelSourceChannelMapped), false},
		{"allowlist + requested", MatrixAccessAllowlist, mpS(BillingModelSourceRequested), false},
		{"open + upstream", MatrixAccessOpen, mpS(BillingModelSourceUpstream), false},
		{"allowlist without a channel", MatrixAccessAllowlist, nil, false},
	}
	for _, tc := range cases {
		p := newMPPolicyFor(GroupStateSnapshot{Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
			c.AccessMode = tc.mode
			c.BillingModelSource = tc.bms
		})})
		got, err := p.UpstreamCheck(ctx, 1)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.want, got, tc.name)
	}
}

// ---------------------------------------------------------------------------
// 生效窗口：只作用于价格覆盖，不作用于开放状态；窗口外按 inherit 处理，字面名命中仍然停止
// ---------------------------------------------------------------------------

func TestMatrixPolicy_EffectiveWindowAppliesToPriceOnly(t *testing.T) {
	ctx := context.Background()
	from, to := mpT0.Add(time.Hour), mpT0.Add(2*time.Hour)
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: {Cells: []StoredMatrixCell{
		mpWindow(mpExtra("gpt-5.4", 2), &from, &to),
		mpWindow(mpCustom("gpt-5.5", 0.9e-6), &from, &to),
		// 变体的字面名单元格在窗口外：按 inherit 处理，但仍然停止查找，不回落到基名。
		mpExtra("gpt-5.6-luna", 4),
		mpWindow(mpExtra("gpt-5.6-luna-high", 2), &from, &to),
		// 窗口不管开放状态：窗口外的关闭仍然是关闭。
		mpWindow(mpClosed(mpInherit("o3")), &from, &to),
	}}})
	p, clock := newMPForTest(src, nil)

	before := mpT0
	inside := mpT0.Add(90 * time.Minute)
	for at, want := range map[time.Time]float64{before: 1, from.Add(-time.Nanosecond): 1, from: 2, inside: 2, to.Add(-time.Nanosecond): 2, to: 1, to.Add(time.Hour): 1} {
		require.Equal(t, want, p.ExtraMultiplier(ctx, 1, "gpt-5.4", at), "at=%s", at)
	}
	require.Nil(t, p.PriceOverride(ctx, 1, "gpt-5.5", before))
	require.InDelta(t, 0.9e-6, mpInput(t, p.PriceOverride(ctx, 1, "gpt-5.5", inside)), 1e-15)
	require.Nil(t, p.PriceOverride(ctx, 1, "gpt-5.5", to))

	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "gpt-5.6-luna-high", before), "outside the window the literal cell stops the lookup at inherit")
	require.Equal(t, 2.0, p.ExtraMultiplier(ctx, 1, "gpt-5.6-luna-high", inside))
	require.Equal(t, 4.0, p.ExtraMultiplier(ctx, 1, "gpt-5.6-luna-xhigh", before), "a variant without a literal cell still uses the base")

	require.False(t, p.ModelAccess(ctx, 1, "o3").OK, "the window does not affect openness")

	// 计费时点为零值时取当前时间（resolver 目前不传时点）。
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4", time.Time{}))
	clock.Advance(90 * time.Minute)
	require.Equal(t, 2.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4", time.Time{}))
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4", before), "an explicit time wins over the clock")
}

func TestMatrixPolicy_InvalidCellsAreTreatedAsInherit(t *testing.T) {
	ctx := context.Background()
	bad := mpCellBase("gpt-5.4", MatrixPriceExtra) // 缺倍率
	zero := mpExtra("gpt-5.5", 0)                  // 倍率不大于 0
	noPrice := mpCellBase("gpt-5.2", MatrixPriceCustom)
	p := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{bad, zero, noPrice}})
	for _, model := range []string{"gpt-5.4", "gpt-5.5", "gpt-5.2"} {
		require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, model, time.Time{}), model)
		require.Nil(t, p.PriceOverride(ctx, 1, model, time.Time{}), model)
	}
}

// ---------------------------------------------------------------------------
// 功能开关、成本模式、阶段
// ---------------------------------------------------------------------------

func TestMatrixPolicy_FeatureShapes(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
		c.Features = map[string]any{
			featureKeyWebSearchEmulation:         map[string]any{PlatformAnthropic: true, PlatformOpenAI: false},
			featureKeyBedrockCCCompat:            true,
			featureKeyCodexImageGenerationBridge: false,
		}
	})})
	boolOf := func(v *bool, err error) string {
		require.NoError(t, err)
		switch {
		case v == nil:
			return "nil"
		case *v:
			return "true"
		}
		return "false"
	}

	require.Equal(t, "true", boolOf(p.Feature(ctx, 1, PlatformAnthropic, GroupFeatureWebSearchEmulation)))
	require.Equal(t, "false", boolOf(p.Feature(ctx, 1, PlatformOpenAI, GroupFeatureWebSearchEmulation)))
	require.Equal(t, "false", boolOf(p.Feature(ctx, 1, "gemini", GroupFeatureWebSearchEmulation)), "the switch exists but has no entry for this platform")
	require.Equal(t, "true", boolOf(p.Feature(ctx, 1, PlatformAnthropic, GroupFeatureBedrockCCCompat)))
	require.Equal(t, "true", boolOf(p.Feature(ctx, 1, "", GroupFeatureBedrockCCCompat)), "a bare bool does not depend on the platform")
	require.Equal(t, "false", boolOf(p.Feature(ctx, 1, PlatformOpenAI, GroupFeatureCodexImageGenerationBridge)), "a bare false is an explicit override")
	require.Equal(t, "nil", boolOf(p.Feature(ctx, 1, PlatformOpenAI, GroupFeature("no_such_feature"))))

	// 开关不存在：没有显式设置。
	empty := newMPPolicyFor(GroupStateSnapshot{})
	for _, f := range []GroupFeature{GroupFeatureWebSearchEmulation, GroupFeatureBedrockCCCompat, GroupFeatureCodexImageGenerationBridge} {
		require.Equal(t, "nil", boolOf(empty.Feature(ctx, 1, PlatformOpenAI, f)), string(f))
	}

	// codex 图片桥也认按平台的 map 形状（platformBoolOverride 的读法）。
	perPlatform := newMPPolicyFor(GroupStateSnapshot{Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
		c.Features = map[string]any{featureKeyCodexImageGenerationBridge: map[string]any{PlatformOpenAI: true}}
	})})
	require.Equal(t, "true", boolOf(perPlatform.Feature(ctx, 1, PlatformOpenAI, GroupFeatureCodexImageGenerationBridge)))
	require.Equal(t, "nil", boolOf(perPlatform.Feature(ctx, 1, PlatformAnthropic, GroupFeatureCodexImageGenerationBridge)))
}

func TestMatrixPolicy_CostModeAndStage(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		mode  MatrixCostMode
		stage PricingStage
	}{
		{MatrixCostAccountRate, PricingStageLegacy},
		{MatrixCostCatalogUpstream, PricingStageShadow},
		{MatrixCostFollowBilling, PricingStageV2},
	} {
		p := newMPPolicyFor(GroupStateSnapshot{Config: mpStoredConfig(tc.stage, func(c *MatrixGroupConfig) { c.CostMode = tc.mode })})
		require.Equal(t, tc.mode, p.CostMode(ctx, 1))
		require.Equal(t, tc.stage, p.Stage(ctx, 1))
	}
	// 没有配置行：默认值。
	none := newMPPolicyFor(GroupStateSnapshot{})
	require.Equal(t, MatrixCostAccountRate, none.CostMode(ctx, 1))
	require.Equal(t, PricingStageLegacy, none.Stage(ctx, 1))
}

// ---------------------------------------------------------------------------
// 成本核算：匹配语义（PR2 审查提醒 2.4）
// ---------------------------------------------------------------------------

func mpRule(id int64, sortOrder, ordinal int, accounts, groups []int64, prices ...MatrixCostRulePrice) StoredMatrixCostRule {
	return StoredMatrixCostRule{
		ID: id, ScopeGroupID: 1, Source: MatrixSourceLegacyDerived,
		MatrixCostRule: MatrixCostRule{
			Name: "rule", SourceChannelID: 9, SourceOrdinal: ordinal, SortOrder: sortOrder, Enabled: true,
			GroupIDs: groups, AccountIDs: accounts, Prices: prices,
		},
	}
}

func mpRulePrice(platform string, input float64, models ...string) MatrixCostRulePrice {
	return MatrixCostRulePrice{Platform: platform, Models: models, Price: MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: mpF(input)}}
}

// mpStatsCost 走完整的 resolveAccountStatsCost（优先级 1：自定义规则，优先级 2：跟随计费），不带官方价目录。
func mpStatsCost(p GroupPolicy, accountID int64, model string, totalCost float64) *float64 {
	return resolveAccountStatsCost(context.Background(), p, nil, accountID, 1, model,
		UsageTokens{InputTokens: 1000}, 1, totalCost, "", time.Time{})
}

func TestMatrixPolicy_CostRulesMatching(t *testing.T) {
	ctx := context.Background()
	cfg := mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) { c.CostMode = MatrixCostCatalogUpstream })

	t.Run("account or group hit, both empty never matches", func(t *testing.T) {
		p := newMPPolicyFor(GroupStateSnapshot{Config: cfg, Rules: []StoredMatrixCostRule{
			mpRule(1, 0, 1, []int64{7}, nil, mpRulePrice("", 0.001, "gpt-5.6-luna")),
			mpRule(2, 0, 2, nil, []int64{1}, mpRulePrice("", 0.002, "gpt-5.4")),
			mpRule(3, 0, 3, nil, nil, mpRulePrice("", 0.003, "gpt-5.5")),
		}})
		require.InDelta(t, 1.0, *mpStatsCost(p, 7, "gpt-5.6-luna", 0), 1e-12, "account hit")
		require.Nil(t, mpStatsCost(p, 8, "gpt-5.6-luna", 0), "neither the account nor the rule's group list matches")
		require.InDelta(t, 2.0, *mpStatsCost(p, 8, "gpt-5.4", 0), 1e-12, "the rule's own group list contains the request group")
		require.Nil(t, mpStatsCost(p, 7, "gpt-5.5", 0), "a rule with neither accounts nor groups matches nothing")
	})

	t.Run("rules are ordered by sort_order, source_ordinal (manual last), id; first match wins", func(t *testing.T) {
		p := newMPPolicyFor(GroupStateSnapshot{Config: cfg, Rules: []StoredMatrixCostRule{
			mpRule(10, 5, 0, []int64{7}, nil, mpRulePrice("", 0.005, "gpt-5.6-luna")), // manual（ordinal 0）排在同 sort_order 的派生行后面
			mpRule(11, 5, 2, []int64{7}, nil, mpRulePrice("", 0.004, "gpt-5.6-luna")),
			mpRule(12, 5, 1, []int64{7}, nil, mpRulePrice("", 0.003, "gpt-5.6-luna")),
			mpRule(13, 1, 9, []int64{7}, nil, mpRulePrice("", 0.001, "gpt-5.4")),
		}})
		rules, _ := p.CostRules(ctx, 1)
		var ids []int64
		for _, r := range rules {
			ids = append(ids, r.ID)
		}
		require.Equal(t, []int64{13, 12, 11, 10}, ids)
		require.InDelta(t, 3.0, *mpStatsCost(p, 7, "gpt-5.6-luna", 0), 1e-12, "the first rule that has the model wins")
		require.InDelta(t, 1.0, *mpStatsCost(p, 7, "gpt-5.4", 0), 1e-12, "a matching rule without the model falls through to the next one")
	})

	t.Run("inside a rule: exact before wildcard, first exact wins, no claude dot normalization, empty platform matches any", func(t *testing.T) {
		p := newMPPolicyFor(GroupStateSnapshot{Config: cfg, Rules: []StoredMatrixCostRule{
			mpRule(1, 0, 1, []int64{7}, nil,
				mpRulePrice("", 0.009, "gpt-5.4*"), // 通配符在前，但精确名先于通配符
				mpRulePrice("", 0.002, "gpt-5.4-mini"),
				mpRulePrice("", 0.007, "gpt-5.4-mini"), // 精确名重复：先到先得
				mpRulePrice(PlatformOpenAI, 0.003, "gpt-5.5"),
				mpRulePrice(PlatformAnthropic, 0.006, "claude-sonnet-4.5"),
			),
		}})
		require.InDelta(t, 2.0, *mpStatsCost(p, 7, "gpt-5.4-mini", 0), 1e-12)
		require.InDelta(t, 9.0, *mpStatsCost(p, 7, "gpt-5.4-nano", 0), 1e-12, "wildcard")
		require.InDelta(t, 3.0, *mpStatsCost(p, 7, "GPT-5.5", 0), 1e-12, "matched case-insensitively; the group platform is openai")
		require.Nil(t, mpStatsCost(p, 7, "claude-sonnet-4.5", 0), "the price entry is for another platform than the group's")

		dot := newMPPolicyFor(GroupStateSnapshot{Config: cfg, Rules: []StoredMatrixCostRule{
			mpRule(1, 0, 1, []int64{7}, nil, mpRulePrice("", 0.006, "claude-sonnet-4.5")),
		}})
		require.InDelta(t, 6.0, *mpStatsCost(dot, 7, "claude-sonnet-4.5", 0), 1e-12)
		require.Nil(t, mpStatsCost(dot, 7, "claude-sonnet-4-5", 0), "cost rules do not normalize claude dots")
	})

	t.Run("scope and enabled", func(t *testing.T) {
		other := mpRule(1, 0, 1, []int64{7}, nil, mpRulePrice("", 0.001, "gpt-5.6-luna"))
		other.ScopeGroupID = 2
		disabled := mpRule(2, 0, 2, []int64{7}, nil, mpRulePrice("", 0.002, "gpt-5.6-luna"))
		disabled.Enabled = false
		p := newMPPolicyFor(GroupStateSnapshot{Config: cfg, Rules: []StoredMatrixCostRule{other, disabled}})
		rules, _ := p.CostRules(ctx, 1)
		require.Empty(t, rules)
		require.Nil(t, mpStatsCost(p, 7, "gpt-5.6-luna", 0))
	})

	t.Run("cost mode gates the rules", func(t *testing.T) {
		rules := []StoredMatrixCostRule{mpRule(1, 0, 1, []int64{7}, nil, mpRulePrice("", 0.001, "gpt-5.6-luna"))}
		accountRate := newMPPolicyFor(GroupStateSnapshot{Rules: rules}) // 没有配置行：account_rate
		require.Nil(t, mpStatsCost(accountRate, 7, "gpt-5.6-luna", 5), "account_rate ignores custom rules")

		follow := newMPPolicyFor(GroupStateSnapshot{
			Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) { c.CostMode = MatrixCostFollowBilling }),
			Rules:  rules,
		})
		require.InDelta(t, 1.0, *mpStatsCost(follow, 7, "gpt-5.6-luna", 5), 1e-12, "rules come first")
		require.InDelta(t, 5.0, *mpStatsCost(follow, 8, "gpt-5.6-luna", 5), 1e-12, "follow_billing falls back to the customer's total cost")
	})

	t.Run("CostRules returns the group platform and a deep copy", func(t *testing.T) {
		p := newMPPolicyFor(GroupStateSnapshot{Config: cfg, Rules: []StoredMatrixCostRule{
			mpRule(1, 0, 1, []int64{7}, []int64{1}, mpRulePrice("", 0.001, "gpt-5.6-luna")),
		}})
		rules, platform := p.CostRules(ctx, 1)
		require.Equal(t, PlatformOpenAI, platform)
		require.Len(t, rules, 1)
		rules[0].AccountIDs[0] = 12345
		rules[0].Pricing[0].Models[0] = "mutated"
		again, _ := p.CostRules(ctx, 1)
		require.Equal(t, []int64{7}, again[0].AccountIDs)
		require.Equal(t, []string{"gpt-5.6-luna"}, again[0].Pricing[0].Models)
	})
}

// ---------------------------------------------------------------------------
// 快照缓存：TTL、singleflight、失效
// ---------------------------------------------------------------------------

func mpAllowlistSnapshot(models ...string) GroupStateSnapshot {
	cells := make([]StoredMatrixCell, 0, len(models))
	for _, m := range models {
		cells = append(cells, mpInherit(m))
	}
	return GroupStateSnapshot{
		Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }),
		Cells:  cells,
	}
}

func TestMatrixPolicy_SnapshotIsCachedUntilTTL(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
	p, clock := newMPForTest(src, nil)

	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.False(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK)
	require.EqualValues(t, 1, src.snapCalls.Load(), "one load serves every read inside the TTL")

	src.setSnapshot(1, mpAllowlistSnapshot("gpt-5.5"))
	clock.Advance(matrixSnapshotTTL - time.Second)
	require.False(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK, "still the cached snapshot")
	require.EqualValues(t, 1, src.snapCalls.Load())

	clock.Advance(2 * time.Second)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK, "the snapshot is reloaded after the TTL")
	require.False(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.EqualValues(t, 2, src.snapCalls.Load())
	require.Equal(t, MatrixSnapshotStats{Loads: 2}, p.Stats())
}

func TestMatrixPolicy_SnapshotsAreKeptPerGroup(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{
		1: mpAllowlistSnapshot("gpt-5.4"),
		2: mpAllowlistSnapshot("gpt-5.5"),
	})
	p, _ := newMPForTest(src, nil)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.False(t, p.ModelAccess(ctx, 2, "gpt-5.4").OK)
	require.True(t, p.ModelAccess(ctx, 2, "gpt-5.5").OK)
	require.EqualValues(t, 2, src.snapCalls.Load())
}

func TestMatrixPolicy_ConcurrentColdReadsLoadOnce(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
	release := make(chan struct{})
	entered := make(chan struct{}, 64)
	src.afterLoad = func() {
		entered <- struct{}{}
		<-release
	}
	p, _ := newMPForTest(src, nil)

	const readers = 32
	var started, finished sync.WaitGroup
	started.Add(readers)
	finished.Add(readers)
	results := make([]bool, readers)
	for i := 0; i < readers; i++ {
		go func(i int) {
			defer finished.Done()
			started.Done()
			results[i] = p.ModelAccess(ctx, 1, "gpt-5.4").OK
		}(i)
	}
	<-entered // 第一次加载已经开始，并停在返回之前
	started.Wait()
	// 所有读取者都已经启动；再给它们一点时间排到 singleflight 后面，然后放行。
	time.Sleep(100 * time.Millisecond)
	close(release)
	finished.Wait()

	for i, ok := range results {
		require.True(t, ok, "reader %d", i)
	}
	require.EqualValues(t, 1, src.snapCalls.Load(), "singleflight collapses concurrent cold reads into one load")
}

func TestMatrixPolicy_InvalidateGroupsWithoutPubSubOnlyTouchesThoseGroups(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{
		1: mpAllowlistSnapshot("gpt-5.4"),
		2: mpAllowlistSnapshot("gpt-5.4"),
	})
	p, _ := newMPForTest(src, nil)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.True(t, p.ModelAccess(ctx, 2, "gpt-5.4").OK)
	require.EqualValues(t, 2, src.snapCalls.Load())

	src.setSnapshot(1, mpAllowlistSnapshot("gpt-5.5"))
	src.setSnapshot(2, mpAllowlistSnapshot("gpt-5.5"))
	p.InvalidateGroups(1)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK, "the invalidated group reloads at once")
	require.EqualValues(t, 3, src.snapCalls.Load())
	require.True(t, p.ModelAccess(ctx, 2, "gpt-5.4").OK, "the other group keeps its cached snapshot")
	require.EqualValues(t, 3, src.snapCalls.Load())

	p.InvalidateGroups() // 没有传入分组：什么也不做
	require.True(t, p.ModelAccess(ctx, 2, "gpt-5.4").OK)
	require.EqualValues(t, 3, src.snapCalls.Load())

	p.InvalidateAll()
	require.True(t, p.ModelAccess(ctx, 2, "gpt-5.5").OK)
	require.EqualValues(t, 4, src.snapCalls.Load())
}

func TestMatrixPolicy_InvalidatePublishesAndLoopsBack(t *testing.T) {
	ctx := context.Background()
	ps := &mpFakePubSub{}
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{
		1: mpAllowlistSnapshot("gpt-5.4"),
		2: mpAllowlistSnapshot("gpt-5.4"),
	})
	p, _ := newMPForTest(src, ps)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.True(t, p.ModelAccess(ctx, 2, "gpt-5.4").OK)
	require.EqualValues(t, 2, src.snapCalls.Load())

	// 通知没有载荷：订阅者（包括发布者自己）丢弃全部分组的快照，所以另一个分组也会重新加载。
	src.setSnapshot(1, mpAllowlistSnapshot("gpt-5.5"))
	src.setSnapshot(2, mpAllowlistSnapshot("gpt-5.5"))
	p.InvalidateGroups(1)
	require.Equal(t, 1, ps.notifyCount())
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK)
	require.True(t, p.ModelAccess(ctx, 2, "gpt-5.5").OK)
	require.EqualValues(t, 4, src.snapCalls.Load())

	// 没有传入分组时什么也不做，也不发通知。
	p.InvalidateGroups()
	require.Equal(t, 1, ps.notifyCount())
	require.EqualValues(t, 4, src.snapCalls.Load())

	p.InvalidateAll()
	require.Equal(t, 2, ps.notifyCount())
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK)
	require.True(t, p.ModelAccess(ctx, 2, "gpt-5.5").OK)
	require.EqualValues(t, 6, src.snapCalls.Load())
}

func TestMatrixPolicy_NotificationFromOtherInstanceClearsLocalCacheWithoutRepublishing(t *testing.T) {
	ctx := context.Background()
	ps := &mpFakePubSub{}
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
	p, _ := newMPForTest(src, ps)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)

	src.setSnapshot(1, mpAllowlistSnapshot("gpt-5.5"))
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK, "no notification yet: still the cached snapshot")

	ps.fireFromOtherInstance()
	require.Zero(t, ps.notifyCount(), "a received notification must not be published again")
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK)
	require.False(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
}

func TestMatrixPolicy_InvalidationDuringLoadDoesNotCacheStaleData(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
	p, _ := newMPForTest(src, nil)

	loaded := make(chan struct{})
	finish := make(chan struct{})
	var first atomic.Bool
	src.afterLoad = func() {
		if first.CompareAndSwap(false, true) {
			close(loaded)
			<-finish
		}
	}

	done := make(chan bool, 1)
	go func() { done <- p.ModelAccess(ctx, 1, "gpt-5.4").OK }()
	<-loaded
	// 第一次加载已经读到了旧数据（只放行 gpt-5.4），但还没有返回。这时有人写入新数据并失效了缓存。
	src.setSnapshot(1, mpAllowlistSnapshot("gpt-5.5"))
	p.InvalidateGroups(1)
	close(finish)

	require.True(t, <-done, "the in-flight request itself may still see the data it loaded")
	callsAfterFirst := src.snapCalls.Load()
	// 但这份数据早于那次写入，不能被存进缓存：下一次读取必须重新加载，并看到新数据。
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK)
	require.False(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.Equal(t, callsAfterFirst+1, src.snapCalls.Load(), "exactly one reload, whose result is cached")
}

func TestMatrixPolicy_DeletedOrMissingGroupGetsDefaults(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
	src.metas[1] = DeriveGroup{ID: 1, Platform: PlatformOpenAI, Deleted: true}
	p, _ := newMPForTest(src, nil)

	require.True(t, p.ModelAccess(ctx, 1, "unknown-model").OK, "a soft-deleted group's rows are dropped on read")
	require.Zero(t, src.snapCalls.Load(), "no need to read the snapshot of a deleted group")
	require.True(t, p.ModelAccess(ctx, 404, "unknown-model").OK, "a group that does not exist")
	require.False(t, p.SnapshotDegraded(ctx, 1))
	require.Equal(t, PricingStageLegacy, p.Stage(ctx, 1))
}

func TestMatrixPolicy_LoadIgnoresCallerCancellation(t *testing.T) {
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
	p, _ := newMPForTest(src, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 假数据源在 ctx 已取消时会报错；加载用的是脱离请求取消信号的 ctx，所以不受影响，也不会落到兜底。
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.False(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK, "the real snapshot was loaded, not the fail-open fallback")
	require.Zero(t, p.Stats().LoadFailures)
}

// ---------------------------------------------------------------------------
// 快照加载失败的语义
// ---------------------------------------------------------------------------

func TestMatrixPolicy_ColdLoadFailureFallsBackToDefaultsAndRetriesAfterBackoff(t *testing.T) {
	ctx := context.Background()
	loadErr := errors.New("db down")
	snap := mpAllowlistSnapshot("gpt-5.4")
	snap.Config.Features = map[string]any{featureKeyBedrockCCCompat: true}
	snap.Config.BillingModelSource = mpS(BillingModelSourceUpstream)
	snap.Config.ModelMapping = []MatrixMappingEntry{{Src: "gpt-5.5", Dst: "gpt-5.4"}}
	snap.Cells = append(snap.Cells, mpExtra("gpt-5.4-mini", 2))
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: snap})
	src.setErrors(loadErr, nil)
	p, clock := newMPForTest(src, nil)

	// 兜底：与 legacy 缓存加载失败时一致，开放、没有映射、没有价格覆盖、没有额外倍率。
	require.True(t, p.ModelAccess(ctx, 1, "unknown-model").OK)
	require.True(t, p.UpstreamAccess(ctx, 1, "unknown-model").OK)
	require.Equal(t, ChannelMappingResult{MappedModel: "gpt-5.5"}, p.Mapping(ctx, 1, "gpt-5.5"))
	require.Nil(t, p.PriceOverride(ctx, 1, "gpt-5.4", time.Time{}))
	require.Equal(t, 1.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4-mini", time.Time{}))
	require.Equal(t, MatrixCostAccountRate, p.CostMode(ctx, 1))
	require.Equal(t, PricingStageLegacy, p.Stage(ctx, 1))
	// 能返回错误的两个方法把加载错误交给调用方。
	check, err := p.UpstreamCheck(ctx, 1)
	require.ErrorIs(t, err, loadErr)
	require.False(t, check)
	feature, err := p.Feature(ctx, 1, PlatformAnthropic, GroupFeatureBedrockCCCompat)
	require.ErrorIs(t, err, loadErr)
	require.Nil(t, feature)
	require.True(t, p.SnapshotDegraded(ctx, 1))

	// 退避期内不再碰数据库。
	require.EqualValues(t, 1, src.metaCalls.Load())
	clock.Advance(matrixSnapshotErrorTTL - time.Second)
	require.True(t, p.ModelAccess(ctx, 1, "unknown-model").OK)
	require.EqualValues(t, 1, src.metaCalls.Load())
	require.Equal(t, MatrixSnapshotStats{LoadFailures: 1, ColdFallbacks: 1}, p.Stats())

	// 退避期过后重试；仍然失败就再退避一轮。
	clock.Advance(2 * time.Second)
	require.True(t, p.ModelAccess(ctx, 1, "unknown-model").OK)
	require.EqualValues(t, 2, src.metaCalls.Load())
	require.Equal(t, MatrixSnapshotStats{LoadFailures: 2, ColdFallbacks: 2}, p.Stats())

	// 数据库恢复：退避期过后读到真实数据，不再是兜底。
	src.setErrors(nil, nil)
	clock.Advance(matrixSnapshotErrorTTL + time.Second)
	require.False(t, p.ModelAccess(ctx, 1, "unknown-model").OK)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.Equal(t, 2.0, p.ExtraMultiplier(ctx, 1, "gpt-5.4-mini", time.Time{}))
	require.Equal(t, PricingStageV2, p.Stage(ctx, 1))
	require.False(t, p.SnapshotDegraded(ctx, 1))
	check, err = p.UpstreamCheck(ctx, 1)
	require.NoError(t, err)
	require.True(t, check)
}

func TestMatrixPolicy_LoadFailureWithWarmSnapshotKeepsServingTheOldOne(t *testing.T) {
	ctx := context.Background()
	for name, failure := range map[string]struct{ meta, snap error }{
		"meta query fails":     {meta: errors.New("meta down")},
		"snapshot query fails": {snap: errors.New("snapshot down")},
	} {
		t.Run(name, func(t *testing.T) {
			src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
			p, clock := newMPForTest(src, nil)
			require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
			require.False(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK)

			// TTL 到期时数据库出错：继续用旧快照，而不是退回「全放行」。
			src.setSnapshot(1, mpAllowlistSnapshot("gpt-5.5"))
			src.setErrors(failure.meta, failure.snap)
			clock.Advance(matrixSnapshotTTL + time.Second)
			require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
			require.False(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK, "the stale snapshot still restricts")
			require.False(t, p.SnapshotDegraded(ctx, 1), "serving an old snapshot is not the default-state fallback")
			upstreamCheck, err := p.UpstreamCheck(ctx, 1)
			require.NoError(t, err, "no error is handed to callers while an old snapshot is in use")
			require.False(t, upstreamCheck)
			require.Equal(t, MatrixSnapshotStats{Loads: 1, LoadFailures: 1, StaleServed: 1}, p.Stats())

			// 退避期内不再重试，期满后重试；仍然失败继续用旧快照。
			metaCalls := src.metaCalls.Load()
			clock.Advance(matrixSnapshotErrorTTL - time.Second)
			require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
			require.Equal(t, metaCalls, src.metaCalls.Load())
			clock.Advance(2 * time.Second)
			require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
			require.Greater(t, src.metaCalls.Load(), metaCalls)
			require.EqualValues(t, 2, p.Stats().StaleServed)

			// 恢复之后读到新数据。
			src.setErrors(nil, nil)
			clock.Advance(matrixSnapshotErrorTTL + time.Second)
			require.True(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK)
			require.False(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
			require.Zero(t, p.Stats().ColdFallbacks)
		})
	}
}

func TestMatrixPolicy_InvalidatedSnapshotIsStillUsedWhenReloadFails(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
	p, _ := newMPForTest(src, nil)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)

	src.setErrors(errors.New("db down"), nil)
	p.InvalidateGroups(1)
	require.True(t, p.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.False(t, p.ModelAccess(ctx, 1, "gpt-5.5").OK, "an invalidated snapshot is kept as the fallback, not dropped")
	require.EqualValues(t, 1, p.Stats().StaleServed)
	require.Zero(t, p.Stats().ColdFallbacks)
}

// ---------------------------------------------------------------------------
// 派生服务写入之后失效分组快照
// ---------------------------------------------------------------------------

type mpRecordingInvalidator struct{ calls [][]int64 }

func (r *mpRecordingInvalidator) InvalidateGroups(ids ...int64) {
	r.calls = append(r.calls, append([]int64{}, ids...))
}

func TestPricingDerivationService_InvalidatesSnapshotsOnlyForGroupsThatChanged(t *testing.T) {
	ctx := context.Background()
	e := newMxEnv(mxOpenAIGroups(10, 11, 12)...)
	rec := &mpRecordingInvalidator{}
	e.svc.SetSnapshotInvalidator(rec)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11, 12))
	// 分组 12 已经是 v2：派生钩子整体跳过它，它的快照没有变。
	e.matrix.state[12] = GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 12, PricingStage: PricingStageV2, Revision: 3, MatrixGroupConfig: defaultMatrixGroupConfig()}}

	_, err := e.svc.RefreshChannel(ctx, 1, nil)
	require.NoError(t, err)
	require.Equal(t, [][]int64{{10, 11}}, rec.calls)

	// 第二次什么都不写：不失效。
	_, err = e.svc.RefreshChannel(ctx, 1, nil)
	require.NoError(t, err)
	require.Len(t, rec.calls, 1)

	// 落库失败：不失效，错误照常返回。
	e.setChannel(mxChannelWithRule(1, 2e-6, 10, 11, 12))
	e.matrix.applyErr = errors.New("db down")
	_, err = e.svc.RefreshChannel(ctx, 1, nil)
	require.Error(t, err)
	require.Len(t, rec.calls, 1)

	// 恢复之后写入成功，再失效。
	e.matrix.applyErr = nil
	_, err = e.svc.RefreshChannel(ctx, 1, nil)
	require.NoError(t, err)
	require.Equal(t, [][]int64{{10, 11}, {10, 11}}, rec.calls)
}

func TestPricingDerivationService_WithoutInvalidatorBehavesAsBefore(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	report, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
	require.True(t, mxGroupResult(t, report, 10).ConfigWritten)
	e.svc.SetSnapshotInvalidator(nil)
	_, err = e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
}

// 端到端：派生服务写入 -> 失效 -> matrixPolicy 重新加载。用同一份假仓库既做派生的落库、又做快照的数据源。
func TestMatrixPolicy_SeesDerivedWritesAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	e := newMxEnv(mxOpenAIGroups(10)...)
	// matrixPolicy 的数据源就是派生服务落库的那份假仓库（不看阶段：按阶段选哪个策略是后续 PR 的事）。
	p, _ := newMPForTest(e.matrix, nil)
	e.svc.SetSnapshotInvalidator(p)

	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	require.Nil(t, p.PriceOverride(ctx, 10, "gpt-5.5", mpT0), "no channel yet: the (empty) snapshot is cached")

	_, err := e.svc.RefreshChannel(ctx, 1, nil)
	require.NoError(t, err)
	got := p.PriceOverride(ctx, 10, "gpt-5.5", mpT0)
	require.NotNil(t, got, "the invalidation after the derive write makes the policy reload at once, not after the TTL")
	require.InDelta(t, 1e-6, mpInput(t, got), 1e-15)

	e.setChannel(mxChannelWithRule(1, 3e-6, 10))
	_, err = e.svc.RefreshChannel(ctx, 1, []int64{10})
	require.NoError(t, err)
	require.InDelta(t, 3e-6, mpInput(t, p.PriceOverride(ctx, 10, "gpt-5.5", mpT0)), 1e-15)
}
