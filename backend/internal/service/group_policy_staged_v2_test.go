//go:build unit

package service

// W6 PR7a：stagedPolicy 的 v2 路由、启动预加载、运行时目录状态与白名单无价检查。
// 测试名以 TestStagedPolicy_ 开头，CI 的 -race job 会跑它们。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func v2Snap(mutate func(*MatrixGroupConfig), revision int64, cells ...StoredMatrixCell) GroupStateSnapshot {
	cfg := mpStoredConfig(PricingStageV2, mutate)
	cfg.Revision = revision
	return GroupStateSnapshot{Config: cfg, Cells: cells}
}

func allowlistConfig(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }

// v2 分组的每一个读口都读矩阵，legacy 一侧一次也不会被调用。
func TestStagedPolicy_V2RoutesEveryReadToTheMatrix(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(func(c *MatrixGroupConfig) {
		c.AccessMode = MatrixAccessAllowlist
		c.BillingModelSource = mpS(BillingModelSourceUpstream)
		c.ModelMapping = []MatrixMappingEntry{{Src: "alias", Dst: "gpt-5.4"}}
		c.Features = map[string]any{
			featureKeyWebSearchEmulation: map[string]any{PlatformOpenAI: true},
			featureKeyBedrockCCCompat:    true,
		}
		c.CostMode = MatrixCostFollowBilling
	}, 3, mpCustom("gpt-5.4", 1e-6), mpExtra("gpt-5.2", 3), mpClosed(mpInherit("gpt-5.1"))))
	// legacy 一侧给出与矩阵完全不同的答案：任何一个读口落到它上面都会被看出来。
	f.legacy.access = QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}
	f.legacy.mapping = ChannelMappingResult{MappedModel: "legacy-model", BillingModelSource: BillingModelSourceRequested}
	f.legacy.extra = 9

	require.Equal(t, PricingStageV2, f.staged.Stage(ctx, 1))
	require.Equal(t, ChannelMappingResult{MappedModel: "gpt-5.4", Mapped: true, BillingModelSource: BillingModelSourceUpstream},
		f.staged.Mapping(ctx, 1, "alias"))
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}, f.staged.ModelAccess(ctx, 1, "gpt-5.1"))
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}, f.staged.ModelAccess(ctx, 1, "not-listed"))
	require.Equal(t, QuoteAccess{OK: true}, f.staged.UpstreamAccess(ctx, 1, "gpt-5.4"))
	check, err := f.staged.UpstreamCheck(ctx, 1)
	require.NoError(t, err)
	require.True(t, check, "allowlist group + upstream billing source")

	web, err := f.staged.Feature(ctx, 1, PlatformOpenAI, GroupFeatureWebSearchEmulation)
	require.NoError(t, err)
	require.NotNil(t, web)
	require.True(t, *web)
	bedrock, err := f.staged.Feature(ctx, 1, PlatformOpenAI, GroupFeatureBedrockCCCompat)
	require.NoError(t, err)
	require.NotNil(t, bedrock)
	require.True(t, *bedrock)

	override := f.staged.PriceOverride(ctx, 1, "gpt-5.4", time.Time{})
	require.NotNil(t, override)
	require.Equal(t, 1e-6, mpInput(t, override))
	require.Equal(t, 3.0, f.staged.ExtraMultiplier(ctx, 1, "gpt-5.2", time.Time{}))
	require.Equal(t, 1.0, f.staged.ExtraMultiplier(ctx, 1, "gpt-5.4", time.Time{}))
	require.Equal(t, MatrixCostFollowBilling, f.staged.CostMode(ctx, 1))
	_, platform := f.staged.CostRules(ctx, 1)
	require.Equal(t, PlatformOpenAI, platform)

	require.Zero(t, f.legacy.calls.Load(), "a v2 group never touches the legacy policy")
	require.Empty(t, f.staged.Stats().ComparedTotal, "live routing is not a comparison")
}

// v2 快照在重新加载失败时沿用旧数据：阶段不会因为数据库抖动退回 legacy。
func TestStagedPolicy_V2KeepsServingTheStaleSnapshotWhenReloadFails(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(nil, 1, mpClosed(mpInherit("gpt-5.4"))))
	closed := QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}
	require.Equal(t, closed, f.staged.ModelAccess(ctx, 1, "gpt-5.4"))

	f.src.setErrors(nil, errors.New("database is down"))
	f.matrix.InvalidateGroups(1)
	f.matrix.snapshot(ctx, 1) // 阻塞读取一次：加载失败，沿用旧快照
	require.Equal(t, closed, f.staged.ModelAccess(ctx, 1, "gpt-5.4"), "still v2, still the old cells")
	require.Equal(t, PricingStageV2, f.staged.Stage(ctx, 1))
	require.EqualValues(t, 1, f.matrix.Stats().StaleServed)
	require.Zero(t, f.legacy.calls.Load())
}

