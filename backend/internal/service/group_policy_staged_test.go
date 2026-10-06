//go:build unit

package service

// W6 PR5：stagedPolicy 的路由、影子比对、跳过规则与限流。测试名以 TestStagedPolicy_ 开头，CI 的 -race job 会跑它们。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// spLegacy 是「legacy 一侧」的假实现：每个方法返回设定好的值，并记录被调用的次数。
type spLegacy struct {
	mapping  ChannelMappingResult
	access   QuoteAccess
	upCheck  bool
	upErr    error
	feature  *bool
	featErr  error
	override *ChannelModelPricing
	extra    float64
	costMode MatrixCostMode
	rules    []AccountStatsPricingRule

	calls atomic.Int32
}

func (l *spLegacy) Mapping(context.Context, int64, string) ChannelMappingResult {
	l.calls.Add(1)
	return l.mapping
}

func (l *spLegacy) ModelAccess(context.Context, int64, string) QuoteAccess {
	l.calls.Add(1)
	return l.access
}

func (l *spLegacy) UpstreamAccess(context.Context, int64, string) QuoteAccess {
	l.calls.Add(1)
	return l.access
}

func (l *spLegacy) UpstreamCheck(context.Context, int64) (bool, error) {
	l.calls.Add(1)
	return l.upCheck, l.upErr
}

func (l *spLegacy) Feature(context.Context, int64, string, GroupFeature) (*bool, error) {
	l.calls.Add(1)
	return l.feature, l.featErr
}

func (l *spLegacy) PriceOverride(context.Context, int64, string, time.Time) *ChannelModelPricing {
	l.calls.Add(1)
	return l.override
}

func (l *spLegacy) ExtraMultiplier(context.Context, int64, string, time.Time) float64 {
	l.calls.Add(1)
	if l.extra == 0 {
		return 1
	}
	return l.extra
}

func (l *spLegacy) CostMode(context.Context, int64) MatrixCostMode {
	l.calls.Add(1)
	return l.costMode
}

func (l *spLegacy) CostRules(context.Context, int64) ([]AccountStatsPricingRule, string) {
	l.calls.Add(1)
	return l.rules, PlatformOpenAI
}

func (l *spLegacy) Stage(context.Context, int64) PricingStage {
	l.calls.Add(1)
	return PricingStageLegacy
}

// spSink 收集差异样本。
type spSink struct {
	mu      sync.Mutex
	samples []PricingShadowSample
	full    bool
}

func (s *spSink) Enqueue(sample PricingShadowSample) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.full {
		return false
	}
	s.samples = append(s.samples, sample)
	return true
}

func (s *spSink) all() []PricingShadowSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PricingShadowSample{}, s.samples...)
}

// spFixture 拼出 stagedPolicy：矩阵一侧是分组 1 的快照，已经预热（阶段读取不阻塞，冷启动按 legacy 处理）。
type spFixture struct {
	staged *stagedPolicy
	legacy *spLegacy
	matrix *matrixPolicy
	src    *mpFakeSource
	clock  *mpClock
	sink   *spSink
}

func newSPFixture(t *testing.T, snap GroupStateSnapshot) *spFixture {
	t.Helper()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: snap})
	matrix, clock := newMPForTest(src, nil)
	legacy := &spLegacy{
		mapping:  ChannelMappingResult{MappedModel: "gpt-5.4", BillingModelSource: BillingModelSourceRequested},
		access:   QuoteAccess{OK: true},
		costMode: MatrixCostAccountRate,
	}
	sink := &spSink{}
	staged := newStagedGroupPolicy(legacy, matrix, sink)
	staged.hub.now = clock.Now
	matrix.Stage(context.Background(), 1) // 预热：把分组 1 的快照加载进缓存
	return &spFixture{staged: staged, legacy: legacy, matrix: matrix, src: src, clock: clock, sink: sink}
}

func shadowSnap(mutate func(*MatrixGroupConfig), cells ...StoredMatrixCell) GroupStateSnapshot {
	return GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, mutate), Cells: cells}
}

