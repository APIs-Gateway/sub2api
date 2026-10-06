package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// 批量派生（`sub2api pricing-matrix derive`）。
//
// 迁移之后三张矩阵表是空的，而唯一的写入方是渠道保存钩子，所以上线时需要一个批量入口。
// 这里不另写派生与落库逻辑：
//   - dry-run 用 buildViewPlan（与只读预览接口同一套 DeriveGroupState + PlanGroupApply）实时对比库里现状；
//   - apply 调用 RefreshChannel / RefreshGroups，也就是渠道保存钩子（AfterChannelSaved）里的那条路径：
//     同一个互斥锁、同一个 ApplyPlans 事务（每个渠道一个事务）、同一套混合阶段规则
//     （v2 整体跳过；其余阶段保留 pricing_stage，只动 legacy_derived 来源的行）。
//
// 它不改任何分组的 pricing_stage，不碰 channel_model_pricing 与 groups。

// 批量派生的模式。
const (
	PricingDeriveModeDryRun = "dry-run"
	PricingDeriveModeApply  = "apply"
)

// 分组摘要的状态。
const (
	PricingDeriveStatusChanged   = "changed"
	PricingDeriveStatusUnchanged = "unchanged"
	PricingDeriveStatusSkipped   = "skipped_v2"
)

// 配置行的变化。
const (
	PricingDeriveConfigNew       = "new"
	PricingDeriveConfigUpdated   = "updated"
	PricingDeriveConfigUnchanged = "unchanged"
	PricingDeriveConfigSkipped   = "skipped"
)

const noteCellBlockedByNonDerived = "cell_blocked_by_non_derived"

// PricingDeriveBatchOptions 批量派生的参数。ChannelID 与 GroupID 都为 0 表示全部启用的渠道。
type PricingDeriveBatchOptions struct {
	// ChannelID 只处理这个渠道（不论是否启用：停用渠道按「无渠道」派生）。
	ChannelID int64
	// GroupID 只处理这个分组（按它当前所属的渠道派生）。与 ChannelID 互斥。
	GroupID int64
	// Apply 为 false 时只读，不写任何东西。
	Apply bool
	// ChannelTimeout 每个渠道（一个事务）的超时；0 表示不限。
	ChannelTimeout time.Duration
}

// PricingDeriveGroupSummary 一个分组的批量派生摘要。数量都是「这次要写（或写了）多少」，
// 再跑一次 apply 时应当全部为 unchanged。
type PricingDeriveGroupSummary struct {
	GroupID   int64  `json:"group_id"`
	ChannelID int64  `json:"channel_id"`
	Platform  string `json:"platform"`
	// Stage 库里现有的阶段；没有 group_model_config 行时是 legacy。批量派生不会改它。
	Stage  string `json:"stage"`
	Status string `json:"status"`
	// SkipReason 在 Status 为 skipped_v2 时给出原因。
	SkipReason string `json:"skip_reason,omitempty"`
	// Orphan 为 true 表示分组已不在该渠道里，但库里还留着它的派生成本核算行（会被清掉）。
	Orphan bool `json:"orphan,omitempty"`

	Config string `json:"config"`

	CellsNew       int `json:"cells_new"`
	CellsUpdated   int `json:"cells_updated"`
	CellsDeleted   int `json:"cells_deleted"`
	CellsUnchanged int `json:"cells_unchanged"`
	// CellsBlocked 已有非派生来源（manual、copied、legacy_frozen）的同键单元格，派生不覆盖。
	CellsBlocked int `json:"cells_blocked"`

	RulesNew       int `json:"rules_new"`
	RulesDeleted   int `json:"rules_deleted"`
	RulesUnchanged int `json:"rules_unchanged"`

	// 派生出的单元格里各价格模式的数量；Closed 是未开放的单元格（不计入前三项）。
	Inherit int `json:"inherit"`
	Extra   int `json:"extra"`
	Custom  int `json:"custom"`
	Closed  int `json:"closed"`

	Warnings int              `json:"warnings"`
	Notes    []DerivationNote `json:"notes"`
	// Applied 为 true 表示已经落库（apply 模式且该渠道的事务成功）。
	Applied bool `json:"applied"`
}

// PricingDeriveFailure 一个渠道（或分组）处理失败。
type PricingDeriveFailure struct {
	ChannelID int64  `json:"channel_id,omitempty"`
	GroupID   int64  `json:"group_id,omitempty"`
	Error     string `json:"error"`
}