// 生产构造的 stagedPolicy 默认放开 v2 路由。
func TestStagedPolicy_V2RoutingIsLiveByDefault(t *testing.T) {
	staged := newStagedGroupPolicy(&spLegacy{}, nil, nil)
	require.True(t, staged.v2Live)
}

// ---------------------------------------------------------------------------
// 启动预加载
// ---------------------------------------------------------------------------

type spLister struct {
	ids    []int64
	stages map[int64]PricingStage // 没写的分组按 v2 算
	errs   []error                // 前几次调用依次返回这些错误
	calls  atomic.Int32
}

func (l *spLister) ListConfiguredGroups(context.Context) ([]ConfiguredGroup, error) {
	n := int(l.calls.Add(1))
	if n <= len(l.errs) && l.errs[n-1] != nil {
		return nil, l.errs[n-1]
	}
	out := make([]ConfiguredGroup, 0, len(l.ids))
	for _, id := range l.ids {
		stage, ok := l.stages[id]
		if !ok {
			stage = PricingStageV2
		}
		out = append(out, ConfiguredGroup{ID: id, Stage: stage})
	}
	return out, nil
}

// flakyMatrixSource 前 failLoads 次读取分组快照失败，之后恢复。
type flakyMatrixSource struct {
	*mpFakeSource
	failLoads int32
	loads     atomic.Int32
}

func (f *flakyMatrixSource) LoadGroupSnapshots(ctx context.Context, ids []int64) (map[int64]GroupStateSnapshot, error) {
	if f.loads.Add(1) <= f.failLoads {
		return nil, errors.New("transient failure")
	}
	return f.mpFakeSource.LoadGroupSnapshots(ctx, ids)
}

func newColdStaged(src MatrixSnapshotSource) (*stagedPolicy, *spLegacy) {
	matrix, _ := newMPForTest(src, nil)
	legacy := &spLegacy{access: QuoteAccess{OK: true}, mapping: ChannelMappingResult{MappedModel: "legacy"}}
	staged := newStagedGroupPolicy(legacy, matrix, nil)
	staged.retryDelay = time.Millisecond
	return staged, legacy
}

// 预加载完成之后，第一个请求就已经读到 v2 快照：冷启动窗口里 v2 分组不会走 legacy。
func TestStagedPolicy_PreloadMakesTheFirstRequestRouteToV2(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: v2Snap(nil, 1, mpClosed(mpInherit("gpt-5.4")))})
	staged, legacy := newColdStaged(src)

	staged.Preload(ctx, &spLister{ids: []int64{1}})
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}, staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.Equal(t, PricingStageV2, staged.Stage(ctx, 1))
	require.Zero(t, legacy.calls.Load())
	require.EqualValues(t, 1, src.snapCalls.Load(), "the request itself did not have to load anything")
	require.Zero(t, staged.MatrixSnapshotStats().PreloadFailures)
}

func TestStagedPolicy_PreloadRetriesTransientFailures(t *testing.T) {
	ctx := context.Background()
	src := &flakyMatrixSource{
		mpFakeSource: newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: v2Snap(nil, 1, mpClosed(mpInherit("gpt-5.4")))}),
		failLoads:    1,
	}
	staged, legacy := newColdStaged(src)
	lister := &spLister{ids: []int64{1}, errs: []error{errors.New("list failed")}}

	staged.Preload(ctx, lister)
	require.EqualValues(t, 2, lister.calls.Load(), "the list is retried until it succeeds, then reused")
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}, staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.Zero(t, legacy.calls.Load())
	require.Zero(t, staged.MatrixSnapshotStats().PreloadFailures)
}

