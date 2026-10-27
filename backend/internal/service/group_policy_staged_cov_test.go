//go:build unit

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// W6 PR7b-1：快照不可用策略的中性读口、EnsureGroupsLoaded 的失败分支、重新列出配置分组的失败分支。

func TestSnapshotUnavailablePolicy_NeutralReads(t *testing.T) {
	ctx := context.Background()
	p := snapshotUnavailablePolicy{err: errors.New("down")}
	require.Equal(t, ChannelMappingResult{MappedModel: "m"}, p.Mapping(ctx, 1, "m"))
	require.Nil(t, p.PriceOverride(ctx, 1, "m", time.Now()))
	require.Equal(t, float64(1), p.ExtraMultiplier(ctx, 1, "m", time.Now()))
	require.Equal(t, MatrixCostAccountRate, p.CostMode(ctx, 1))
	rules, hash := p.CostRules(ctx, 1)
	require.Nil(t, rules)
	require.Empty(t, hash)
	require.Equal(t, PricingStageLegacy, p.Stage(ctx, 1))
	require.False(t, p.UpstreamAccess(ctx, 1, "m").OK)
	ok, err := p.UpstreamCheck(ctx, 1)
	require.False(t, ok)
	require.EqualError(t, err, "down")
}

// spLoadScriptSource 按脚本读快照：第一次读的时候可选地模拟「自己的失效通知回声」，之后按 mode 失败或继续回声。
type spLoadScriptSource struct {
	*mpFakeSource
	matrix *matrixPolicy
	mode   string // echo-then-fail：第一次回声、之后失败；always-echo：每次都回声
	calls  atomic.Int32
}

func (s *spLoadScriptSource) LoadGroupSnapshots(ctx context.Context, ids []int64) (map[int64]GroupStateSnapshot, error) {
	n := s.calls.Add(1)
	switch s.mode {
	case "echo-then-fail":
		if n > 1 {
			return nil, errors.New("down after the echo")
		}
		s.matrix.invalidateAll()
	case "always-echo":
		s.matrix.invalidateAll()
	}
	return s.mpFakeSource.LoadGroupSnapshots(ctx, ids)
}

func newScriptedStaged(mode string) (*stagedPolicy, *spLoadScriptSource) {
	src := &spLoadScriptSource{mpFakeSource: newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: shadowSnap(nil)}), mode: mode}
	matrix, _ := newMPForTest(src, nil)
	src.matrix = matrix
	return newStagedGroupPolicy(&spLegacy{}, matrix, nil), src
}

func TestStagedPolicy_EnsureGroupsLoadedReportsEveryFailureShape(t *testing.T) {
	ctx := context.Background()

	// 第一次加载就失败。
	src := &flakyMatrixSource{
		mpFakeSource: newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: shadowSnap(nil)}),
		failLoads:    1 << 20,
	}
	staged, _ := newColdStaged(src)
	require.Error(t, staged.EnsureGroupsLoaded(ctx, 1))

	// 回声之后补加载失败（ctx 已取消时也不阻塞等待，补加载本身不受调用方 ctx 影响）。
	staged, _ = newScriptedStaged("echo-then-fail")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorContains(t, staged.EnsureGroupsLoaded(canceled, 1), "down after the echo")

	// 每次加载都被回声打断：快照始终没有存进缓存，补加载用尽之后报错而不是假装成功。
	staged, src2 := newScriptedStaged("always-echo")
	require.ErrorContains(t, staged.EnsureGroupsLoaded(ctx, 1), "was not cached")
	require.EqualValues(t, 1+ensureReloadAttempts, src2.calls.Load())
}

type spPanicLister struct{}

func (spPanicLister) ListConfiguredGroups(context.Context) ([]ConfiguredGroup, error) {
	panic("lister exploded")
}

func TestMatrixPolicy_RelistFailureShapesNeverEscape(t *testing.T) {
	staged, _ := newColdStaged(newMPSource(PlatformOpenAI, nil))
	m := staged.matrix

	// 没有 lister：什么也不做。
	m.relistConfigured()
	require.True(t, m.mayBeConfigured(7), "the list is still unknown")

	// lister 报错：只记日志，集合不变。
	m.relistMu.Lock()
	m.lister = &spDynLister{errs: 1 << 20}
	m.relistMu.Unlock()
	m.relistConfigured()
	require.True(t, m.mayBeConfigured(7))

	// lister panic：被拦下，不影响调用方。
	m.relistMu.Lock()
	m.lister = spPanicLister{}
	m.relistMu.Unlock()
	require.NotPanics(t, m.relistConfigured)
}

func TestMatrixPolicy_KickRelistWithTheDefaultDebounceAndNoListerIsHarmless(t *testing.T) {
	staged, _ := newColdStaged(newMPSource(PlatformOpenAI, nil))
	m := staged.matrix
	m.kickRelist() // 没有 lister：直接返回，不起任何等待
	require.False(t, m.relistPending.Load())

	m.relistMu.Lock()
	m.lister = &spDynLister{}
	m.relistMu.Unlock()
	m.relistDebounce = 0 // 用默认去抖
	m.kickRelist()
	require.True(t, m.relistPending.Load(), "a relist is pending behind the debounce")
	m.kickRelist() // 等待期间的后续通知并入同一次
}

func TestMatrixPolicy_PreloadCountsTheRestAsFailedOnceTheContextIsDone(t *testing.T) {
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: shadowSnap(nil), 2: shadowSnap(nil)})
	staged, _ := newColdStaged(src)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, []int64{1, 2}, staged.matrix.preload(ctx, []int64{1, 2}))
}