// PricingDeriveTotals 汇总。
type PricingDeriveTotals struct {
	Channels         int `json:"channels"`
	ChannelsInactive int `json:"channels_skipped_inactive"`
	Groups           int `json:"groups"`
	GroupsChanged    int `json:"groups_changed"`
	GroupsUnchanged  int `json:"groups_unchanged"`
	GroupsSkipped    int `json:"groups_skipped_v2"`

	ConfigNew       int `json:"config_new"`
	ConfigUpdated   int `json:"config_updated"`
	ConfigUnchanged int `json:"config_unchanged"`

	CellsNew       int `json:"cells_new"`
	CellsUpdated   int `json:"cells_updated"`
	CellsDeleted   int `json:"cells_deleted"`
	CellsUnchanged int `json:"cells_unchanged"`
	CellsBlocked   int `json:"cells_blocked"`

	RulesNew       int `json:"rules_new"`
	RulesDeleted   int `json:"rules_deleted"`
	RulesUnchanged int `json:"rules_unchanged"`

	Inherit int `json:"inherit"`
	Extra   int `json:"extra"`
	Custom  int `json:"custom"`
	Closed  int `json:"closed"`

	Warnings int `json:"warnings"`
	Failures int `json:"failures"`
}

// PricingDeriveBatchReport 一次批量派生的结果。
type PricingDeriveBatchReport struct {
	Mode     string                      `json:"mode"`
	Groups   []PricingDeriveGroupSummary `json:"groups"`
	Failures []PricingDeriveFailure      `json:"failures"`
	Totals   PricingDeriveTotals         `json:"totals"`
}

// batchUnit 一个事务的处理单元：一个渠道的全部相关分组，或 --group 点名的分组。
type batchUnit struct {
	channelID int64 // 0 表示按分组点名
	groupIDs  []int64
	current   map[int64]bool // 渠道当前关联的分组；其余的是 orphan
}

// DeriveBatch 对所有启用的渠道（或 opts 限定的渠道、分组）派生，并与库里现状对比；opts.Apply 时落库。
// 单个渠道失败不会中断其余渠道，失败记在报告的 Failures 里；只有参数错误、取不到渠道清单、
// 或 ctx 取消才返回 error。
func (s *PricingDerivationService) DeriveBatch(ctx context.Context, opts PricingDeriveBatchOptions) (*PricingDeriveBatchReport, error) {
	if opts.ChannelID != 0 && opts.GroupID != 0 {
		return nil, errors.New("channel and group are mutually exclusive")
	}
	report := &PricingDeriveBatchReport{
		Mode:     PricingDeriveModeDryRun,
		Groups:   []PricingDeriveGroupSummary{},
		Failures: []PricingDeriveFailure{},
	}
	if opts.Apply {
		report.Mode = PricingDeriveModeApply
	}

	units, inactive, err := s.batchUnits(ctx, opts)
	if err != nil {
		return nil, err
	}
	report.Totals.ChannelsInactive = inactive

	for _, u := range units {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		unitCtx, cancel := ctx, context.CancelFunc(func() {})
		if opts.ChannelTimeout > 0 {
			unitCtx, cancel = context.WithTimeout(ctx, opts.ChannelTimeout)
		}
		summaries, err := s.deriveUnit(unitCtx, u, opts.Apply)
		cancel()
		if err != nil {
			report.Failures = append(report.Failures, PricingDeriveFailure{ChannelID: u.channelID, GroupID: firstOrZero(u.channelID, u.groupIDs), Error: err.Error()})
			continue
		}
		report.Groups = append(report.Groups, summaries...)
		if u.channelID != 0 {
			report.Totals.Channels++
		}
	}
	tallyPricingDerive(report)
	return report, nil
}

func firstOrZero(channelID int64, ids []int64) int64 {
	if channelID == 0 && len(ids) == 1 {
		return ids[0]
	}
	return 0
}