func TestStagedPolicy_PreloadGivesUpAndCountsTheGroupsThatNeverLoaded(t *testing.T) {
	ctx := context.Background()
	src := &flakyMatrixSource{
		mpFakeSource: newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: v2Snap(nil, 1)}),
		failLoads:    1000,
	}
	staged, _ := newColdStaged(src)
	staged.Preload(ctx, &spLister{ids: []int64{1}})
	require.EqualValues(t, 1, staged.MatrixSnapshotStats().PreloadFailures)

	// 列表一直失败：没有分组可计数，单独记一次失败（不知道哪些分组是 v2），也不 panic。
	staged2, _ := newColdStaged(newMPSource(PlatformOpenAI, nil))
	staged2.Preload(ctx, &spLister{errs: []error{errors.New("a"), errors.New("b"), errors.New("c")}})
	require.EqualValues(t, 1, staged2.MatrixSnapshotStats().PreloadFailures)

	// 没有 lister 或没有矩阵：什么也不做。
	staged2.Preload(ctx, nil)
	newStagedGroupPolicy(&spLegacy{}, nil, nil).Preload(ctx, &spLister{})
	var nilStaged *stagedPolicy
	nilStaged.Preload(ctx, &spLister{})
}

// W6 PR7b-1 审查 B1：快照加载失败时的拒绝范围只含启动时处于 shadow、v2 的分组。
// legacy 分组（有配置行也一样）照旧按 legacy 放行；shadow 分组按「可能已经推进到 v2」处理，和 v2 一样被拒绝。
func TestStagedPolicy_UnavailableSnapshotOnlyRejectsShadowAndV2Groups(t *testing.T) {
	ctx := context.Background()
	src := &flakyMatrixSource{
		// 分组 1、2、3 在库里存在（有元信息）：读快照才会真的失败；不存在的分组（99）按默认状态，读不到也不算失败。
		mpFakeSource: newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{
			1: {Config: mpStoredConfig(PricingStageLegacy, nil)}, 2: shadowSnap(nil), 3: v2Snap(nil, 1)}),
		failLoads: 1 << 20, // 数据库一直不可用：预加载和请求时的加载都失败
	}
	staged, legacy := newColdStaged(src)
	staged.Preload(ctx, &spLister{
		ids:    []int64{1, 2, 3},
		stages: map[int64]PricingStage{1: PricingStageLegacy, 2: PricingStageShadow, 3: PricingStageV2},
	})
	denied := QuoteAccess{OK: false, Reason: QuoteAccessReasonSnapshotUnavailable}

	require.True(t, staged.ModelAccess(ctx, 1, "gpt-5.4").OK, "legacy group with a config row: legacy answers")
	require.Positive(t, legacy.calls.Load())
	require.Zero(t, staged.MatrixSnapshotStats().SnapshotUnavailable, "legacy requests were not rejected")
	require.True(t, staged.ModelAccess(ctx, 99, "gpt-5.4").OK, "no config row: legacy")

	require.Equal(t, denied, staged.ModelAccess(ctx, 2, "gpt-5.4"), "shadow may have advanced to v2: rejected like v2")
	require.Equal(t, denied, staged.ModelAccess(ctx, 3, "gpt-5.4"))
	require.GreaterOrEqual(t, staged.MatrixSnapshotStats().SnapshotUnavailable, int64(2))
}

// 运行中从 legacy 切到 shadow/v2 的分组（启动时没有配置行或是 legacy）会进入「可能是 v2」集合：
// 阶段切换提交后失效快照的路径记下它，之后它的快照加载失败就被拒绝，而不是按 legacy 放行。
func TestStagedPolicy_GroupSwitchedAfterStartupBecomesRejectableOnLoadFailure(t *testing.T) {
	ctx := context.Background()
	src := &flakyMatrixSource{
		// 分组 1、7、8 在库里存在：读快照才会真的失败（元信息不存在的分组读快照不会走到失败的那一步）。
		mpFakeSource: newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: {}, 7: {}, 8: {}}),
		failLoads:    1 << 20,
	}
	staged, _ := newColdStaged(src)
	staged.Preload(ctx, &spLister{ids: []int64{1}, stages: map[int64]PricingStage{1: PricingStageLegacy}})
	denied := QuoteAccess{OK: false, Reason: QuoteAccessReasonSnapshotUnavailable}

	require.True(t, staged.ModelAccess(ctx, 7, "gpt-5.4").OK, "group 7 had no config row at startup: legacy")
	require.True(t, staged.ModelAccess(ctx, 1, "gpt-5.4").OK, "group 1 was legacy at startup: legacy")

	// 阶段切换提交之后的失效通知：两个分组都进了集合。
	staged.InvalidateGroups(7)
	staged.InvalidateGroups(1)
	require.Equal(t, denied, staged.ModelAccess(ctx, 7, "gpt-5.4"))
	require.Equal(t, denied, staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.True(t, staged.ModelAccess(ctx, 8, "gpt-5.4").OK, "an untouched group stays legacy")
}

