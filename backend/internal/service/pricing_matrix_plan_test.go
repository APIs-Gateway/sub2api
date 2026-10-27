//go:build unit

package service

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 夹具：把「库」建模成快照，把执行器建模成 mxApplyPlan，
// 这样混合阶段与幂等性不需要数据库就能验证（数据库侧由集成测试覆盖）。
// ---------------------------------------------------------------------------

func mxStoredCell(id int64, c MatrixCell) StoredMatrixCell {
	return StoredMatrixCell{ID: id, GroupID: 10, Revision: 1, MatrixCell: c}
}

func mxStoredRule(id int64, source MatrixSource, r MatrixCostRule) StoredMatrixCostRule {
	return StoredMatrixCostRule{ID: id, ScopeGroupID: 10, Source: source, MatrixCostRule: r}
}

func mxSnapshotFrom(d DerivedGroupState, stage PricingStage) GroupStateSnapshot {
	snap := GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: d.GroupID, MatrixGroupConfig: d.Config, PricingStage: stage, Revision: 1}}
	for i, c := range d.Cells {
		snap.Cells = append(snap.Cells, mxStoredCell(int64(100+i), c))
	}
	for i, r := range d.CostRules {
		snap.Rules = append(snap.Rules, mxStoredRule(int64(200+i), MatrixSourceLegacyDerived, r))
	}
	return snap
}

var mxNextID int64 = 1000

// mxApplyPlan 模拟执行器：按计划修改快照。
func mxApplyPlan(snap GroupStateSnapshot, plan GroupApplyPlan) GroupStateSnapshot {
	if plan.Skipped {
		return snap
	}
	out := GroupStateSnapshot{}
	if snap.Config != nil {
		cfg := *snap.Config
		out.Config = &cfg
	}
	if plan.ConfigWrite != nil {
		if out.Config == nil {
			out.Config = &StoredGroupConfig{GroupID: plan.GroupID, PricingStage: PricingStageLegacy}
		}
		out.Config.MatrixGroupConfig = *plan.ConfigWrite
		out.Config.Revision++
	}

	deleted := map[int64]bool{}
	for _, id := range plan.CellDeletes {
		deleted[id] = true
	}
	updated := map[int64]StoredMatrixCell{}
	for _, c := range plan.CellUpdates {
		updated[c.ID] = c
	}
	for _, c := range snap.Cells {
		switch {
		case deleted[c.ID]:
		case updated[c.ID].ID != 0:
			out.Cells = append(out.Cells, updated[c.ID])
		default:
			out.Cells = append(out.Cells, c)
		}
	}
	for _, c := range plan.CellInserts {
		mxNextID++
		out.Cells = append(out.Cells, mxStoredCell(mxNextID, c))
	}

	ruleDeleted := map[int64]bool{}
	for _, id := range plan.RuleDeletes {
		ruleDeleted[id] = true
	}
	for _, r := range snap.Rules {
		if !ruleDeleted[r.ID] {
			out.Rules = append(out.Rules, r)
		}
	}
	for _, r := range plan.RuleInserts {
		mxNextID++
		out.Rules = append(out.Rules, mxStoredRule(mxNextID, MatrixSourceLegacyDerived, r))
	}
	return out
}

func mxSortedCells(cells []StoredMatrixCell) []MatrixCell {
	out := make([]MatrixCell, 0, len(cells))
	for _, c := range cells {
		out = append(out, c.MatrixCell)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsPattern != out[j].IsPattern {
			return !out[i].IsPattern
		}
		if out[i].IsPattern {
			return out[i].PatternOrder < out[j].PatternOrder
		}
		return out[i].ModelKey < out[j].ModelKey
	})
	return out
}

func mxDerivedRules(rules []StoredMatrixCostRule) []MatrixCostRule {
	out := []MatrixCostRule{}
	for _, r := range rules {
		if r.Source == MatrixSourceLegacyDerived {
			out = append(out, r.MatrixCostRule)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceOrdinal < out[j].SourceOrdinal })
	return out
}