func (f *spFixture) diffCount(kind, class string) int64 {
	var n int64
	for _, d := range f.staged.Stats().DiffTotal {
		if d.GroupID == 1 && d.Kind == kind && d.Class == class {
			n += d.Count
		}
	}
	return n
}

func (f *spFixture) compared() int64 {
	var n int64
	for _, c := range f.staged.Stats().ComparedTotal {
		if c.GroupID == 1 {
			n += c.Count
		}
	}
	return n
}

func TestStagedPolicy_LegacyStageForwardsToLegacyWithoutComparing(t *testing.T) {
	f := newSPFixture(t, GroupStateSnapshot{Config: mpStoredConfig(PricingStageLegacy, nil)})
	ctx := context.Background()
	zero := time.Time{}

	require.Equal(t, f.legacy.mapping, f.staged.Mapping(ctx, 1, "gpt-5.4"))
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.Equal(t, QuoteAccess{OK: true}, f.staged.UpstreamAccess(ctx, 1, "gpt-5.4"))
	got, err := f.staged.UpstreamCheck(ctx, 1)
	require.NoError(t, err)
	require.False(t, got)
	feature, err := f.staged.Feature(ctx, 1, PlatformOpenAI, GroupFeatureBedrockCCCompat)
	require.NoError(t, err)
	require.Nil(t, feature)
	require.Nil(t, f.staged.PriceOverride(ctx, 1, "gpt-5.4", zero))
	require.Equal(t, 1.0, f.staged.ExtraMultiplier(ctx, 1, "gpt-5.4", zero))
	require.Equal(t, MatrixCostAccountRate, f.staged.CostMode(ctx, 1))
	rules, _ := f.staged.CostRules(ctx, 1)
	require.Nil(t, rules)
	require.Equal(t, PricingStageLegacy, f.staged.Stage(ctx, 1))

	stats := f.staged.Stats()
	require.Empty(t, stats.ComparedTotal)
	require.Empty(t, stats.DiffTotal)
	require.Empty(t, stats.SkippedTotal)
	require.Empty(t, f.sink.all())
}

// 分组还没有快照（进程刚启动）：按 legacy 处理，不阻塞，不比对。
func TestStagedPolicy_ColdSnapshotFallsBackToLegacy(t *testing.T) {
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: shadowSnap(nil)})
	// Keep the asynchronous warm-up cold until both cold-state assertions finish.
	// Scheduler speed must not decide whether the second read sees shadow.
	releaseLoad := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLoad) }) }
	src.afterLoad = func() { <-releaseLoad }
	t.Cleanup(release)
	matrix, _ := newMPForTest(src, nil)
	legacy := &spLegacy{mapping: ChannelMappingResult{MappedModel: "a"}, access: QuoteAccess{OK: true}}
	staged := newStagedGroupPolicy(legacy, matrix, nil)

	require.Equal(t, legacy.mapping, staged.Mapping(context.Background(), 1, "a"))
	require.Equal(t, PricingStageLegacy, staged.Stage(context.Background(), 1), "unknown stage is legacy")
	require.Empty(t, staged.Stats().ComparedTotal)

	release()
	require.Equal(t, PricingStageShadow, matrix.Stage(context.Background(), 1), "released load reaches the real warm state")
	require.Equal(t, PricingStageShadow, staged.Stage(context.Background(), 1), "a warm stage must not stay legacy")

	// 没有矩阵策略：永远 legacy。
	bare := newStagedGroupPolicy(legacy, nil, nil)
	require.Equal(t, legacy.mapping, bare.Mapping(context.Background(), 1, "a"))
	require.Equal(t, PricingStageLegacy, bare.Stage(context.Background(), 1))
	require.Equal(t, MatrixSnapshotStats{}, bare.MatrixSnapshotStats())
	bare.InvalidateGroups(1) // 不 panic
}