// spDynLister 可以在运行中改变返回内容的 lister。
type spDynLister struct {
	mu     sync.Mutex
	groups []ConfiguredGroup
	errs   int // 前几次调用返回错误
	calls  atomic.Int32
}

func (l *spDynLister) set(groups ...ConfiguredGroup) {
	l.mu.Lock()
	l.groups = groups
	l.mu.Unlock()
}

func (l *spDynLister) ListConfiguredGroups(context.Context) ([]ConfiguredGroup, error) {
	n := int(l.calls.Add(1))
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= l.errs {
		return nil, errors.New("list failed")
	}
	return append([]ConfiguredGroup{}, l.groups...), nil
}

// 别的实例切换阶段：本实例只收到不带分组的失效通知。通知之后（去抖）重新列一次，shadow、v2 的分组进入集合，
// legacy 分组不进，已经在集合里的分组不会被移除。
func TestStagedPolicy_PeerInvalidationRelistsConfiguredGroups(t *testing.T) {
	ctx := context.Background()
	staged, _ := newColdStaged(newMPSource(PlatformOpenAI, nil))
	staged.matrix.relistDebounce = 5 * time.Millisecond
	lister := &spDynLister{}
	lister.set(ConfiguredGroup{ID: 1, Stage: PricingStageLegacy})
	staged.Preload(ctx, lister)
	require.False(t, staged.matrix.mayBeConfigured(7), "listed at startup: group 7 has no config row")

	lister.set(ConfiguredGroup{ID: 1, Stage: PricingStageLegacy}, ConfiguredGroup{ID: 7, Stage: PricingStageShadow},
		ConfiguredGroup{ID: 8, Stage: PricingStageV2})
	staged.matrix.onPeerInvalidate()
	require.Eventually(t, func() bool { return staged.matrix.mayBeConfigured(7) }, 5*time.Second, 5*time.Millisecond)
	require.True(t, staged.matrix.mayBeConfigured(8))
	require.False(t, staged.matrix.mayBeConfigured(1), "legacy groups stay out of the set")

	// 只加不减：之后的列表里没有分组 7 了，它仍在集合里。
	lister.set(ConfiguredGroup{ID: 1, Stage: PricingStageLegacy})
	before := lister.calls.Load()
	staged.matrix.onPeerInvalidate()
	require.Eventually(t, func() bool { return lister.calls.Load() > before }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	require.True(t, staged.matrix.mayBeConfigured(7))
}

// 启动时三次都没列出分组的实例，之后由周期任务补上：集合变成「只有 shadow、v2」，legacy 分组不再被拒绝。
func TestStagedPolicy_PeriodicRelistRecoversFromAFailedStartupList(t *testing.T) {
	ctx := context.Background()
	src := &flakyMatrixSource{
		mpFakeSource: newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: {}, 2: v2Snap(nil, 1)}),
		failLoads:    1 << 20,
	}
	staged, _ := newColdStaged(src)
	staged.matrix.relistInterval = 10 * time.Millisecond
	lister := &spDynLister{errs: matrixPreloadAttempts}
	lister.set(ConfiguredGroup{ID: 1, Stage: PricingStageLegacy}, ConfiguredGroup{ID: 2, Stage: PricingStageV2})
	staged.Preload(ctx, lister)

	denied := QuoteAccess{OK: false, Reason: QuoteAccessReasonSnapshotUnavailable}
	require.Eventually(t, func() bool { return staged.ModelAccess(ctx, 1, "gpt-5.4").OK }, 5*time.Second, 5*time.Millisecond,
		"after the relist succeeds the legacy group is served as legacy")
	require.Equal(t, denied, staged.ModelAccess(ctx, 2, "gpt-5.4"))
}

// 切换提交之后本实例自己发出的失效通知会回到自己的订阅上，加载到一半代数变了：补加载一次，不报失败。
type spEchoSource struct {
	*mpFakeSource
	matrix *matrixPolicy
	calls  atomic.Int32
}

func (s *spEchoSource) LoadGroupSnapshots(ctx context.Context, ids []int64) (map[int64]GroupStateSnapshot, error) {
	if s.calls.Add(1) == 1 {
		s.matrix.invalidateAll() // 模拟自己的通知回声落在加载过程中
	}
	return s.mpFakeSource.LoadGroupSnapshots(ctx, ids)
}