func mxPricedChannel(model string, price float64) *Channel {
	ch := mxChannel(1, 10)
	p := mxPricing(1, PlatformOpenAI, BillingModeToken, model)
	p.InputPrice = mxF(price)
	ch.ModelPricing = []ChannelModelPricing{p}
	return ch
}

// ---------------------------------------------------------------------------
// 阶段：v2 整体跳过
// ---------------------------------------------------------------------------

func TestPlanGroupApply_SkipsV2GroupsEntirely(t *testing.T) {
	derived := DeriveGroupState(mxPricedChannel("gpt-5.5", 1e-6), mxGroup(10, PlatformOpenAI), nil)
	// v2 分组里的现状与派生结果完全不同：配置、单元格、成本核算规则都不能被动。
	snap := GroupStateSnapshot{
		Config: &StoredGroupConfig{GroupID: 10, PricingStage: PricingStageV2, Revision: 7,
			MatrixGroupConfig: MatrixGroupConfig{AccessMode: MatrixAccessAllowlist, CostMode: MatrixCostFollowBilling}},
		Cells: []StoredMatrixCell{
			mxStoredCell(1, MatrixCell{ModelKey: "gpt-5.5", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived}),
			mxStoredCell(2, MatrixCell{ModelKey: "stale", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived}),
		},
		Rules: []StoredMatrixCostRule{mxStoredRule(3, MatrixSourceLegacyDerived, MatrixCostRule{Name: "x", SourceChannelID: 1, SourceOrdinal: 1})},
	}
	plan := PlanGroupApply(derived, snap)
	require.True(t, plan.Skipped)
	require.Equal(t, planSkipStageV2, plan.SkipReason)
	require.True(t, plan.Empty())
	require.Equal(t, snap, mxApplyPlan(snap, plan))
}

func TestPlanGroupApply_ShadowAndLegacyAreRefreshed(t *testing.T) {
	for _, stage := range []PricingStage{PricingStageLegacy, PricingStageShadow} {
		t.Run(string(stage), func(t *testing.T) {
			derived := DeriveGroupState(mxPricedChannel("gpt-5.5", 1e-6), mxGroup(10, PlatformOpenAI), nil)
			snap := GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 10, PricingStage: stage, Revision: 3,
				MatrixGroupConfig: defaultMatrixGroupConfig()}}
			plan := PlanGroupApply(derived, snap)
			require.False(t, plan.Skipped)
			require.NotNil(t, plan.ConfigWrite, "配置变了必须写")
			require.Len(t, plan.CellInserts, 1)
		})
	}
}

// ---------------------------------------------------------------------------
// group_model_config
// ---------------------------------------------------------------------------

func TestPlanGroupApply_Config(t *testing.T) {
	withChannel := DeriveGroupState(mxChannel(1, 10), mxGroup(10, PlatformOpenAI), nil)
	noChannel := DeriveGroupState(nil, mxGroup(10, PlatformOpenAI), nil)

	t.Run("没有行且派生为默认状态：不建行", func(t *testing.T) {
		plan := PlanGroupApply(noChannel, GroupStateSnapshot{})
		require.True(t, plan.Empty())
	})
	t.Run("没有行但分组有渠道：建行", func(t *testing.T) {
		plan := PlanGroupApply(withChannel, GroupStateSnapshot{})
		require.NotNil(t, plan.ConfigWrite)
		require.Equal(t, withChannel.Config, *plan.ConfigWrite)
	})
	t.Run("内容相同：不写（nil 与空切片视为相同）", func(t *testing.T) {
		stored := withChannel.Config
		stored.ModelMapping = nil
		stored.Features = nil
		plan := PlanGroupApply(withChannel, GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 10, MatrixGroupConfig: stored, PricingStage: PricingStageShadow}})
		require.Nil(t, plan.ConfigWrite)
	})
	t.Run("内容不同：写", func(t *testing.T) {
		stored := withChannel.Config
		stored.AccessMode = MatrixAccessAllowlist
		plan := PlanGroupApply(withChannel, GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 10, MatrixGroupConfig: stored, PricingStage: PricingStageLegacy}})
		require.NotNil(t, plan.ConfigWrite)
		require.Equal(t, MatrixAccessOpen, plan.ConfigWrite.AccessMode)
	})
	t.Run("渠道被解绑：把行恢复成默认状态", func(t *testing.T) {
		plan := PlanGroupApply(noChannel, mxSnapshotFrom(withChannel, PricingStageLegacy))
		require.NotNil(t, plan.ConfigWrite)
		require.Nil(t, plan.ConfigWrite.BillingModelSource)
	})
}