func TestStagedPolicy_ShadowReturnsLegacyAndCountsMappingDiff(t *testing.T) {
	f := newSPFixture(t, shadowSnap(func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceChannelMapped)
		c.ModelMapping = []MatrixMappingEntry{{Src: "gpt-5.4", Dst: "gpt-5.4-x"}}
	}))

	got := f.staged.Mapping(context.Background(), 1, "gpt-5.4")
	require.Equal(t, f.legacy.mapping, got, "the caller always gets the legacy result")

	require.EqualValues(t, 1, f.compared())
	require.EqualValues(t, 1, f.diffCount(ShadowKindMapping, ShadowClassTranslation))
	samples := f.sink.all()
	require.Len(t, samples, 1)
	require.Equal(t, ShadowKindMapping, samples[0].Kind)
	require.Equal(t, "gpt-5.4", samples[0].Model)
	require.JSONEq(t, `{"mapped_model":"gpt-5.4","mapped":false,"billing_model_source":"requested"}`, string(samples[0].LegacyView))
	require.JSONEq(t, `{"mapped_model":"gpt-5.4-x","mapped":true,"billing_model_source":"channel_mapped"}`, string(samples[0].V2View))
}

func TestStagedPolicy_ShadowIdenticalResultsCountAsComparedWithoutDiff(t *testing.T) {
	f := newSPFixture(t, shadowSnap(func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceRequested)
	}))
	ctx := context.Background()
	f.legacy.feature = nil

	require.Equal(t, f.legacy.mapping, f.staged.Mapping(ctx, 1, "gpt-5.4"))
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.Equal(t, QuoteAccess{OK: true}, f.staged.UpstreamAccess(ctx, 1, "gpt-5.4"))
	check, err := f.staged.UpstreamCheck(ctx, 1)
	require.NoError(t, err)
	require.False(t, check)
	feature, err := f.staged.Feature(ctx, 1, PlatformOpenAI, GroupFeatureBedrockCCCompat)
	require.NoError(t, err)
	require.Nil(t, feature)

	require.EqualValues(t, 5, f.compared())
	require.Empty(t, f.staged.Stats().DiffTotal)
	require.Empty(t, f.sink.all())
	require.Equal(t, PricingStageShadow, f.staged.Stage(ctx, 1), "Stage reports the configured stage")
}