func TestStagedPolicy_EnsureGroupsLoadedRetriesAfterAnEchoedInvalidation(t *testing.T) {
	ctx := context.Background()
	src := &spEchoSource{mpFakeSource: newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: shadowSnap(nil)})}
	matrix, _ := newMPForTest(src, nil)
	src.matrix = matrix
	staged := newStagedGroupPolicy(&spLegacy{}, matrix, nil)

	require.NoError(t, staged.EnsureGroupsLoaded(ctx, 1))
	require.EqualValues(t, 2, src.calls.Load(), "one echoed load, one retry")
	require.True(t, matrix.cachedReady(1))
}

// ---------------------------------------------------------------------------
// 目录状态
// ---------------------------------------------------------------------------

type spCatalogSource struct {
	entries []ModelCatalogEntry
	err     error
	calls   atomic.Int32
}

func (c *spCatalogSource) List(_ context.Context, filter ModelCatalogFilter) ([]ModelCatalogEntry, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	var out []ModelCatalogEntry
	for _, e := range c.entries {
		if filter.Platform == "" || e.Platform == filter.Platform {
			out = append(out, e)
		}
	}
	return out, nil
}

func catalogEntry(platform, key string, status ModelCatalogStatus, aliases ...string) ModelCatalogEntry {
	return ModelCatalogEntry{ModelKey: key, Platform: platform, Status: status, Aliases: aliases}
}

func TestStagedPolicy_V2AccessStacksTheCatalogStatus(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(nil, 1))
	src := &spCatalogSource{entries: []ModelCatalogEntry{
		catalogEntry(PlatformOpenAI, "gpt-5.4", ModelCatalogDraft),
		catalogEntry(PlatformOpenAI, "gpt-5.2", ModelCatalogRetired),
		catalogEntry(PlatformOpenAI, "gpt-5.1", ModelCatalogActive),
		catalogEntry(PlatformOpenAI, "gpt-6", ModelCatalogDraft, "gpt-6-alias"),
		catalogEntry(PlatformAnthropic, "claude-sonnet-4", ModelCatalogDraft),
	}}
	require.NotNil(t, f.staged.SetModelCatalog(src))

	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogDraft}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogRetired}, f.staged.ModelAccess(ctx, 1, " GPT-5.2 "), "names are normalized like catalog keys")
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.1"))
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "never-registered"), "unregistered models count as active (Q2)")
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogDraft}, f.staged.ModelAccess(ctx, 1, "gpt-6-alias"), "aliases resolve to the entry")
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "claude-sonnet-4"), "another platform's entry does not apply")
	require.EqualValues(t, 1, src.calls.Load(), "one cached load per platform")

	// 目录只会再挡，不会放行：单元格已经关闭的模型，目录里是 active 也照样不放行。
	f2 := newSPFixture(t, v2Snap(nil, 1, mpClosed(mpInherit("gpt-5.1"))))
	f2.staged.SetModelCatalog(src)
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}, f2.staged.ModelAccess(ctx, 1, "gpt-5.1"))

	// 上游模型不看目录：新模型上线前账号可以先映射过去。
	f3 := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("gpt-5.4")))
	f3.staged.SetModelCatalog(src)
	require.Equal(t, QuoteAccess{OK: true}, f3.staged.UpstreamAccess(ctx, 1, "gpt-5.4"))
	require.False(t, f3.staged.ModelAccess(ctx, 1, "gpt-5.4").OK, "the same model is refused for users")
}

// legacy 与 shadow 分组的准入不读目录。
func TestStagedPolicy_LegacyAndShadowAccessIgnoreTheCatalog(t *testing.T) {
	ctx := context.Background()
	src := &spCatalogSource{entries: []ModelCatalogEntry{catalogEntry(PlatformOpenAI, "gpt-5.4", ModelCatalogDraft)}}

	shadow := newSPFixture(t, shadowSnap(nil))
	shadow.staged.SetModelCatalog(src)
	require.Equal(t, QuoteAccess{OK: true}, shadow.staged.ModelAccess(ctx, 1, "gpt-5.4"))

	legacy := newSPFixture(t, GroupStateSnapshot{Config: mpStoredConfig(PricingStageLegacy, nil)})
	legacy.staged.SetModelCatalog(src)
	require.Equal(t, QuoteAccess{OK: true}, legacy.staged.ModelAccess(ctx, 1, "gpt-5.4"))

	// v2Live 关掉时 v2 分组也走 legacy，不读目录。
	off := newSPFixture(t, v2Snap(nil, 1))
	off.staged.v2Live = false
	off.staged.SetModelCatalog(src)
	require.Equal(t, QuoteAccess{OK: true}, off.staged.ModelAccess(ctx, 1, "gpt-5.4"))

	require.Zero(t, src.calls.Load(), "the catalog is not even read")
}

