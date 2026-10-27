package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// PricingDerivationService 渠道保存之后的派生钩子，以及只读的派生结果查看（设计文档 4.2、S-6）。
//
// 本 PR 没有任何计费、调度、准入路径读取派生结果；唯一的写入方是 AfterChannelSaved。
// 钩子是 best-effort：任何错误、超时、panic 都只记日志、计数，不影响渠道保存。

// PricingMatrixRepository 矩阵表（group_model_config、model_group_prices、cost_accounting_rules）的数据访问。
type PricingMatrixRepository interface {
	// GetGroupMeta 返回分组的平台与软删除标记；不存在的分组不出现在结果里。
	GetGroupMeta(ctx context.Context, groupIDs []int64) (map[int64]DeriveGroup, error)
	// ListDerivedRuleGroupIDs 返回来源渠道为 channelID 的 legacy_derived 成本核算行所属的分组。
	ListDerivedRuleGroupIDs(ctx context.Context, channelID int64) ([]int64, error)
	// ListDerivedRuleChannels 返回库里所有 legacy_derived 成本核算行的来源渠道 id，及各自所属的分组
	// （批量派生命令用它找出「有派生行、但渠道已停用或不存在」的分组）。
	ListDerivedRuleChannels(ctx context.Context) (map[int64][]int64, error)
	// LoadGroupSnapshots 读取各分组在库里的现状（不加锁，供只读查看用）。
	LoadGroupSnapshots(ctx context.Context, groupIDs []int64) (map[int64]GroupStateSnapshot, error)
	// LoadGroupSummaries 一次读取各分组的阶段、配置 revision 与成本核算规则摘要（不加锁，只读）。
	// groupIDs 为空表示全部未删除分组；结果按 id 升序，最多 limit 行；不存在或已删除的分组不出现。
	LoadGroupSummaries(ctx context.Context, groupIDs []int64, limit int) ([]GroupPricingSummary, error)
	// ApplyPlans 在一个事务里依次完成：先取派生落库的事务级咨询锁（跨进程串行化钩子与批量派生命令），再按 group_id 升序对 group_model_config 行 SELECT ... FOR UPDATE
	// （与阶段切换互斥，4.2 混合阶段规则第 3 条）、读取各分组现状、调用 plan 生成计划、执行计划。
	// plan 里不能做 I/O。
	ApplyPlans(ctx context.Context, groupIDs []int64, plan func(snaps map[int64]GroupStateSnapshot) ([]GroupApplyPlan, error)) error
}

// PricingDeriveStats 钩子的进程内计数。
type PricingDeriveStats struct {
	Runs     int64 `json:"runs"`
	Failures int64 `json:"failures"`
	Panics   int64 `json:"panics"`
}

// PricingRefreshGroupResult 一个分组的刷新结果。
type PricingRefreshGroupResult struct {
	GroupID       int64            `json:"group_id"`
	ChannelID     int64            `json:"channel_id"`
	Skipped       bool             `json:"skipped"`
	SkipReason    string           `json:"skip_reason,omitempty"`
	Revision      string           `json:"revision"`
	ConfigWritten bool             `json:"config_written"`
	CellsInserted int              `json:"cells_inserted"`
	CellsUpdated  int              `json:"cells_updated"`
	CellsDeleted  int              `json:"cells_deleted"`
	RulesReplaced int              `json:"rules_replaced"`
	Notes         []DerivationNote `json:"notes"`
}

// PricingRefreshReport 一次钩子刷新的结果。
type PricingRefreshReport struct {
	ChannelID int64                       `json:"channel_id"`
	Groups    []PricingRefreshGroupResult `json:"groups"`
}

// PricingDerivationService 见文件头注释。
type PricingDerivationService struct {
	repo     PricingMatrixRepository
	channels ChannelRepository
	facts    OfficialPriceFactSource
	timeout  time.Duration

	// invalidator 在派生结果落库之后失效分组快照缓存（见 SetSnapshotInvalidator）；nil 表示没有读取方。
	invalidator MatrixSnapshotInvalidator

	// mu 串行化同一进程里的刷新，避免两次渠道保存的派生结果以相反的顺序落库。
	mu     sync.Mutex
	runs   atomic.Int64
	fails  atomic.Int64
	panics atomic.Int64
}