// ---------------------------------------------------------------------------
// 单元格
// ---------------------------------------------------------------------------

func TestPlanGroupApply_Cells(t *testing.T) {
	derived := DeriveGroupState(mxPricedChannel("gpt-5.5", 2e-6), mxGroup(10, PlatformOpenAI), nil)
	require.Len(t, derived.Cells, 1)
	want := derived.Cells[0]
	cfg := &StoredGroupConfig{GroupID: 10, MatrixGroupConfig: derived.Config, PricingStage: PricingStageLegacy}

	t.Run("新增", func(t *testing.T) {
		plan := PlanGroupApply(derived, GroupStateSnapshot{Config: cfg})
		require.Equal(t, []MatrixCell{want}, plan.CellInserts)
		require.Empty(t, plan.CellUpdates)
		require.Empty(t, plan.CellDeletes)
	})
	t.Run("内容相同不写", func(t *testing.T) {
		plan := PlanGroupApply(derived, GroupStateSnapshot{Config: cfg, Cells: []StoredMatrixCell{mxStoredCell(5, want)}})
		require.True(t, plan.Empty())
	})
	t.Run("内容不同：原地更新并保留 id", func(t *testing.T) {
		old := want
		old.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: mxF(9)}
		plan := PlanGroupApply(derived, GroupStateSnapshot{Config: cfg, Cells: []StoredMatrixCell{mxStoredCell(5, old)}})
		require.Len(t, plan.CellUpdates, 1)
		require.Equal(t, int64(5), plan.CellUpdates[0].ID)
		require.Equal(t, want, plan.CellUpdates[0].MatrixCell)
		require.Empty(t, plan.CellInserts)
		require.Empty(t, plan.CellDeletes)
	})
	t.Run("派生里已经没有的 legacy_derived 行：删除，按 id 升序", func(t *testing.T) {
		stale := func(key string) MatrixCell {
			return MatrixCell{ModelKey: key, Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived}
		}
		plan := PlanGroupApply(derived, GroupStateSnapshot{Config: cfg, Cells: []StoredMatrixCell{
			mxStoredCell(9, stale("b")), mxStoredCell(5, want), mxStoredCell(7, stale("a")),
		}})
		require.Equal(t, []int64{7, 9}, plan.CellDeletes)
		require.Empty(t, plan.CellInserts)
		require.Empty(t, plan.CellUpdates)
	})
	t.Run("非 legacy_derived 的行一律不碰", func(t *testing.T) {
		for _, src := range []MatrixSource{MatrixSourceManual, MatrixSourceCopied, MatrixSourceLegacyFrozen} {
			manual := MatrixCell{ModelKey: "manual-model", Open: false, PriceMode: MatrixPriceExtra, ExtraMultiplier: mxF(1.2), Source: src}
			plan := PlanGroupApply(derived, GroupStateSnapshot{Config: cfg, Cells: []StoredMatrixCell{mxStoredCell(1, manual), mxStoredCell(5, want)}})
			require.True(t, plan.Empty(), src)
		}
	})
	t.Run("同键已有非 legacy_derived 的行：不覆盖，留备注", func(t *testing.T) {
		manual := MatrixCell{ModelKey: want.ModelKey, Open: false, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual}
		plan := PlanGroupApply(derived, GroupStateSnapshot{Config: cfg, Cells: []StoredMatrixCell{mxStoredCell(1, manual)}})
		require.Empty(t, plan.CellInserts)
		require.Empty(t, plan.CellUpdates)
		require.Empty(t, plan.CellDeletes)
		require.Contains(t, mxNoteCodes(plan.Notes), "cell_blocked_by_non_derived")
	})
	t.Run("精确名与通配符是两个独立的键", func(t *testing.T) {
		ch := mxChannel(1, 10)
		priced := mxPricing(1, PlatformOpenAI, BillingModeToken, "gpt-5", "gpt-5*")
		priced.InputPrice = mxF(1)
		ch.ModelPricing = []ChannelModelPricing{priced}
		d := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
		require.Len(t, d.Cells, 2)
		plan := PlanGroupApply(d, GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 10, MatrixGroupConfig: d.Config, PricingStage: PricingStageLegacy}})
		require.Len(t, plan.CellInserts, 2)
	})
}

