//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 批量派生的错误分支与纯函数：同样用内存假仓库，不碰任何共享表。

func TestPricingDeriveBatch_CanceledContextReturnsPartialReport(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r, err := e.svc.DeriveBatch(ctx, PricingDeriveBatchOptions{Apply: true})
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, r, "取消时仍返回已完成部分的报告")
	require.Empty(t, r.Groups)
	require.Empty(t, e.matrix.state)
}

func TestPricingDeriveBatch_ChannelTimeoutStillDerives(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{ChannelTimeout: time.Minute, Apply: true})
	require.NoError(t, err)
	require.Empty(t, r.Failures)
	require.NotNil(t, e.matrix.state[10].Config)
}

func TestPricingDeriveBatch_GroupModeFailureCarriesGroupID(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))
	e.matrix.applyErr = errors.New("boom")

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{GroupID: 11, Apply: true})
	require.NoError(t, err)
	require.Len(t, r.Failures, 1)
	require.Equal(t, int64(0), r.Failures[0].ChannelID)
	require.Equal(t, int64(11), r.Failures[0].GroupID, "按分组点名时失败记在该分组上")
}

func TestPricingDeriveBatch_SelectionErrors(t *testing.T) {
	boom := errors.New("db down")
	cases := []struct {
		name  string
		opts  PricingDeriveBatchOptions
		setup func(e *mxEnv)
		want  string
	}{
		{"点名分组时读分组元数据失败", PricingDeriveBatchOptions{GroupID: 10},
			func(e *mxEnv) { e.matrix.metaErr = boom }, "get group meta"},
		{"点名渠道时读渠道失败", PricingDeriveBatchOptions{ChannelID: 1},
			func(e *mxEnv) {
				e.repo.getByIDFn = func(context.Context, int64) (*Channel, error) { return nil, boom }
			}, "db down"},
		{"点名渠道时读派生行失败", PricingDeriveBatchOptions{ChannelID: 1},
			func(e *mxEnv) { e.matrix.ruleErr = boom }, "list derived rule groups"},
		{"列渠道清单失败", PricingDeriveBatchOptions{},
			func(e *mxEnv) {
				e.repo.listAllFn = func(context.Context) ([]Channel, error) { return nil, boom }
			}, "list channels"},
		{"全量时某个渠道读取失败", PricingDeriveBatchOptions{},
			func(e *mxEnv) {
				e.repo.getByIDFn = func(context.Context, int64) (*Channel, error) { return nil, boom }
			}, "db down"},
		{"全量时某个渠道读派生行失败", PricingDeriveBatchOptions{},
			func(e *mxEnv) { e.matrix.ruleErr = boom }, "list derived rule groups"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := mxBatchEnv(mxOpenAIGroups(10)...)
			e.setChannel(mxChannelWithRule(1, 1e-6, 10))
			c.setup(e)
			r, err := e.svc.DeriveBatch(context.Background(), c.opts)
			require.Nil(t, r)
			require.ErrorContains(t, err, c.want)
		})
	}
}

func TestPricingDeriveBatch_StaleListingErrorsAndChannelsWithoutGroups(t *testing.T) {
	boom := errors.New("db down")

	// 没有任何渠道：只剩列派生行这一步，它失败就整体失败。
	e := mxBatchEnv()
	e.matrix.ruleErr = boom
	_, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{})
	require.ErrorContains(t, err, "list derived rule channels")

	// 启用的渠道没有关联任何分组、也没有派生行：不产生处理单元。
	e = mxBatchEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannel(1))
	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{})
	require.NoError(t, err)
	require.Empty(t, r.Groups)
	require.Zero(t, r.Totals.Channels)
}

func TestPricingDeriveBatch_StaleGroupsOfOneChannelAreSorted(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(21, 20)...)
	e.setChannel(mxChannelWithRule(2, 2e-6, 21, 20))
	_, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{Apply: true})
	require.NoError(t, err)
	e.removeChannel(2)

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{})
	require.NoError(t, err)
	require.Equal(t, []PricingDeriveStaleGroup{
		{ChannelID: 2, GroupID: 20, Reason: PricingDeriveStaleMissing},
		{ChannelID: 2, GroupID: 21, Reason: PricingDeriveStaleMissing},
	}, r.Stale)
}

func TestPricingDeriveBatch_ApplyVerificationReadFailureIsReported(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	orig := e.repo.getChannelIDByGroupIDFn
	e.repo.getChannelIDByGroupIDFn = func(ctx context.Context, gid int64) (int64, error) {
		if len(e.matrix.applyCalls) > 0 { // 落库之后的读取（核对）失败
			return 0, errors.New("db down")
		}
		return orig(ctx, gid)
	}

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{Apply: true})
	require.NoError(t, err)
	require.Len(t, r.Failures, 1)
	require.Contains(t, r.Failures[0].Error, "get channel of group 10")
}

func TestPricingDeriveBatch_GroupMissingFromMetaIsTreatedAsDeleted(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 99)) // 99 没有分组记录

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{})
	require.NoError(t, err)
	require.Empty(t, r.Failures)
	require.Len(t, r.Groups, 2)
	require.Equal(t, int64(99), mxBatchGroup(t, r, 99).GroupID)
}

func TestPricingDeriveBatch_GroupModeOwnerChannelReadFailure(t *testing.T) {
	e := mxBatchEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	e.repo.getByIDFn = func(context.Context, int64) (*Channel, error) { return nil, errors.New("db down") }

	r, err := e.svc.DeriveBatch(context.Background(), PricingDeriveBatchOptions{GroupID: 10})
	require.NoError(t, err)
	require.Len(t, r.Failures, 1)
	require.Contains(t, r.Failures[0].Error, "db down")
}