// batchUnits 决定要处理哪些渠道/分组。inactive 是被跳过的停用渠道数（只在没有点名时统计）。
func (s *PricingDerivationService) batchUnits(ctx context.Context, opts PricingDeriveBatchOptions) ([]batchUnit, int, error) {
	switch {
	case opts.GroupID != 0:
		meta, err := s.repo.GetGroupMeta(ctx, []int64{opts.GroupID})
		if err != nil {
			return nil, 0, fmt.Errorf("get group meta: %w", err)
		}
		if _, ok := meta[opts.GroupID]; !ok {
			return nil, 0, ErrGroupNotFound
		}
		return []batchUnit{{groupIDs: []int64{opts.GroupID}, current: map[int64]bool{opts.GroupID: true}}}, 0, nil
	case opts.ChannelID != 0:
		u, err := s.channelUnit(ctx, opts.ChannelID)
		if err != nil {
			return nil, 0, err
		}
		if u == nil {
			return nil, 0, ErrChannelNotFound
		}
		return []batchUnit{*u}, 0, nil
	}

	all, err := s.channels.ListAll(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list channels: %w", err)
	}
	var units []batchUnit
	inactive := 0
	for i := range all {
		if !all[i].IsActive() {
			inactive++
			continue
		}
		u, err := s.channelUnit(ctx, all[i].ID)
		if err != nil {
			return nil, 0, err
		}
		if u == nil || len(u.groupIDs) == 0 {
			continue
		}
		units = append(units, *u)
	}
	return units, inactive, nil
}

// channelUnit 与 RefreshChannel 选分组的方式一致：渠道当前关联的分组，加上仍带有该渠道派生成本核算行的分组。
// 渠道不存在返回 nil。
func (s *PricingDerivationService) channelUnit(ctx context.Context, channelID int64) (*batchUnit, error) {
	ch, err := s.loadChannel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, nil
	}
	ruleGroups, err := s.repo.ListDerivedRuleGroupIDs(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("list derived rule groups: %w", err)
	}
	current := make(map[int64]bool, len(ch.GroupIDs))
	for _, id := range ch.GroupIDs {
		current[id] = true
	}
	return &batchUnit{channelID: channelID, groupIDs: uniqueSortedIDs(ch.GroupIDs, ruleGroups), current: current}, nil
}

// deriveUnit 处理一个单元：先实时对比（只读），apply 时再走钩子同一条落库路径。
func (s *PricingDerivationService) deriveUnit(ctx context.Context, u batchUnit, apply bool) ([]PricingDeriveGroupSummary, error) {
	summaries, err := s.summarizeUnit(ctx, u)
	if err != nil {
		return nil, err
	}
	if !apply {
		return summaries, nil
	}

	var refreshed *PricingRefreshReport
	if u.channelID != 0 {
		refreshed, err = s.RefreshChannel(ctx, u.channelID, nil)
	} else {
		refreshed, err = s.RefreshGroups(ctx, u.groupIDs)
	}
	if err != nil {
		return nil, err
	}
	written := make(map[int64]PricingRefreshGroupResult, len(refreshed.Groups))
	for _, g := range refreshed.Groups {
		written[g.GroupID] = g
	}
	for i := range summaries {
		sm := &summaries[i]
		sm.Applied = true
		// 预览与落库之间如果有别的写入（渠道刚被保存），两边的写入量会对不上：留一条备注，不影响结果。
		if g, ok := written[sm.GroupID]; ok && pricingDeriveDrifted(*sm, g) {
			sm.Warnings++
			sm.Notes = append(sm.Notes, DerivationNote{
				Level: DerivationNoteWarn, Code: "plan_drift",
				Message: "落库时的写入量与事先的对比不一致（对比与落库之间配置被改动过），请再跑一次确认",
			})
		}
	}
	return summaries, nil
}

func pricingDeriveDrifted(sm PricingDeriveGroupSummary, g PricingRefreshGroupResult) bool {
	if g.Skipped != (sm.Status == PricingDeriveStatusSkipped) {
		return true
	}
	if g.Skipped {
		return false
	}
	return g.ConfigWritten != (sm.Config == PricingDeriveConfigNew || sm.Config == PricingDeriveConfigUpdated) ||
		g.CellsInserted != sm.CellsNew || g.CellsUpdated != sm.CellsUpdated || g.CellsDeleted != sm.CellsDeleted ||
		g.RulesReplaced != sm.RulesNew+sm.RulesDeleted
}

// summarizeUnit 对单元里的每个分组按它当前所属的渠道实时派生，并与库里现状对比（只读）。
func (s *PricingDerivationService) summarizeUnit(ctx context.Context, u batchUnit) ([]PricingDeriveGroupSummary, error) {
	meta, err := s.repo.GetGroupMeta(ctx, u.groupIDs)
	if err != nil {
		return nil, fmt.Errorf("get group meta: %w", err)
	}
	loaded := map[int64]*Channel{}
	out := make([]PricingDeriveGroupSummary, 0, len(u.groupIDs))
	for _, id := range u.groupIDs {
		g, ok := meta[id]
		if !ok {
			// 分组记录不存在：与软删除同样处理（和 RefreshChannel 一致）。
			g = DeriveGroup{ID: id, Deleted: true}
		}
		g.ID = id

		var owner *Channel
		ownerID, err := s.channels.GetChannelIDByGroupID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get channel of group %d: %w", id, err)
		}
		if ownerID != 0 {
			if _, seen := loaded[ownerID]; !seen {
				ch, err := s.loadChannel(ctx, ownerID)
				if err != nil {
					return nil, err
				}
				loaded[ownerID] = ch
			}
			owner = loaded[ownerID]
		}
		view, plan, err := s.buildViewPlan(ctx, g, owner)
		if err != nil {
			return nil, err
		}
		sm := summarizePricingDerive(view, plan)
		sm.Orphan = u.channelID != 0 && !u.current[id]
		out = append(out, sm)
	}
	return out, nil
}