// DefaultPricingDeriveTimeout 钩子的超时时间。
const DefaultPricingDeriveTimeout = 15 * time.Second

// NewPricingDerivationService 创建派生服务。facts 可以为 nil（所有价格全空的 token 条目按「事实未知」处理，即按敏感派生）。
func NewPricingDerivationService(repo PricingMatrixRepository, channels ChannelRepository, facts OfficialPriceFactSource) *PricingDerivationService {
	return &PricingDerivationService{
		repo:     repo,
		channels: channels,
		facts:    facts,
		timeout:  DefaultPricingDeriveTimeout,
	}
}

// SetSnapshotInvalidator 设置派生写入落库之后要失效的分组快照缓存（matrixPolicy，由后续 PR 接线）。
// 渠道保存时 ChannelService 发出的缓存通知早于这次派生写入，单靠它会让快照缓存住「写入前」的数据，
// 所以写入之后要由这里再失效一次。必须在开始处理请求之前设置；默认没有，什么也不做。
func (s *PricingDerivationService) SetSnapshotInvalidator(inv MatrixSnapshotInvalidator) {
	s.invalidator = inv
}

// Stats 返回钩子的进程内计数。
func (s *PricingDerivationService) Stats() PricingDeriveStats {
	return PricingDeriveStats{Runs: s.runs.Load(), Failures: s.fails.Load(), Panics: s.panics.Load()}
}

// AfterChannelSaved 实现 ChannelSaveHook：渠道创建、更新、删除之后刷新受影响分组的派生结果。
// previousGroupIDs 是保存前渠道关联的分组，用来清理已经离开渠道的分组。
// 失败只记日志、计数，绝不向调用方传播。
func (s *PricingDerivationService) AfterChannelSaved(ctx context.Context, channelID int64, previousGroupIDs []int64) {
	s.runs.Add(1)
	defer func() {
		if r := recover(); r != nil {
			s.panics.Add(1)
			s.fails.Add(1)
			slog.Error("pricing derive hook panicked", "channel_id", channelID, "panic", r)
		}
	}()

	// 请求取消不应该让派生半途而废：脱离请求的取消信号，单独计时。
	hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
	defer cancel()

	report, err := s.RefreshChannel(hookCtx, channelID, previousGroupIDs)
	if err != nil {
		s.fails.Add(1)
		slog.Warn("pricing derive hook failed", "channel_id", channelID, "error", err)
		return
	}
	for _, g := range report.Groups {
		slog.Info("pricing derive hook applied",
			"channel_id", channelID, "group_id", g.GroupID, "skipped", g.Skipped, "skip_reason", g.SkipReason,
			"config_written", g.ConfigWritten, "cells_inserted", g.CellsInserted, "cells_updated", g.CellsUpdated,
			"cells_deleted", g.CellsDeleted, "rules_replaced", g.RulesReplaced, "revision", g.Revision)
	}
}

// RefreshChannel 重新派生渠道当前关联的分组、保存前关联的分组、以及仍带有该渠道派生成本核算行的分组，
// 并落库。每个分组都按它当前所属的渠道派生（离开渠道的分组按「无渠道」处理）。
func (s *PricingDerivationService) RefreshChannel(ctx context.Context, channelID int64, previousGroupIDs []int64) (*PricingRefreshReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	channel, err := s.loadChannel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	ruleGroups, err := s.repo.ListDerivedRuleGroupIDs(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("list derived rule groups: %w", err)
	}
	var current []int64
	if channel != nil {
		current = channel.GroupIDs
	}
	ids := uniqueSortedIDs(current, previousGroupIDs, ruleGroups)
	return s.refreshLocked(ctx, channelID, channel, ids)
}