func TestPricingDerivationService_RefreshGroupsErrors(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))

	report, err := e.svc.RefreshGroups(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, report.Groups)

	_, err = e.svc.RefreshGroups(context.Background(), []int64{10, 404})
	require.ErrorIs(t, err, ErrGroupNotFound)

	e.matrix.metaErr = errors.New("meta down")
	_, err = e.svc.RefreshGroups(context.Background(), []int64{10})
	require.ErrorContains(t, err, "get group meta")
}

func TestUnconvergedGroups(t *testing.T) {
	require.Empty(t, unconvergedGroups(nil))
	got := unconvergedGroups([]PricingDeriveGroupSummary{
		{GroupID: 1, Status: PricingDeriveStatusUnchanged},
		{GroupID: 2, Status: PricingDeriveStatusChanged},
		{GroupID: 3, Status: PricingDeriveStatusSkipped},
		{GroupID: 4, Status: PricingDeriveStatusChanged},
	})
	require.Equal(t, []int64{2, 4}, got)
}

func TestPricingDeriveDrifted(t *testing.T) {
	planned := PricingDeriveGroupSummary{
		Status: PricingDeriveStatusChanged, Config: PricingDeriveConfigNew,
		CellsNew: 2, CellsUpdated: 1, CellsDeleted: 1, RulesNew: 1, RulesDeleted: 1,
	}
	same := PricingRefreshGroupResult{ConfigWritten: true, CellsInserted: 2, CellsUpdated: 1, CellsDeleted: 1, RulesReplaced: 2}
	require.False(t, pricingDeriveDrifted(planned, same))

	for name, mutate := range map[string]func(g *PricingRefreshGroupResult){
		"跳过状态不一致": func(g *PricingRefreshGroupResult) { g.Skipped = true },
		"配置写入不一致": func(g *PricingRefreshGroupResult) { g.ConfigWritten = false },
		"新增单元格数":  func(g *PricingRefreshGroupResult) { g.CellsInserted++ },
		"更新单元格数":  func(g *PricingRefreshGroupResult) { g.CellsUpdated++ },
		"删除单元格数":  func(g *PricingRefreshGroupResult) { g.CellsDeleted++ },
		"规则替换数":   func(g *PricingRefreshGroupResult) { g.RulesReplaced++ },
	} {
		t.Run(name, func(t *testing.T) {
			g := same
			mutate(&g)
			require.True(t, pricingDeriveDrifted(planned, g))
		})
	}

	skipped := PricingDeriveGroupSummary{Status: PricingDeriveStatusSkipped}
	require.False(t, pricingDeriveDrifted(skipped, PricingRefreshGroupResult{Skipped: true}), "两边都跳过不算漂移")
	require.True(t, pricingDeriveDrifted(skipped, PricingRefreshGroupResult{}))
}

func TestSummarizePricingDerive_CountsModesAndClampsUnchanged(t *testing.T) {
	view := &GroupDeriveView{
		GroupID: 7, Platform: PlatformOpenAI,
		StoredConfig: &StoredGroupConfig{GroupID: 7, PricingStage: PricingStageShadow},
		Derived: DerivedGroupState{
			ChannelID: 3,
			Cells: []MatrixCell{
				{ModelKey: "a", Open: false},
				{ModelKey: "b", Open: true, PriceMode: MatrixPriceExtra},
				{ModelKey: "c", Open: true, PriceMode: MatrixPriceCustom},
				{ModelKey: "d", Open: true, PriceMode: MatrixPriceInherit},
			},
			Notes: []DerivationNote{{Level: DerivationNoteWarn, Code: "w"}, {Level: DerivationNoteInfo, Code: "i"}},
		},
	}
	cfg := defaultMatrixGroupConfig()
	plan := GroupApplyPlan{
		GroupID: 7, ConfigWrite: &cfg,
		CellInserts: make([]MatrixCell, 5), // 比派生出的单元格还多，未变数要夹到 0
	}

	sm := summarizePricingDerive(view, plan)
	require.Equal(t, 1, sm.Closed)
	require.Equal(t, 1, sm.Extra)
	require.Equal(t, 1, sm.Custom)
	require.Equal(t, 1, sm.Inherit)
	require.Equal(t, string(PricingStageShadow), sm.Stage)
	require.Equal(t, PricingDeriveConfigUpdated, sm.Config, "库里已有配置行则是更新")
	require.Equal(t, 1, sm.Warnings)
	require.Equal(t, 5, sm.CellsNew)
	require.Zero(t, sm.CellsUnchanged)
	require.Equal(t, PricingDeriveStatusChanged, sm.Status)

	view.StoredConfig = nil
	require.Equal(t, PricingDeriveConfigNew, summarizePricingDerive(view, plan).Config)
}

func TestSummarizePricingDerive_SkippedPlan(t *testing.T) {
	view := &GroupDeriveView{GroupID: 8, Platform: PlatformOpenAI}
	sm := summarizePricingDerive(view, GroupApplyPlan{GroupID: 8, Skipped: true, SkipReason: planSkipStageV2})
	require.Equal(t, PricingDeriveStatusSkipped, sm.Status)
	require.Equal(t, PricingDeriveConfigSkipped, sm.Config)
	require.Equal(t, planSkipStageV2, sm.SkipReason)
}