// ---------------------------------------------------------------------------
// 成本核算规则
// ---------------------------------------------------------------------------

func TestPlanGroupApply_CostRules(t *testing.T) {
	ch := mxChannel(7, 10)
	ch.AccountStatsPricingRules = []AccountStatsPricingRule{
		{ID: 1, Name: "a", SortOrder: 1, GroupIDs: []int64{10}},
		{ID: 2, Name: "b", SortOrder: 2},
	}
	derived := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), nil)
	require.Len(t, derived.CostRules, 2)
	cfg := &StoredGroupConfig{GroupID: 10, MatrixGroupConfig: derived.Config, PricingStage: PricingStageLegacy}

	t.Run("没有现状：全部新增", func(t *testing.T) {
		plan := PlanGroupApply(derived, GroupStateSnapshot{Config: cfg})
		require.Equal(t, derived.CostRules, plan.RuleInserts)
		require.Empty(t, plan.RuleDeletes)
	})
	t.Run("完全一致：不动", func(t *testing.T) {
		snap := mxSnapshotFrom(derived, PricingStageLegacy)
		// 顺序打乱也一样（按 sort_order、ordinal 排序后比较）。
		snap.Rules[0], snap.Rules[1] = snap.Rules[1], snap.Rules[0]
		require.True(t, PlanGroupApply(derived, snap).Empty())
	})
	t.Run("有差异：整批删除重建", func(t *testing.T) {
		snap := mxSnapshotFrom(derived, PricingStageLegacy)
		snap.Rules[1].Name = "changed"
		plan := PlanGroupApply(derived, snap)
		require.ElementsMatch(t, []int64{200, 201}, plan.RuleDeletes)
		require.Equal(t, derived.CostRules, plan.RuleInserts)
	})
	t.Run("渠道里规则被删光：删光该分组的派生行", func(t *testing.T) {
		snap := mxSnapshotFrom(derived, PricingStageLegacy)
		ch2 := ch.Clone()
		ch2.AccountStatsPricingRules = nil
		plan := PlanGroupApply(DeriveGroupState(ch2, mxGroup(10, PlatformOpenAI), nil), snap)
		require.ElementsMatch(t, []int64{200, 201}, plan.RuleDeletes)
		require.Empty(t, plan.RuleInserts)
	})
	t.Run("来自别的渠道的派生行也一并清掉（分组换了渠道，CHECK_OPUS_3 1.4-1）", func(t *testing.T) {
		old := mxSnapshotFrom(derived, PricingStageLegacy)
		for i := range old.Rules {
			old.Rules[i].SourceChannelID = 99
		}
		plan := PlanGroupApply(derived, old)
		require.ElementsMatch(t, []int64{200, 201}, plan.RuleDeletes)
		require.Equal(t, derived.CostRules, plan.RuleInserts)
	})
	t.Run("手工规则（manual）不碰", func(t *testing.T) {
		snap := mxSnapshotFrom(derived, PricingStageLegacy)
		snap.Rules = append(snap.Rules, mxStoredRule(300, MatrixSourceManual, MatrixCostRule{Name: "mine", SortOrder: 0}))
		require.True(t, PlanGroupApply(derived, snap).Empty())

		snap.Rules[0].Name = "drift"
		plan := PlanGroupApply(derived, snap)
		require.NotContains(t, plan.RuleDeletes, int64(300))
	})
}

// ---------------------------------------------------------------------------
// 幂等与收敛
// ---------------------------------------------------------------------------