// RefreshGroups 只重新派生并落库指定的分组（批量派生命令的 --group）。
// 每个分组按它当前所属的渠道派生，规则与 RefreshChannel 完全一致（同一个 refreshLocked）。
// 任一分组记录不存在返回 ErrGroupNotFound（钩子对已删除分组按「无渠道」处理，这里是显式点名，所以要报错）。
func (s *PricingDerivationService) RefreshGroups(ctx context.Context, groupIDs []int64) (*PricingRefreshReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := uniqueSortedIDs(groupIDs)
	meta, err := s.repo.GetGroupMeta(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get group meta: %w", err)
	}
	for _, id := range ids {
		if _, ok := meta[id]; !ok {
			return nil, ErrGroupNotFound
		}
	}
	return s.refreshLocked(ctx, 0, nil, ids)
}

// refreshLocked 派生 ids 里的分组并在一个事务里落库。调用方必须持有 s.mu。
// saved 是刚保存的渠道（可能为 nil，此时每个分组按它当前所属的渠道派生）。
func (s *PricingDerivationService) refreshLocked(ctx context.Context, channelID int64, channel *Channel, ids []int64) (*PricingRefreshReport, error) {
	report := &PricingRefreshReport{ChannelID: channelID, Groups: []PricingRefreshGroupResult{}}
	if len(ids) == 0 {
		return report, nil
	}

	derived, err := s.deriveGroups(ctx, ids, channel)
	if err != nil {
		return nil, err
	}

	var plans []GroupApplyPlan
	err = s.repo.ApplyPlans(ctx, ids, func(snaps map[int64]GroupStateSnapshot) ([]GroupApplyPlan, error) {
		plans = make([]GroupApplyPlan, 0, len(derived))
		for _, d := range derived {
			plans = append(plans, PlanGroupApply(d, snaps[d.GroupID]))
		}
		return plans, nil
	})
	if err != nil {
		return nil, fmt.Errorf("apply derive plans: %w", err)
	}
	if s.invalidator != nil {
		var changed []int64
		for _, p := range plans {
			if !p.Skipped && !p.Empty() {
				changed = append(changed, p.GroupID)
			}
		}
		if len(changed) > 0 {
			s.invalidator.InvalidateGroups(changed...)
		}
	}

	for i, p := range plans {
		report.Groups = append(report.Groups, PricingRefreshGroupResult{
			GroupID:       p.GroupID,
			ChannelID:     derived[i].ChannelID,
			Skipped:       p.Skipped,
			SkipReason:    p.SkipReason,
			Revision:      derived[i].Revision,
			ConfigWritten: p.ConfigWrite != nil,
			CellsInserted: len(p.CellInserts),
			CellsUpdated:  len(p.CellUpdates),
			CellsDeleted:  len(p.CellDeletes),
			RulesReplaced: len(p.RuleDeletes) + len(p.RuleInserts),
			Notes:         append(append([]DerivationNote{}, derived[i].Notes...), p.Notes...),
		})
	}
	return report, nil
}

// loadChannel 读取渠道；渠道不存在（已删除）返回 nil。
func (s *PricingDerivationService) loadChannel(ctx context.Context, channelID int64) (*Channel, error) {
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, ErrChannelNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get channel: %w", err)
	}
	return ch, nil
}

// deriveGroups 对每个分组按它当前所属的渠道派生。saved 是刚保存的渠道（可能为 nil）。
func (s *PricingDerivationService) deriveGroups(ctx context.Context, ids []int64, saved *Channel) ([]DerivedGroupState, error) {
	meta, err := s.repo.GetGroupMeta(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get group meta: %w", err)
	}

	owners := make(map[int64]*Channel, len(ids))
	loaded := make(map[int64]*Channel)
	if saved != nil {
		loaded[saved.ID] = saved
	}
	for _, id := range ids {
		if saved != nil && matrixChannelHasGroup(saved, id) {
			owners[id] = saved
			continue
		}
		ownerID, err := s.channels.GetChannelIDByGroupID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get channel of group %d: %w", id, err)
		}
		if ownerID == 0 {
			continue
		}
		if _, ok := loaded[ownerID]; !ok {
			other, err := s.loadChannel(ctx, ownerID)
			if err != nil {
				return nil, err
			}
			loaded[ownerID] = other
		}
		owners[id] = loaded[ownerID]
	}

	groups := make([]DeriveGroup, 0, len(ids))
	for _, id := range ids {
		g, ok := meta[id]
		if !ok {
			// 分组记录不存在：与软删除同样处理。
			g = DeriveGroup{ID: id, Deleted: true}
		}
		g.ID = id
		groups = append(groups, g)
	}

	facts := s.collectFacts(groups, owners)
	out := make([]DerivedGroupState, 0, len(groups))
	for _, g := range groups {
		out = append(out, DeriveGroupState(owners[g.ID], g, facts))
	}
	return out, nil
}

