//go:build unit

package service

// W6 PR5：一次计算固定读同一份分组快照（PR4-1 审查「PR5 前必须修」第 2 条），以及 cachedSnapshot 与 stale 标记。
// 测试名以 TestMatrixPolicy_ 开头，CI 的 -race job 会跑它们。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pinTestSource(snap GroupStateSnapshot) *mpFakeSource {
	return newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: snap})
}

// 审查给出的场景：单元格从 extra x2 改成 custom X。额外倍率与价格覆盖分开读快照时，
// 先读到旧的 x2、再读到新的 X，会按 X x 2 收费。固定快照之后，两次读取来自同一份快照。
func TestMatrixPolicy_PinnedComputationReadsOneSnapshotAcrossAReplacement(t *testing.T) {
	src := pinTestSource(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 2)}})
	p, _ := newMPForTest(src, nil)
	zero := time.Time{}

	pinned := pinGroupPolicySnapshots(context.Background(), p)
	require.NotNil(t, pinFromContext(pinned))
	require.Equal(t, 2.0, p.ExtraMultiplier(pinned, 1, "gpt-5.4", zero), "first read: the old snapshot")

	// 快照在两次读取之间被替换。
	src.setSnapshot(1, GroupStateSnapshot{Cells: []StoredMatrixCell{mpCustom("gpt-5.4", 1e-6)}})
	p.InvalidateGroups(1)

	require.Nil(t, p.PriceOverride(pinned, 1, "gpt-5.4", zero), "the same computation still sees the old snapshot: no custom price")
	require.Equal(t, 2.0, p.ExtraMultiplier(pinned, 1, "gpt-5.4", zero))

	// 没有固定器的读取（别的请求）看到新快照。
	require.NotNil(t, p.PriceOverride(context.Background(), 1, "gpt-5.4", zero))
	require.Equal(t, 1.0, p.ExtraMultiplier(context.Background(), 1, "gpt-5.4", zero))

	// 一次新的计算固定的是新快照。
	fresh := pinGroupPolicySnapshots(context.Background(), p)
	require.NotNil(t, p.PriceOverride(fresh, 1, "gpt-5.4", zero))
	require.Equal(t, 1.0, p.ExtraMultiplier(fresh, 1, "gpt-5.4", zero))
}

// 「是否兜底」取自参与计算的同一份快照：第一次读到的是兜底快照，之后数据库恢复，这次计算仍然是兜底。
func TestMatrixPolicy_PinnedDegradedFlagComesFromTheSameSnapshot(t *testing.T) {
	src := pinTestSource(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 2)}})
	src.setErrors(errors.New("db down"), nil)
	p, clock := newMPForTest(src, nil)

	pinned := pinGroupPolicySnapshots(context.Background(), p)
	require.True(t, p.SnapshotDegraded(pinned, 1), "cold start with a failing database: the default-state fallback")
	require.Equal(t, 1.0, p.ExtraMultiplier(pinned, 1, "gpt-5.4", time.Time{}))

	src.setErrors(nil, nil)
	clock.Advance(10 * time.Second) // 超过 5 秒的错误缓存
	p.InvalidateGroups(1)

	require.True(t, p.SnapshotDegraded(pinned, 1), "the pinned computation keeps its snapshot, flag included")
	require.Equal(t, 1.0, p.ExtraMultiplier(pinned, 1, "gpt-5.4", time.Time{}))
	require.False(t, p.SnapshotDegraded(context.Background(), 1), "an unpinned read sees the recovered snapshot")
	require.Equal(t, 2.0, p.ExtraMultiplier(context.Background(), 1, "gpt-5.4", time.Time{}))
}

