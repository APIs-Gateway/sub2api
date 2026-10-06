//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// 批量派生命令的测试：和钩子的测试一样用内存里的假仓库，不碰任何共享表。

// mxBatchEnv 在 mxEnv 上接好渠道清单（ListAll）。
func mxBatchEnv(groups ...DeriveGroup) *mxEnv {
	e := newMxEnv(groups...)
	e.repo.listAllFn = func(context.Context) ([]Channel, error) {
		out := make([]Channel, 0, len(e.channels))
		for _, ch := range e.channels {
			out = append(out, *ch.Clone())
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	return e
}

func mxBatchGroup(t *testing.T, r *PricingDeriveBatchReport, gid int64) PricingDeriveGroupSummary {
	t.Helper()
	for _, g := range r.Groups {
		if g.GroupID == gid {
			return g
		}
	}
	t.Fatalf("摘要里没有分组 %d", gid)
	return PricingDeriveGroupSummary{}
}

func TestPricingDeriveBatch_DryRunWritesNothing(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{})
	require.NoError(t, err)
	require.Equal(t, PricingDeriveModeDryRun, r.Mode)
	require.Empty(t, e.matrix.applyCalls, "dry-run 不开事务")
	require.Empty(t, e.matrix.state, "dry-run 不写库")

	require.Equal(t, 1, r.Totals.Channels)
	require.Equal(t, 2, r.Totals.Groups)
	require.Equal(t, 2, r.Totals.GroupsChanged)
	for _, gid := range []int64{10, 11} {
		g := mxBatchGroup(t, r, gid)
		require.Equal(t, PricingDeriveStatusChanged, g.Status)
		require.Equal(t, PricingDeriveConfigNew, g.Config)
		require.Equal(t, string(PricingStageLegacy), g.Stage)
		require.Equal(t, 1, g.CellsNew)
		require.Equal(t, 1, g.RulesNew)
		require.Equal(t, g.CellsNew, g.Inherit+g.Extra+g.Custom+g.Closed, "价格模式与关闭的数量合起来是派生出的单元格数")
		require.False(t, g.Applied)
	}
	require.Equal(t, 2, r.Totals.ConfigNew)
	require.Equal(t, 2, r.Totals.CellsNew)
}

func TestPricingDeriveBatch_ApplyIsIdempotent(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 11, 20)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))
	e.setChannel(mxChannelWithRule(2, 2e-6, 20))
	opts := PricingDeriveBatchOptions{Apply: true}

	first, err := e.svc.DeriveBatch(context.Background(), opts)
	require.NoError(t, err)
	require.Empty(t, first.Failures)
	require.Equal(t, 3, first.Totals.GroupsChanged)
	require.Equal(t, 3, first.Totals.ConfigNew)
	require.Equal(t, [][]int64{{10, 11}, {20}}, e.matrix.applyCalls, "每个渠道一个事务")
	for _, g := range first.Groups {
		require.True(t, g.Applied)
		require.NotContains(t, g.Notes, DerivationNote{Code: "plan_drift"})
	}
	for _, gid := range []int64{10, 11, 20} {
		st := e.matrix.state[gid]
		require.NotNil(t, st.Config)
		require.Len(t, st.Cells, 1)
		require.Len(t, st.Rules, 1)
	}
	snapshot := map[int64]GroupStateSnapshot{}
	for id, st := range e.matrix.state {
		snapshot[id] = st
	}

	second, err := e.svc.DeriveBatch(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, 3, second.Totals.Groups)
	require.Equal(t, 3, second.Totals.GroupsUnchanged, "第二次全部不变")
	require.Zero(t, second.Totals.GroupsChanged+second.Totals.ConfigNew+second.Totals.ConfigUpdated)
	require.Zero(t, second.Totals.CellsNew+second.Totals.CellsUpdated+second.Totals.CellsDeleted)
	require.Zero(t, second.Totals.RulesNew+second.Totals.RulesDeleted)
	require.Equal(t, 3, second.Totals.ConfigUnchanged)
	require.Equal(t, 3, second.Totals.CellsUnchanged)
	require.Equal(t, 3, second.Totals.RulesUnchanged)
	require.Equal(t, snapshot, e.matrix.state, "第二次不改任何一行（revision 也不动）")
}