// summarizePricingDerive 纯函数：把一个分组的派生结果与计划折成摘要。
func summarizePricingDerive(view *GroupDeriveView, plan GroupApplyPlan) PricingDeriveGroupSummary {
	sm := PricingDeriveGroupSummary{
		GroupID:   view.GroupID,
		ChannelID: view.Derived.ChannelID,
		Platform:  view.Platform,
		Stage:     string(PricingStageLegacy),
		Config:    PricingDeriveConfigUnchanged,
		Status:    PricingDeriveStatusUnchanged,
	}
	if view.StoredConfig != nil {
		sm.Stage = string(view.StoredConfig.PricingStage)
	}
	for _, c := range view.Derived.Cells {
		switch {
		case !c.Open:
			sm.Closed++
		case c.PriceMode == MatrixPriceCustom:
			sm.Custom++
		case c.PriceMode == MatrixPriceExtra:
			sm.Extra++
		default:
			sm.Inherit++
		}
	}
	sm.Notes = append(append([]DerivationNote{}, view.Derived.Notes...), plan.Notes...)
	for _, n := range sm.Notes {
		if n.Level == DerivationNoteWarn {
			sm.Warnings++
		}
		if n.Code == noteCellBlockedByNonDerived {
			sm.CellsBlocked++
		}
	}

	if plan.Skipped {
		sm.Status = PricingDeriveStatusSkipped
		sm.SkipReason = plan.SkipReason
		sm.Config = PricingDeriveConfigSkipped
		return sm
	}

	switch {
	case plan.ConfigWrite == nil:
	case view.StoredConfig == nil:
		sm.Config = PricingDeriveConfigNew
	default:
		sm.Config = PricingDeriveConfigUpdated
	}
	sm.CellsNew = len(plan.CellInserts)
	sm.CellsUpdated = len(plan.CellUpdates)
	sm.CellsDeleted = len(plan.CellDeletes)
	sm.CellsUnchanged = len(view.Derived.Cells) - sm.CellsNew - sm.CellsUpdated - sm.CellsBlocked
	if sm.CellsUnchanged < 0 {
		sm.CellsUnchanged = 0
	}
	sm.RulesNew = len(plan.RuleInserts)
	sm.RulesDeleted = len(plan.RuleDeletes)
	if sm.RulesNew == 0 && sm.RulesDeleted == 0 {
		sm.RulesUnchanged = len(view.Derived.CostRules)
	}
	if !plan.Empty() {
		sm.Status = PricingDeriveStatusChanged
	}
	return sm
}

// tallyPricingDerive 汇总分组摘要。
func tallyPricingDerive(r *PricingDeriveBatchReport) {
	t := &r.Totals
	t.Failures = len(r.Failures)
	for _, g := range r.Groups {
		t.Groups++
		switch g.Status {
		case PricingDeriveStatusChanged:
			t.GroupsChanged++
		case PricingDeriveStatusSkipped:
			t.GroupsSkipped++
		default:
			t.GroupsUnchanged++
		}
		switch g.Config {
		case PricingDeriveConfigNew:
			t.ConfigNew++
		case PricingDeriveConfigUpdated:
			t.ConfigUpdated++
		case PricingDeriveConfigUnchanged:
			t.ConfigUnchanged++
		}
		t.CellsNew += g.CellsNew
		t.CellsUpdated += g.CellsUpdated
		t.CellsDeleted += g.CellsDeleted
		t.CellsUnchanged += g.CellsUnchanged
		t.CellsBlocked += g.CellsBlocked
		t.RulesNew += g.RulesNew
		t.RulesDeleted += g.RulesDeleted
		t.RulesUnchanged += g.RulesUnchanged
		t.Inherit += g.Inherit
		t.Extra += g.Extra
		t.Custom += g.Custom
		t.Closed += g.Closed
		t.Warnings += g.Warnings
	}
}