func TestStagedPolicy_CatalogCacheTTLAndFailureHandling(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(nil, 1))
	src := &spCatalogSource{entries: []ModelCatalogEntry{catalogEntry(PlatformOpenAI, "gpt-5.4", ModelCatalogDraft)}}
	cat := f.staged.SetModelCatalog(src)
	cat.now = f.clock.Now
	draft := QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogDraft}

	require.Equal(t, draft, f.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	f.clock.Advance(runtimeCatalogTTL - time.Second)
	src.entries = []ModelCatalogEntry{catalogEntry(PlatformOpenAI, "gpt-5.4", ModelCatalogActive)}
	require.Equal(t, draft, f.staged.ModelAccess(ctx, 1, "gpt-5.4"), "within the TTL the cached state is used")
	require.EqualValues(t, 1, src.calls.Load())
	f.clock.Advance(2 * time.Second)
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"), "after the TTL the new status takes effect")
	require.EqualValues(t, 2, src.calls.Load())

	// 加载失败：沿用旧数据，不退回「放行」。
	src.entries = []ModelCatalogEntry{catalogEntry(PlatformOpenAI, "gpt-5.4", ModelCatalogRetired)}
	src.err = errors.New("catalog read failed")
	f.clock.Advance(runtimeCatalogTTL + time.Second)
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"), "stale (active) data is served")
	loads := src.calls.Load()
	require.Equal(t, QuoteAccess{OK: true}, f.staged.ModelAccess(ctx, 1, "gpt-5.4"))
	require.Equal(t, loads, src.calls.Load(), "a failed load is not retried on every request")

	// 没有旧数据又读不到目录：放行（目录读取失败不能把可用模型挡掉）。
	f2 := newSPFixture(t, v2Snap(nil, 1))
	f2.staged.SetModelCatalog(&spCatalogSource{err: errors.New("down")})
	require.Equal(t, QuoteAccess{OK: true}, f2.staged.ModelAccess(ctx, 1, "gpt-5.4"))

	// 报价器共用同一个读取方。
	got, err := cat.Resolve(ctx, PlatformOpenAI, "gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Nil(t, (*stagedPolicy)(nil).SetModelCatalog(src))
	require.Nil(t, f.staged.SetModelCatalog(nil))
}

// ---------------------------------------------------------------------------
// 白名单分组的无价检查
// ---------------------------------------------------------------------------

type spPriceInputs struct {
	official      map[string]bool
	policy        string
	officialCalls atomic.Int32
	policyCalls   atomic.Int32
	snapshotID    int64
}

func (p *spPriceInputs) inputs() RuntimePriceInputs {
	return RuntimePriceInputs{
		OfficialState: func(model string) OfficialPriceState {
			p.officialCalls.Add(1)
			return OfficialPriceState{Known: p.official[model], TokenNonZero: p.official[model]}
		},
		PricingSnapshotID: p.snapshotID,
		ReadPolicy: func(context.Context) string {
			p.policyCalls.Add(1)
			return p.policy
		},
	}
}

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

func TestStagedPolicy_RuntimeAccessAllowlistPricing(t *testing.T) {
	ctx := context.Background()
	empty := mpCellBase("empty-custom", MatrixPriceCustom)
	empty.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeToken}
	zero := mpCellBase("zero-custom", MatrixPriceCustom)
	zero.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: mpF(0), OutputPrice: mpF(0)}
	interval := mpCellBase("interval-custom", MatrixPriceCustom)
	interval.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeToken, Intervals: []MatrixPriceInterval{{MinTokens: 0, InputPrice: mpF(1e-6)}}}
	f := newSPFixture(t, v2Snap(allowlistConfig, 4,
		mpInherit("official-priced"), mpInherit("unpriced"), mpExtra("extra-unpriced", 2),
		mpCustom("custom-priced", 1e-6), empty, zero, interval, mpExtra("official-priced-extra", 2)))
	in := &spPriceInputs{official: map[string]bool{"official-priced": true, "official-priced-extra": true}, policy: BillingUnpricedPolicyObserve}

	// 有价：官方价、显式自定义价（含 0 价与区间价）；Priced 为 true，OK 为 true。
	for _, model := range []string{"official-priced", "official-priced-extra", "custom-priced", "zero-custom", "interval-custom"} {
		got := f.staged.RuntimeAccess(ctx, 1, []string{model}, in.inputs())
		require.Equal(t, QuoteAccess{OK: true, Priced: yes()}, got, model)
	}

	// 无价：没有官方价的 inherit、extra，以及字段全空的 custom（回落官方价，而官方价没有）。
	for _, model := range []string{"unpriced", "extra-unpriced", "empty-custom", "not-a-cell-at-all"} {
		got := f.staged.RuntimeAccess(ctx, 1, []string{model}, in.inputs())
		require.Equal(t, QuoteAccess{OK: true, Reason: QuoteAccessReasonUnpriced, Priced: no()}, got, "observe: "+model+" is let through")
	}
	observed, blocked := f.staged.UnpricedAdmissionStats()
	require.EqualValues(t, 4, observed)
	require.Zero(t, blocked)

	// block_allowlist：无价被拦，有价照常。缓存了 15 秒，所以先让开关缓存过期。
	in.policy = BillingUnpricedPolicyBlockAllowlist
	f.clock.Advance(runtimePolicyTTL + time.Second)
	got := f.staged.RuntimeAccess(ctx, 1, []string{"unpriced"}, in.inputs())
	require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonUnpriced, Priced: no()}, got)
	require.Equal(t, QuoteAccess{OK: true, Priced: yes()}, f.staged.RuntimeAccess(ctx, 1, []string{"official-priced"}, in.inputs()))
	observed, blocked = f.staged.UnpricedAdmissionStats()
	require.EqualValues(t, 5, observed)
	require.EqualValues(t, 1, blocked)

	// 未知的开关值按 observe。
	in.policy = "something-else"
	f.clock.Advance(runtimePolicyTTL + time.Second)
	require.True(t, f.staged.RuntimeAccess(ctx, 1, []string{"unpriced"}, in.inputs()).OK)
}

