package service

import (
	"fmt"
	"sort"
)

// 派生结果落库的计划（纯函数，没有 I/O）。
//
// 钩子的规则（设计文档 4.2「混合阶段规则」、2.5、CHECK_OPUS_3 1.4）：
//   - pricing_stage = 'v2' 的分组整体跳过：单元格、成本核算行、group_model_config 行一律不覆盖；
//   - 其余分组：group_model_config 行刷新（保留阶段与版本号，内容变了才把 revision 加一），
//     单元格只增删改 source = legacy_derived 的行，其他来源的行不碰，
//     成本核算行把该分组全部 legacy_derived 行（不限来源渠道）整批删除重建。

// GroupStateSnapshot 一个分组在库里的现状。
type GroupStateSnapshot struct {
	// Config 为 nil 表示 group_model_config 行不存在（= 默认值、legacy）。
	Config *StoredGroupConfig
	Cells  []StoredMatrixCell
	// Rules 是 scope_group_id 等于该分组的全部成本核算行（任意来源）。
	Rules []StoredMatrixCostRule
}

// GroupApplyPlan 一个分组的落库计划。
type GroupApplyPlan struct {
	GroupID    int64
	Skipped    bool
	SkipReason string

	// ConfigWrite 非 nil 表示要写 group_model_config：行不存在就插入，存在就更新。
	ConfigWrite *MatrixGroupConfig

	CellInserts []MatrixCell
	CellUpdates []StoredMatrixCell // 带 ID，内容是新值
	CellDeletes []int64

	RuleDeletes []int64
	RuleInserts []MatrixCostRule

	Notes []DerivationNote
}

// Empty 计划里没有任何写操作。
func (p GroupApplyPlan) Empty() bool {
	return p.ConfigWrite == nil && len(p.CellInserts) == 0 && len(p.CellUpdates) == 0 &&
		len(p.CellDeletes) == 0 && len(p.RuleDeletes) == 0 && len(p.RuleInserts) == 0
}

const planSkipStageV2 = "pricing_stage_v2"

type matrixCellKey struct {
	key       string
	isPattern bool
}

// PlanGroupApply 比较派生结果与库里现状，生成落库计划。
func PlanGroupApply(derived DerivedGroupState, snap GroupStateSnapshot) GroupApplyPlan {
	plan := GroupApplyPlan{GroupID: derived.GroupID}
	if snap.Config != nil && snap.Config.PricingStage == PricingStageV2 {
		plan.Skipped = true
		plan.SkipReason = planSkipStageV2
		return plan
	}

	planConfig(&plan, derived, snap)
	planCells(&plan, derived, snap)
	planCostRules(&plan, derived, snap)
	return plan
}

// normalizeMatrixConfig 把可能为 nil 的切片与 map 规范成空值，保证比较稳定。
func normalizeMatrixConfig(c MatrixGroupConfig) MatrixGroupConfig {
	if c.ModelMapping == nil {
		c.ModelMapping = []MatrixMappingEntry{}
	}
	if c.Features == nil {
		c.Features = map[string]any{}
	}
	return c
}

func planConfig(plan *GroupApplyPlan, derived DerivedGroupState, snap GroupStateSnapshot) {
	want := normalizeMatrixConfig(derived.Config)
	switch {
	case snap.Config == nil:
		// 行不存在 = 默认值：派生结果也是默认状态（无渠道）时不需要建行。
		if derived.ChannelID != 0 {
			plan.ConfigWrite = &want
		}
	case matrixCanonicalJSON(normalizeMatrixConfig(snap.Config.MatrixGroupConfig)) != matrixCanonicalJSON(want):
		plan.ConfigWrite = &want
	}
}

// matrixCellContent 单元格里参与比较的内容（不含 source、版本号、时间戳）。
func matrixCellContent(c MatrixCell) string {
	c.Source = ""
	return matrixCanonicalJSON(c)
}

func planCells(plan *GroupApplyPlan, derived DerivedGroupState, snap GroupStateSnapshot) {
	existing := make(map[matrixCellKey]StoredMatrixCell, len(snap.Cells))
	for _, c := range snap.Cells {
		existing[matrixCellKey{c.ModelKey, c.IsPattern}] = c
	}
	wanted := make(map[matrixCellKey]struct{}, len(derived.Cells))
	for _, c := range derived.Cells {
		k := matrixCellKey{c.ModelKey, c.IsPattern}
		wanted[k] = struct{}{}
		cur, ok := existing[k]
		switch {
		case !ok:
			plan.CellInserts = append(plan.CellInserts, c)
		case cur.Source != MatrixSourceLegacyDerived:
			plan.Notes = append(plan.Notes, DerivationNote{
				Level: DerivationNoteWarn, Code: "cell_blocked_by_non_derived", Model: c.ModelKey,
				Message: fmt.Sprintf("该键已有来源为 %s 的单元格，派生不覆盖", cur.Source),
			})
		case matrixCellContent(cur.MatrixCell) != matrixCellContent(c):
			updated := cur
			updated.MatrixCell = c
			plan.CellUpdates = append(plan.CellUpdates, updated)
		}
	}
	for _, c := range snap.Cells {
		if c.Source != MatrixSourceLegacyDerived {
			continue
		}
		if _, ok := wanted[matrixCellKey{c.ModelKey, c.IsPattern}]; !ok {
			plan.CellDeletes = append(plan.CellDeletes, c.ID)
		}
	}
	sort.Slice(plan.CellDeletes, func(i, j int) bool { return plan.CellDeletes[i] < plan.CellDeletes[j] })
}

func planCostRules(plan *GroupApplyPlan, derived DerivedGroupState, snap GroupStateSnapshot) {
	var current []StoredMatrixCostRule
	for _, r := range snap.Rules {
		if r.Source == MatrixSourceLegacyDerived {
			current = append(current, r)
		}
	}
	sort.SliceStable(current, func(i, j int) bool {
		a, b := current[i], current[j]
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		if a.SourceOrdinal != b.SourceOrdinal {
			return a.SourceOrdinal < b.SourceOrdinal
		}
		return a.ID < b.ID
	})
	if matrixCostRulesEqual(current, derived.CostRules) {
		return
	}
	// 整批重建：规则 id 没有任何引用方，价格行随规则级联删除。
	for _, r := range current {
		plan.RuleDeletes = append(plan.RuleDeletes, r.ID)
	}
	plan.RuleInserts = append(plan.RuleInserts, derived.CostRules...)
}

func matrixCostRulesEqual(current []StoredMatrixCostRule, want []MatrixCostRule) bool {
	if len(current) != len(want) {
		return false
	}
	for i := range current {
		if matrixCanonicalJSON(normalizeMatrixCostRule(current[i].MatrixCostRule)) != matrixCanonicalJSON(normalizeMatrixCostRule(want[i])) {
			return false
		}
	}
	return true
}

// normalizeMatrixCostRule 把 nil 切片规范成空切片，保证比较稳定。
func normalizeMatrixCostRule(r MatrixCostRule) MatrixCostRule {
	if r.GroupIDs == nil {
		r.GroupIDs = []int64{}
	}
	if r.AccountIDs == nil {
		r.AccountIDs = []int64{}
	}
	if r.Prices == nil {
		r.Prices = []MatrixCostRulePrice{}
	}
	for i := range r.Prices {
		if r.Prices[i].Models == nil {
			r.Prices[i].Models = []string{}
		}
	}
	return r
}