// collectFacts 为所有分组需要的模型名查询官方价事实。
func (s *PricingDerivationService) collectFacts(groups []DeriveGroup, owners map[int64]*Channel) OfficialPriceFacts {
	seen := make(map[string]struct{})
	var models []string
	for _, g := range groups {
		for _, m := range CollectDeriveFactModels(owners[g.ID], g.Platform) {
			key := officialPriceFactKey(m)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			models = append(models, m)
		}
	}
	return NewOfficialPriceFacts(s.facts, models)
}

// MatrixPlanSummary 计划的摘要（只读查看用）。
type MatrixPlanSummary struct {
	Skipped       bool   `json:"skipped"`
	SkipReason    string `json:"skip_reason,omitempty"`
	ConfigWrite   bool   `json:"config_write"`
	CellsInsert   int    `json:"cells_insert"`
	CellsUpdate   int    `json:"cells_update"`
	CellsDelete   int    `json:"cells_delete"`
	RulesReplaced int    `json:"rules_replaced"`
}

// GroupDeriveView 一个分组的派生结果与库里现状的对照（只读，不写任何东西）。
type GroupDeriveView struct {
	GroupID  int64  `json:"group_id"`
	Platform string `json:"platform"`
	Deleted  bool   `json:"deleted"`
	// Derived 按渠道当前配置实时派生的结果。
	Derived DerivedGroupState `json:"derived"`
	// Stored 库里现状。
	StoredConfig *StoredGroupConfig     `json:"stored_config"`
	StoredCells  []StoredMatrixCell     `json:"stored_cells"`
	StoredRules  []StoredMatrixCostRule `json:"stored_cost_rules"`
	// InSync 为 true 表示再跑一次钩子不会写任何东西（v2 分组也算同步：钩子会整体跳过）。
	InSync bool              `json:"in_sync"`
	Plan   MatrixPlanSummary `json:"plan"`
}

// loadGroupAndOwner 读取分组元信息与它当前所属的渠道（没有渠道为 nil）。分组不存在返回 ErrGroupNotFound。
func (s *PricingDerivationService) loadGroupAndOwner(ctx context.Context, groupID int64) (DeriveGroup, *Channel, error) {
	meta, err := s.repo.GetGroupMeta(ctx, []int64{groupID})
	if err != nil {
		return DeriveGroup{}, nil, fmt.Errorf("get group meta: %w", err)
	}
	group, ok := meta[groupID]
	if !ok {
		return DeriveGroup{}, nil, ErrGroupNotFound
	}
	group.ID = groupID

	var owner *Channel
	ownerID, err := s.channels.GetChannelIDByGroupID(ctx, groupID)
	if err != nil {
		return DeriveGroup{}, nil, fmt.Errorf("get channel of group: %w", err)
	}
	if ownerID != 0 {
		if owner, err = s.loadChannel(ctx, ownerID); err != nil {
			return DeriveGroup{}, nil, err
		}
	}
	return group, owner, nil
}