// 候选链里任一有价就算有价（R2-S-5）：偏向少拦，计费会走有价的那个候选。
func TestStagedPolicy_RuntimeAccessAnyCandidateOnTheChainCounts(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("requested"), mpInherit("mapped")))
	in := &spPriceInputs{official: map[string]bool{"requested": true}, policy: BillingUnpricedPolicyBlockAllowlist}

	require.True(t, f.staged.RuntimeAccess(ctx, 1, []string{"mapped", "requested"}, in.inputs()).OK, "the mapped model has no price but the requested one does")
	require.Equal(t, QuoteAccess{OK: true, Priced: yes()}, f.staged.RuntimeAccess(ctx, 1, []string{"mapped", "requested"}, in.inputs()))
	require.False(t, f.staged.RuntimeAccess(ctx, 1, []string{"mapped"}, in.inputs()).OK, "with only the unpriced candidate it is blocked")
	require.True(t, f.staged.RuntimeAccess(ctx, 1, nil, in.inputs()).OK, "no candidates: nothing to check")
}

// 开放分组、legacy、shadow、v2Live 关闭时不检查：Priced 为 nil。
func TestStagedPolicy_RuntimeAccessOnlyChecksAllowlistV2Groups(t *testing.T) {
	ctx := context.Background()
	in := &spPriceInputs{policy: BillingUnpricedPolicyBlockAllowlist}
	for name, f := range map[string]*spFixture{
		"open v2 group":       newSPFixture(t, v2Snap(nil, 1)),
		"allowlist shadow":    newSPFixture(t, shadowSnap(allowlistConfig)),
		"allowlist legacy":    newSPFixture(t, GroupStateSnapshot{Config: mpStoredConfig(PricingStageLegacy, allowlistConfig)}),
		"allowlist, v2 off":   func() *spFixture { x := newSPFixture(t, v2Snap(allowlistConfig, 1)); x.staged.v2Live = false; return x }(),
		"allowlist, no cache": newSPFixture(t, v2Snap(allowlistConfig, 1)),
	} {
		if name == "allowlist, no cache" {
			// 对照组：这一项确实会检查。
			require.False(t, f.staged.RuntimeAccess(ctx, 1, []string{"unpriced"}, in.inputs()).OK, name)
			continue
		}
		require.Equal(t, QuoteAccess{OK: true}, f.staged.RuntimeAccess(ctx, 1, []string{"unpriced"}, in.inputs()), name)
	}
	require.EqualValues(t, 1, in.officialCalls.Load(), "only the control group looked at prices")
	require.EqualValues(t, 1, in.policyCalls.Load(), "and only it read the policy switch")

	// 没有矩阵策略：永远放行。
	bare := newStagedGroupPolicy(&spLegacy{}, nil, nil)
	require.Equal(t, QuoteAccess{OK: true}, bare.RuntimeAccess(ctx, 1, []string{"x"}, in.inputs()))
}