func TestStagedPolicy_ShadowAccessDiffClasses(t *testing.T) {
	ctx := context.Background()

	// legacy 放行，v2 因为单元格 open=false 关闭：v2 新增的例外语义，预期差异。
	f := newSPFixture(t, shadowSnap(nil, mpClosed(mpInherit("gpt-5.4"))))
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.EqualValues(t, 1, f.diffCount(ShadowKindAccess, ShadowClassExpected))
	require.Zero(t, f.diffCount(ShadowKindAccess, ShadowClassTranslation))

	// legacy 拒绝而 v2 放行：翻译差异。
	g := newSPFixture(t, shadowSnap(nil))
	g.legacy.access = QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}
	require.Equal(t, g.legacy.access, g.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.EqualValues(t, 1, g.diffCount(ShadowKindAccess, ShadowClassTranslation))

	// 白名单分组里 v2 不认识的模型：legacy 放行，v2 拒绝（not_in_allowlist），也是翻译差异。
	h := newSPFixture(t, shadowSnap(func(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }))
	require.Equal(t, QuoteAccess{OK: true}, h.staged.UpstreamAccess(ctx, 1, "gpt-5.4"))
	require.EqualValues(t, 1, h.diffCount(ShadowKindAccess, ShadowClassTranslation))

	// 都拒绝：原因不同不算差异，只比 OK。
	i := newSPFixture(t, shadowSnap(func(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }))
	i.legacy.access = QuoteAccess{OK: false, Reason: "legacy_reason"}
	require.Equal(t, i.legacy.access, i.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.Empty(t, i.staged.Stats().DiffTotal)
}

func TestStagedPolicy_ShadowFeatureAndUpstreamCheckDiffs(t *testing.T) {
	ctx := context.Background()
	enabled := true
	f := newSPFixture(t, shadowSnap(func(c *MatrixGroupConfig) {
		c.Features = map[string]any{featureKeyBedrockCCCompat: false}
	}))
	f.legacy.feature = &enabled // legacy 开启，v2 关闭

	got, err := f.staged.Feature(ctx, 1, PlatformAnthropic, GroupFeatureBedrockCCCompat)
	require.NoError(t, err)
	require.Same(t, &enabled, got, "the legacy pointer is returned untouched")
	require.EqualValues(t, 1, f.diffCount(ShadowKindFeature, ShadowClassTranslation))

	// 错误形态不一致也是差异。
	g := newSPFixture(t, shadowSnap(nil))
	g.legacy.upErr = errors.New("legacy cache load failed")
	check, err := g.staged.UpstreamCheck(ctx, 1)
	require.Error(t, err)
	require.False(t, check)
	require.EqualValues(t, 1, g.diffCount(ShadowKindFeature, ShadowClassTranslation))

	// legacy 要求逐账号检查，v2 不要求。
	h := newSPFixture(t, shadowSnap(nil))
	h.legacy.upCheck = true
	check, err = h.staged.UpstreamCheck(ctx, 1)
	require.NoError(t, err)
	require.True(t, check)
	require.EqualValues(t, 1, h.diffCount(ShadowKindFeature, ShadowClassTranslation))
}

// 影子比对：web_search_emulation、bedrock_cc_compat 的「未配置」与 false 等价（读取方都把 nil 当 false），
// codex 图片桥的「未配置」跟随全局开关，与 false 不同。
func TestStagedPolicy_ShadowFeatureNilEqualsFalseExceptCodexBridge(t *testing.T) {
	ctx := context.Background()
	off := false
	f := newSPFixture(t, shadowSnap(nil))
	f.legacy.feature = &off
	for _, feat := range []GroupFeature{GroupFeatureWebSearchEmulation, GroupFeatureBedrockCCCompat} {
		_, err := f.staged.Feature(ctx, 1, PlatformOpenAI, feat)
		require.NoError(t, err)
	}
	require.Zero(t, f.diffCount(ShadowKindFeature, ShadowClassTranslation))

	_, err := f.staged.Feature(ctx, 1, PlatformOpenAI, GroupFeatureCodexImageGenerationBridge)
	require.NoError(t, err)
	require.EqualValues(t, 1, f.diffCount(ShadowKindFeature, ShadowClassTranslation))
}

// v2Live 为 false 时，库里写着 v2 的分组也走 legacy，并且不比对（PR7a 起生产构造默认放开 v2Live，这里手动关掉验证 legacy 路径）。
func TestStagedPolicy_V2StageDoesNotRouteToMatrixUntilLive(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, GroupStateSnapshot{
		Config: mpStoredConfig(PricingStageV2, nil),
		Cells:  []StoredMatrixCell{mpClosed(mpInherit("gpt-5.4"))},
	})
	f.staged.v2Live = false
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"), "legacy answer, the closed cell is not read")
	require.Empty(t, f.staged.Stats().ComparedTotal)
	require.Equal(t, PricingStageV2, f.staged.Stage(ctx, 1), "the configured stage is still reported")

	f.staged.v2Live = true
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"),
		"once live, a v2 group reads the matrix")
	require.Empty(t, f.staged.Stats().DiffTotal, "live routing is not a comparison")
}