func TestPricingDeriveBatch_ApplyKeepsStages(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 11, 12, 13)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11, 12, 13))

	e.matrix.state[10] = GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 10, PricingStage: PricingStageShadow, MatrixGroupConfig: defaultMatrixGroupConfig()}}
	sentinel := GroupStateSnapshot{
		Config: &StoredGroupConfig{GroupID: 11, PricingStage: PricingStageV2, Revision: 9,
			MatrixGroupConfig: MatrixGroupConfig{AccessMode: MatrixAccessAllowlist, CostMode: MatrixCostFollowBilling, ModelMapping: []MatrixMappingEntry{}, Features: map[string]any{}}},
		Cells: []StoredMatrixCell{mxStoredCell(1, MatrixCell{ModelKey: "hand-edited", Open: false, PriceMode: MatrixPriceExtra, ExtraMultiplier: mxF(2), Source: MatrixSourceManual})},
	}
	e.matrix.state[11] = sentinel
	// 12：已有来源为 legacy_frozen 的同键单元格，派生不覆盖，只给警告。
	view, err := e.svc.ViewGroup(context.Background(), 12)
	require.NoError(t, err)
	require.NotEmpty(t, view.Derived.Cells)
	frozen := mxStoredCell(2, MatrixCell{ModelKey: view.Derived.Cells[0].ModelKey, Open: false, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyFrozen})
	e.matrix.state[12] = GroupStateSnapshot{
		Config: &StoredGroupConfig{GroupID: 12, PricingStage: PricingStageLegacy, MatrixGroupConfig: defaultMatrixGroupConfig()},
		Cells:  []StoredMatrixCell{frozen},
	}

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{Apply: true})
	require.NoError(t, err)

	require.Equal(t, string(PricingStageShadow), mxBatchGroup(t, r, 10).Stage)
	require.Equal(t, PricingStageShadow, e.matrix.state[10].Config.PricingStage, "已存在的行保持原阶段")
	require.Equal(t, PricingStageLegacy, e.matrix.state[13].Config.PricingStage, "新建的行一律 legacy")
	require.Equal(t, PricingStageLegacy, e.matrix.state[12].Config.PricingStage)

	require.Equal(t, sentinel, e.matrix.state[11], "v2 分组一个字节都不动")
	v2 := mxBatchGroup(t, r, 11)
	require.Equal(t, PricingDeriveStatusSkipped, v2.Status)
	require.Equal(t, planSkipStageV2, v2.SkipReason)
	require.Equal(t, 1, r.Totals.GroupsSkipped)

	blocked := mxBatchGroup(t, r, 12)
	require.Equal(t, 1, blocked.CellsBlocked)
	require.GreaterOrEqual(t, blocked.Warnings, 1, "被挡住的单元格有警告")
	require.Zero(t, blocked.CellsNew+blocked.CellsUpdated)
	require.Equal(t, []StoredMatrixCell{frozen}, e.matrix.state[12].Cells, "非派生来源的单元格不被覆盖")
	require.GreaterOrEqual(t, r.Totals.Warnings, 1)

	// 再跑一次：除了 v2 跳过，其余不变。
	again, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{Apply: true})
	require.NoError(t, err)
	require.Zero(t, again.Totals.GroupsChanged)
	require.Equal(t, PricingStageShadow, e.matrix.state[10].Config.PricingStage)
}

func TestPricingDeriveBatch_ChannelLimit(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 20)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	e.setChannel(mxChannelWithRule(2, 2e-6, 20))

	dry, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{ChannelID: 2})
	require.NoError(t, err)
	require.Len(t, dry.Groups, 1)
	require.Equal(t, int64(20), dry.Groups[0].GroupID)
	require.Empty(t, e.matrix.state)

	_, err = e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{ChannelID: 2, Apply: true})
	require.NoError(t, err)
	require.NotNil(t, e.matrix.state[20].Config)
	require.NotContains(t, e.matrix.state, int64(10), "没点名的渠道不动")
	require.Equal(t, [][]int64{{20}}, e.matrix.applyCalls)

	_, err = e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{ChannelID: 99})
	require.ErrorIs(t, err, ErrChannelNotFound)
}

func TestPricingDeriveBatch_GroupLimit(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))

	dry, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{GroupID: 11})
	require.NoError(t, err)
	require.Len(t, dry.Groups, 1)
	require.Equal(t, int64(1), dry.Groups[0].ChannelID, "按分组当前所属的渠道派生")
	require.Empty(t, e.matrix.state)

	_, err = e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{GroupID: 11, Apply: true})
	require.NoError(t, err)
	require.NotNil(t, e.matrix.state[11].Config)
	require.NotContains(t, e.matrix.state, int64(10), "同渠道的其他分组不动")
	require.Equal(t, [][]int64{{11}}, e.matrix.applyCalls)

	_, err = e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{GroupID: 999})
	require.ErrorIs(t, err, ErrGroupNotFound)
	_, err = e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{GroupID: 11, ChannelID: 1})
	require.Error(t, err, "渠道与分组互斥")
}

func TestPricingDeriveBatch_SkipsInactiveChannels(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 20)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	off := mxChannelWithRule(2, 2e-6, 20)
	off.Status = StatusDisabled
	e.setChannel(off)

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{Apply: true})
	require.NoError(t, err)
	require.Equal(t, 1, r.Totals.Channels)
	require.Equal(t, 1, r.Totals.ChannelsInactive)
	require.Len(t, r.Groups, 1)
	require.NotContains(t, e.matrix.state, int64(20))
}

func TestPricingDeriveBatch_OrphanGroupWithDerivedRulesIsCleared(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))
	_, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{Apply: true})
	require.NoError(t, err)

	// 分组 11 离开渠道：库里还留着它的派生行。
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	dry, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{})
	require.NoError(t, err)
	orphan := mxBatchGroup(t, dry, 11)
	require.True(t, orphan.Orphan)
	require.Equal(t, PricingDeriveStatusChanged, orphan.Status)
	require.Equal(t, 1, orphan.RulesDeleted)
	require.Len(t, e.matrix.state[11].Rules, 1, "dry-run 不清")

	_, err = e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{Apply: true})
	require.NoError(t, err)
	require.Empty(t, e.matrix.state[11].Rules)
}

func TestPricingDeriveBatch_FailureIsReportedAndOthersContinue(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 20)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	e.setChannel(mxChannelWithRule(2, 2e-6, 20))
	e.matrix.applyErr = errors.New("boom")

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{Apply: true})
	require.NoError(t, err)
	require.Len(t, r.Failures, 2, "每个渠道各自失败，不互相中断")
	require.Equal(t, int64(1), r.Failures[0].ChannelID)
	require.Equal(t, 2, r.Totals.Failures)
	require.Empty(t, r.Groups)
	require.Empty(t, e.matrix.state)
}