func TestPlanGroupApply_IdempotentAfterApply(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	for i := 0; i < 300; i++ {
		ch := mxRandomChannel(r)
		derived := DeriveGroupState(ch, mxGroup(10, PlatformOpenAI), mxFactsForPools())

		snap := GroupStateSnapshot{}
		first := PlanGroupApply(derived, snap)
		after := mxApplyPlan(snap, first)
		second := PlanGroupApply(derived, after)
		require.True(t, second.Empty(), "第二次应当什么都不做：%+v", second)
		require.Equal(t, derived.Cells, mxSortedCells(after.Cells))
		require.Equal(t, derived.CostRules, mxDerivedRules(after.Rules))
	}
}

func TestPlanGroupApply_ConvergesFromAnyPreviousStateAndKeepsManualRows(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	manualCell := MatrixCell{ModelKey: "zz-manual", Open: true, PriceMode: MatrixPriceExtra, ExtraMultiplier: mxF(1.5), Source: MatrixSourceManual}
	manualRule := MatrixCostRule{Name: "manual-rule", Enabled: true}

	for i := 0; i < 200; i++ {
		prev := DeriveGroupState(mxRandomChannel(r), mxGroup(10, PlatformOpenAI), mxFactsForPools())
		next := DeriveGroupState(mxRandomChannel(r), mxGroup(10, PlatformOpenAI), mxFactsForPools())

		snap := mxSnapshotFrom(prev, PricingStageShadow)
		snap.Cells = append(snap.Cells, mxStoredCell(900, manualCell))
		snap.Rules = append(snap.Rules, mxStoredRule(901, MatrixSourceManual, manualRule))

		after := mxApplyPlan(snap, PlanGroupApply(next, snap))

		var derivedCells []StoredMatrixCell
		manualKept := false
		for _, c := range after.Cells {
			if c.Source == MatrixSourceLegacyDerived {
				derivedCells = append(derivedCells, c)
			} else if c.ID == 900 {
				manualKept = true
				require.Equal(t, manualCell, c.MatrixCell)
			}
		}
		require.True(t, manualKept, "手工单元格必须保留")
		require.Equal(t, next.Cells, mxSortedCells(derivedCells), "派生行收敛到最新的派生结果")
		require.Equal(t, next.CostRules, mxDerivedRules(after.Rules))
		require.Equal(t, PricingStageShadow, after.Config.PricingStage, "阶段不被钩子改动")

		manualRuleKept := false
		for _, rule := range after.Rules {
			if rule.ID == 901 {
				manualRuleKept = true
			}
		}
		require.True(t, manualRuleKept, "手工成本核算规则必须保留")
		require.True(t, PlanGroupApply(next, after).Empty())
	}
}

// 混合阶段：同一个渠道下的多个分组各处在不同阶段，v2 的那些一个字节都不动。
func TestPlanGroupApply_MixedStagesOnOneChannel(t *testing.T) {
	ch := mxPricedChannel("gpt-5.5", 3e-6)
	ch.GroupIDs = []int64{10, 11, 12}
	ch.AccountStatsPricingRules = []AccountStatsPricingRule{{ID: 1, Name: "r", SortOrder: 1}}

	stages := map[int64]PricingStage{10: PricingStageLegacy, 11: PricingStageShadow, 12: PricingStageV2}
	snaps := map[int64]GroupStateSnapshot{}
	for gid, stage := range stages {
		snaps[gid] = GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: gid, PricingStage: stage, Revision: 1,
			MatrixGroupConfig: MatrixGroupConfig{AccessMode: MatrixAccessAllowlist, CostMode: MatrixCostAccountRate}}}
	}

	for gid, stage := range stages {
		derived := DeriveGroupState(ch, mxGroup(gid, PlatformOpenAI), nil)
		plan := PlanGroupApply(derived, snaps[gid])
		after := mxApplyPlan(snaps[gid], plan)
		if stage == PricingStageV2 {
			require.True(t, plan.Skipped)
			require.Equal(t, snaps[gid], after, "v2 分组必须保持原样")
			continue
		}
		require.False(t, plan.Skipped)
		require.Equal(t, MatrixAccessOpen, after.Config.AccessMode)
		require.Len(t, after.Cells, 1)
		require.Len(t, after.Rules, 1)
		require.Equal(t, stage, after.Config.PricingStage)
	}
}