// 缓存键是（价格快照 id、分组、分组配置 revision、候选链）；结果与开关值各有 TTL。
func TestStagedPolicy_RuntimeAccessCacheKeys(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("m")))
	in := &spPriceInputs{official: map[string]bool{"m": true}, policy: BillingUnpricedPolicyObserve, snapshotID: 10}

	f.staged.RuntimeAccess(ctx, 1, []string{"m"}, in.inputs())
	f.staged.RuntimeAccess(ctx, 1, []string{"m"}, in.inputs())
	require.EqualValues(t, 1, in.officialCalls.Load(), "same key: computed once")

	f.staged.RuntimeAccess(ctx, 1, []string{"m", "other"}, in.inputs())
	require.EqualValues(t, 2, in.officialCalls.Load(), "a different candidate chain is a different key")

	in.snapshotID = 11
	f.staged.RuntimeAccess(ctx, 1, []string{"m"}, in.inputs())
	require.EqualValues(t, 3, in.officialCalls.Load(), "a new price snapshot invalidates the answer")

	// 分组配置 revision 变了：缓存键变化，用新快照重新判断。
	f.src.setSnapshot(1, v2Snap(allowlistConfig, 2, mpInherit("m")))
	f.matrix.InvalidateGroups(1)
	f.matrix.snapshot(ctx, 1)
	f.staged.RuntimeAccess(ctx, 1, []string{"m"}, in.inputs())
	require.EqualValues(t, 4, in.officialCalls.Load(), "a new group revision is a new key")

	f.clock.Advance(runtimePricedTTL + time.Second)
	f.staged.RuntimeAccess(ctx, 1, []string{"m"}, in.inputs())
	require.EqualValues(t, 5, in.officialCalls.Load(), "answers expire after the TTL even if nothing else changed")

	require.Zero(t, in.policyCalls.Load(), "the policy switch is only read when something is unpriced")
}

func TestStagedPolicy_RuntimeAccessLogIsRateLimited(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("m")))
	in := &spPriceInputs{policy: BillingUnpricedPolicyObserve}
	for i := 0; i < 5; i++ {
		f.staged.RuntimeAccess(ctx, 1, []string{"m"}, in.inputs())
	}
	observed, _ := f.staged.UnpricedAdmissionStats()
	require.EqualValues(t, 5, observed, "every unpriced request is counted")

	var fired int
	state := &runtimePricingState{}
	now := mpT0
	for i := 0; i < 5; i++ {
		state.logLimited(now, "k", func() { fired++ })
	}
	require.Equal(t, 1, fired)
	state.logLimited(now.Add(runtimeLogInterval), "k", func() { fired++ })
	require.Equal(t, 2, fired)
	for i := 0; i < runtimeLogMaxKeys+5; i++ {
		state.logLimited(now, "key-"+time.Duration(i).String(), func() {})
	}
	require.LessOrEqual(t, len(state.logged), runtimeLogMaxKeys)
}

func TestMatrixSnapshot_HasPriceHonorsTheEffectiveWindow(t *testing.T) {
	future := mpT0.Add(24 * time.Hour)
	cell := mpWindow(mpCustom("m", 1e-6), &future, nil)
	snap := buildMatrixSnapshot(1, PlatformOpenAI, GroupStateSnapshot{Cells: []StoredMatrixCell{cell}})
	require.False(t, snap.hasPrice("m", mpT0, nil), "the window has not started: the custom price is not in effect")
	require.True(t, snap.hasPrice("m", future.Add(time.Second), nil))
	require.True(t, snap.hasPrice("m", mpT0, func(string) OfficialPriceState { return OfficialPriceState{Known: true, TokenNonZero: true} }), "falls back to the official price")
}