// DeriveGroupCurrent 按渠道当前配置实时派生一个分组，返回派生结果与分组平台（只读，不写任何东西）。
// 阶段切换在事务里拿到分组配置行的锁之后调用它，用派生出的 revision 与回放绑定的 revision 比对。
func (s *PricingDerivationService) DeriveGroupCurrent(ctx context.Context, groupID int64) (DerivedGroupState, string, error) {
	group, owner, err := s.loadGroupAndOwner(ctx, groupID)
	if err != nil {
		return DerivedGroupState{}, "", err
	}
	facts := s.collectFacts([]DeriveGroup{group}, map[int64]*Channel{group.ID: owner})
	return DeriveGroupState(owner, group, facts), group.Platform, nil
}

// ViewGroup 实时派生一个分组并与库里现状对照。分组不存在返回 ErrGroupNotFound。
func (s *PricingDerivationService) ViewGroup(ctx context.Context, groupID int64) (*GroupDeriveView, error) {
	group, owner, err := s.loadGroupAndOwner(ctx, groupID)
	if err != nil {
		return nil, err
	}
	return s.buildView(ctx, group, owner)
}

// ViewChannel 实时派生渠道里的每个分组。渠道不存在返回 ErrChannelNotFound。
func (s *PricingDerivationService) ViewChannel(ctx context.Context, channelID int64) ([]GroupDeriveView, error) {
	ch, err := s.loadChannel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, ErrChannelNotFound
	}
	ids := uniqueSortedIDs(ch.GroupIDs)
	out := make([]GroupDeriveView, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	meta, err := s.repo.GetGroupMeta(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get group meta: %w", err)
	}
	for _, id := range ids {
		g, ok := meta[id]
		if !ok {
			g = DeriveGroup{Deleted: true}
		}
		g.ID = id
		view, err := s.buildView(ctx, g, ch)
		if err != nil {
			return nil, err
		}
		out = append(out, *view)
	}
	return out, nil
}

func (s *PricingDerivationService) buildView(ctx context.Context, group DeriveGroup, owner *Channel) (*GroupDeriveView, error) {
	view, _, err := s.buildViewPlan(ctx, group, owner)
	return view, err
}

// buildViewPlan 同 buildView，同时返回完整的落库计划（批量派生命令要统计单元格、规则的明细）。
func (s *PricingDerivationService) buildViewPlan(ctx context.Context, group DeriveGroup, owner *Channel) (*GroupDeriveView, GroupApplyPlan, error) {
	facts := s.collectFacts([]DeriveGroup{group}, map[int64]*Channel{group.ID: owner})
	derived := DeriveGroupState(owner, group, facts)

	snaps, err := s.repo.LoadGroupSnapshots(ctx, []int64{group.ID})
	if err != nil {
		return nil, GroupApplyPlan{}, fmt.Errorf("load group snapshot: %w", err)
	}
	snap := snaps[group.ID]
	plan := PlanGroupApply(derived, snap)
	return &GroupDeriveView{
		GroupID:      group.ID,
		Platform:     group.Platform,
		Deleted:      group.Deleted,
		Derived:      derived,
		StoredConfig: snap.Config,
		StoredCells:  nonNilCells(snap.Cells),
		StoredRules:  nonNilRules(snap.Rules),
		InSync:       plan.Skipped || plan.Empty(),
		Plan: MatrixPlanSummary{
			Skipped:       plan.Skipped,
			SkipReason:    plan.SkipReason,
			ConfigWrite:   plan.ConfigWrite != nil,
			CellsInsert:   len(plan.CellInserts),
			CellsUpdate:   len(plan.CellUpdates),
			CellsDelete:   len(plan.CellDeletes),
			RulesReplaced: len(plan.RuleDeletes) + len(plan.RuleInserts),
		},
	}, plan, nil
}

func nonNilCells(c []StoredMatrixCell) []StoredMatrixCell {
	if c == nil {
		return []StoredMatrixCell{}
	}
	return c
}

func nonNilRules(r []StoredMatrixCostRule) []StoredMatrixCostRule {
	if r == nil {
		return []StoredMatrixCostRule{}
	}
	return r
}

// uniqueSortedIDs 合并多组 id，去重并升序。
func uniqueSortedIDs(sets ...[]int64) []int64 {
	seen := make(map[int64]struct{})
	var out []int64
	for _, set := range sets {
		for _, id := range set {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