func TestMatrixPolicy_PinGroupPolicySnapshotsOnlyForSnapshotReaders(t *testing.T) {
	ctx := context.Background()
	p := newMPPolicyFor(GroupStateSnapshot{})

	require.Nil(t, pinFromContext(pinGroupPolicySnapshots(ctx, nil)), "no policy: nothing to pin")
	require.Nil(t, pinFromContext(pinGroupPolicySnapshots(ctx, legacyPolicy{})), "legacy policy reads no snapshot: no allocation")
	require.Nil(t, pinFromContext(pinGroupPolicySnapshots(ctx, &mcExtraPolicy{})))

	pinned := pinGroupPolicySnapshots(ctx, p)
	require.NotNil(t, pinFromContext(pinned))
	require.Equal(t, pinned, pinGroupPolicySnapshots(pinned, p), "an outer pin is shared by nested entry points")

	staged := newStagedGroupPolicy(legacyPolicy{}, p, nil)
	require.NotNil(t, pinFromContext(pinGroupPolicySnapshots(ctx, staged)))
}

// cachedSnapshot 不阻塞：冷启动返回 nil 并在后台加载；固定器里记下的 nil 在这次计算里不变。
func TestMatrixPolicy_CachedSnapshotIsNonBlockingAndPinsTheAnswer(t *testing.T) {
	src := pinTestSource(GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, nil)})
	p, _ := newMPForTest(src, nil)

	pinned := pinGroupPolicySnapshots(context.Background(), p)
	require.Nil(t, p.cachedSnapshot(pinned, 1), "cold: no snapshot yet")

	require.Eventually(t, func() bool { return p.cachedSnapshot(context.Background(), 1) != nil }, 5*time.Second, 5*time.Millisecond,
		"the background load fills the cache")
	require.Nil(t, p.cachedSnapshot(pinned, 1), "inside the pinned computation the answer stays nil")

	snap := p.cachedSnapshot(context.Background(), 1)
	require.Equal(t, PricingStageShadow, snap.stage)

	// 阻塞读取把冷启动的空位换成真快照，之后的读取与它一致。
	require.Equal(t, PricingStageShadow, p.Stage(pinned, 1))
	require.NotNil(t, p.cachedSnapshot(pinned, 1))
}

// 过期或被失效的快照继续用（不等），同时后台刷新；刷新完成后读到新数据。
func TestMatrixPolicy_CachedSnapshotServesStaleEntryWhileRefreshing(t *testing.T) {
	src := pinTestSource(GroupStateSnapshot{Config: mpStoredConfig(PricingStageLegacy, nil)})
	p, _ := newMPForTest(src, nil)
	require.Equal(t, PricingStageLegacy, p.Stage(context.Background(), 1)) // 预热

	src.setSnapshot(1, GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, nil)})
	p.InvalidateGroups(1)

	// 失效之后的第一次读取立即返回旧快照。
	require.NotNil(t, p.cachedSnapshot(context.Background(), 1))
	require.Eventually(t, func() bool {
		snap := p.cachedSnapshot(context.Background(), 1)
		return snap != nil && snap.stage == PricingStageShadow
	}, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, time.Time{}, (&matrixPolicy{}).lastInvalidation(), "never invalidated: zero time")
	require.False(t, p.lastInvalidation().IsZero())
}

// 加载失败后继续使用旧快照：数据是旧的，不是缺的，快照打 stale 标记，影子比对据此跳过。
func TestMatrixPolicy_StaleFlagMarksOldDataServedAfterALoadFailure(t *testing.T) {
	src := pinTestSource(GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, nil)})
	p, clock := newMPForTest(src, nil)
	ctx := context.Background()

	require.False(t, p.snapshot(ctx, 1).stale)
	src.setErrors(nil, errors.New("db down"))
	clock.Advance(2 * time.Minute) // 超过 TTL

	snap := p.snapshot(ctx, 1)
	require.True(t, snap.stale)
	require.NoError(t, snap.loadErr, "stale data is not the default-state fallback")
	require.Equal(t, PricingStageShadow, snap.stage, "the data is the old data")
	require.False(t, p.SnapshotDegraded(ctx, 1))
	require.EqualValues(t, 1, p.Stats().StaleServed)

	// 数据库恢复、重试间隔过后，新快照不再带 stale 标记。
	src.setErrors(nil, nil)
	clock.Advance(10 * time.Second)
	require.False(t, p.snapshot(ctx, 1).stale)
}