// forceStage 只有 ctx 同时带影子标记时才生效（S-2）。
func TestStagedPolicy_ForcedStageNeedsTheShadowMarker(t *testing.T) {
	f := newSPFixture(t, GroupStateSnapshot{
		Config: mpStoredConfig(PricingStageLegacy, nil),
		Cells:  []StoredMatrixCell{mpClosed(mpInherit("gpt-5.4")), mpExtra("gpt-5.2", 3)},
	})
	zero := time.Time{}

	forcedOnly := context.WithValue(context.Background(), forceStageCtxKey{}, PricingStageV2)
	_, ok := forcedStageFromCtx(forcedOnly)
	require.False(t, ok, "a bare forceStage is ignored")
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(forcedOnly, 1, "gpt-5.4"))
	require.Equal(t, 1.0, f.staged.ExtraMultiplier(forcedOnly, 1, "gpt-5.2", zero))
	require.Equal(t, PricingStageLegacy, f.staged.Stage(forcedOnly, 1))

	markerOnly := context.WithValue(context.Background(), shadowRecomputeCtxKey{}, true)
	require.True(t, isShadowRecompute(markerOnly))
	_, ok = forcedStageFromCtx(markerOnly)
	require.False(t, ok, "the marker alone forces nothing")
	require.Equal(t, 1.0, f.staged.ExtraMultiplier(markerOnly, 1, "gpt-5.2", zero))

	forced := withShadowRecompute(context.Background(), PricingStageV2)
	require.True(t, IsBillingNonSettlement(forced), "a shadow recompute is a non-settlement call: the PR1 counters skip it")
	stage, ok := forcedStageFromCtx(forced)
	require.True(t, ok)
	require.Equal(t, PricingStageV2, stage)
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}, f.staged.ModelAccess(forced, 1, "gpt-5.4"))
	require.Equal(t, 3.0, f.staged.ExtraMultiplier(forced, 1, "gpt-5.2", zero))
	require.Equal(t, PricingStageV2, f.staged.Stage(forced, 1))

	forcedLegacy := withShadowRecompute(context.Background(), PricingStageLegacy)
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(forcedLegacy, 1, "gpt-5.4"))
	require.Equal(t, PricingStageLegacy, f.staged.Stage(forcedLegacy, 1))

	require.Empty(t, f.staged.Stats().ComparedTotal, "forced calls are recomputations, never compared themselves")
}

func TestStagedPolicy_StaleSnapshotIsSkippedAndCounted(t *testing.T) {
	f := newSPFixture(t, shadowSnap(func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceChannelMapped)
		c.ModelMapping = []MatrixMappingEntry{{Src: "gpt-5.4", Dst: "other"}}
	}))
	f.src.setErrors(nil, errors.New("db down"))
	f.clock.Advance(2 * time.Minute)
	f.matrix.Stage(context.Background(), 1) // 触发一次重新加载：失败，沿用旧快照

	require.Equal(t, f.legacy.mapping, f.staged.Mapping(context.Background(), 1, "gpt-5.4"))
	require.Empty(t, f.staged.Stats().ComparedTotal)
	require.Empty(t, f.staged.Stats().DiffTotal, "no comparison against old data, so no false diff")
	require.EqualValues(t, 1, f.staged.Stats().SkippedTotal[ShadowSkipStale])
}

func TestStagedPolicy_RecentInvalidationIsSkippedForTheGraceWindow(t *testing.T) {
	f := newSPFixture(t, shadowSnap(func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceChannelMapped)
		c.ModelMapping = []MatrixMappingEntry{{Src: "gpt-5.4", Dst: "other"}}
	}))
	ctx := context.Background()

	f.matrix.InvalidateGroups(1) // 渠道刚保存、派生可能还没落库
	f.staged.Mapping(ctx, 1, "gpt-5.4")
	require.EqualValues(t, 1, f.staged.Stats().SkippedTotal[ShadowSkipRecentChange])
	require.Empty(t, f.staged.Stats().ComparedTotal)

	f.clock.Advance(shadowRecentChangeGrace + time.Second)
	f.staged.Mapping(ctx, 1, "gpt-5.4")
	require.EqualValues(t, 1, f.compared())
	require.EqualValues(t, 1, f.diffCount(ShadowKindMapping, ShadowClassTranslation))
}

func TestStagedPolicy_RateLimitCapsComparisons(t *testing.T) {
	f := newSPFixture(t, shadowSnap(nil))
	f.staged.hub.callGate.limit = 2
	for i := 0; i < 5; i++ {
		f.staged.ModelAccess(context.Background(), 1, "gpt-5.4")
	}
	require.EqualValues(t, 2, f.compared())
	require.EqualValues(t, 3, f.staged.Stats().SkippedTotal[ShadowSkipRateLimited])

	// 下一秒重新计数。
	f.clock.Advance(time.Second)
	f.staged.ModelAccess(context.Background(), 1, "gpt-5.4")
	require.EqualValues(t, 3, f.compared())
}

// v2 一侧的 panic 被吞掉并计数，调用方照常拿到 legacy 结果。
func TestStagedPolicy_PanicInTheShadowSideIsSwallowed(t *testing.T) {
	f := newSPFixture(t, shadowSnap(nil))
	snap := f.matrix.cachedSnapshot(context.Background(), 1)
	require.NotNil(t, snap)

	require.NotPanics(t, func() {
		f.staged.compareCall(context.Background(), 1, snap, func(context.Context) { panic("v2 exploded") })
	})
	require.EqualValues(t, 1, f.staged.Stats().PanicsTotal)
	require.Empty(t, f.staged.Stats().ComparedTotal, "a panicking comparison is not counted as compared")

	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(context.Background(), 1, "gpt-5.4"), "the next request is unaffected")
	require.EqualValues(t, 1, f.compared())
}

func TestStagedPolicy_PricingMethodsFollowTheRoute(t *testing.T) {
	ctx := context.Background()
	price := 1e-6
	f := newSPFixture(t, shadowSnap(nil, mpExtra("gpt-5.4", 3)))
	f.legacy.override = &ChannelModelPricing{InputPrice: &price}
	f.legacy.extra = 2
	f.legacy.costMode = MatrixCostFollowBilling
	f.legacy.rules = []AccountStatsPricingRule{{ID: 7}}

	// shadow：全部读 legacy。
	require.Same(t, f.legacy.override, f.staged.PriceOverride(ctx, 1, "gpt-5.4", time.Time{}))
	require.Equal(t, 2.0, f.staged.ExtraMultiplier(ctx, 1, "gpt-5.4", time.Time{}))
	require.Equal(t, MatrixCostFollowBilling, f.staged.CostMode(ctx, 1))
	rules, _ := f.staged.CostRules(ctx, 1)
	require.Len(t, rules, 1)

	// 影子重算（v2）：全部读矩阵，并且用的是 ctx 里固定的快照。
	forced := withShadowRecompute(ctx, PricingStageV2)
	require.Nil(t, f.staged.PriceOverride(forced, 1, "gpt-5.4", time.Time{}))
	require.Equal(t, 3.0, f.staged.ExtraMultiplier(forced, 1, "gpt-5.4", time.Time{}))
	require.Equal(t, MatrixCostAccountRate, f.staged.CostMode(forced, 1))
}

func TestStagedPolicy_InvalidateGroupsReachesTheMatrixCache(t *testing.T) {
	f := newSPFixture(t, shadowSnap(nil))
	require.True(t, f.matrix.lastInvalidation().IsZero())
	f.staged.InvalidateGroups(1)
	require.False(t, f.matrix.lastInvalidation().IsZero())
	require.Equal(t, f.matrix.Stats(), f.staged.MatrixSnapshotStats())
}

// 并发：请求、失效、刷新交错，-race 下不能有数据竞争。
func TestStagedPolicy_ConcurrentRequestsAndInvalidations(t *testing.T) {
	f := newSPFixture(t, shadowSnap(func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceChannelMapped)
		c.ModelMapping = []MatrixMappingEntry{{Src: "gpt-5.4", Dst: "other"}}
	}, mpExtra("gpt-5.4", 2)))
	f.staged.hub.callGate.limit = 1 << 30

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				ctx := pinGroupPolicySnapshots(context.Background(), f.staged)
				f.staged.Mapping(ctx, 1, "gpt-5.4")
				f.staged.ModelAccess(ctx, 1, "gpt-5.4")
				f.staged.ExtraMultiplier(ctx, 1, "gpt-5.4", time.Time{})
				if g == 0 && i%20 == 0 {
					f.staged.InvalidateGroups(1)
				}
				if g == 1 && i%50 == 0 {
					f.clock.Advance(time.Second)
				}
			}
		}(g)
	}
	wg.Wait()
	_ = f.staged.Stats()
}
