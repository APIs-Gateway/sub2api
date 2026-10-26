package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR7b：阶段切换的事务与预览（设计 4.3、4.6）。
//
// 一次阶段变更在同一个事务里完成：锁分组配置行（与派生钩子互斥，并发切换在这把锁上串行）、重新评估闸门、
// 消耗预览凭证、改单元格与成本核算行的来源或归档、改阶段、写审计。任何一步失败整个事务回滚，什么都不留下。
// 提交之后失效本实例与其他实例的分组快照，并在本实例同步把快照加载好。
//
//   - legacy 到 shadow、shadow 回 legacy：不涉价，不需要闸门与凭证，只记审计；
//   - shadow 到 v2：先预览（评估闸门、算 price_delta、列出被接受的差异、登记凭证），再带着 approval_id 提交；
//     提交时在事务里按「现在」重新评估闸门，并把「分组配置版本、派生 revision、渠道配置摘要、回放记录」绑进计划指纹，
//     预览之后其中任何一个变了，提交被拒绝；通过之后同一个事务里把分组的 legacy_derived 单元格与成本核算行改为 legacy_frozen；
//   - v2 回拨到 shadow 或 legacy：随时可切，不需要闸门与凭证，但要记审计；同一个事务里把非派生行（legacy_frozen、manual、copied）
//     归档（单元格进 model_group_price_history，成本核算行进审计证据）后删除，再按渠道当前配置重新派生（4.6，REVIEW_OPUS_2 S-10）。
//     否则分组再进 shadow 时 v2 视图里带着旧的冻结行，翻译差异不会是 0。

// StageAuditRecord 一次阶段变更的审计记录。
type StageAuditRecord struct {
	GroupID  int64
	From, To PricingStage
	Kind     string
	// Actor 操作人与是否交互式会话；只能由 PriceWriteActorFromAuthMethod 构造（守卫测试限制）。
	Actor          PriceWriteActor
	ApprovalID     int64
	PriceDelta     PriceDelta
	RevisionBefore int64
	RevisionAfter  int64
	Evidence       []byte
}

// StageAuditEntry 审计记录（读出来的形状）。
type StageAuditEntry struct {
	ID             int64           `json:"id"`
	CreatedAt      time.Time       `json:"created_at"`
	GroupID        int64           `json:"group_id"`
	From           PricingStage    `json:"from"`
	To             PricingStage    `json:"to"`
	Kind           string          `json:"kind"`
	OperatorID     int64           `json:"operator_id"`
	Interactive    bool            `json:"interactive"`
	ApprovalID     int64           `json:"approval_id,omitempty"`
	PriceDelta     PriceDelta      `json:"price_delta"`
	RevisionBefore int64           `json:"config_revision_before"`
	RevisionAfter  int64           `json:"config_revision_after"`
	Evidence       json.RawMessage `json:"evidence"`
}

// StageArchive 回滚时归档并删除的非派生行。
type StageArchive struct {
	Cells     int64
	Rules     int64
	RulesJSON json.RawMessage
}

// PricingStageOps 阶段切换需要的存储原语。凡是改库的方法都只在 PriceWriteStore.WithTx 给出的事务里调用；
// 读方法（LoadGateFacts、LoadSnapshot、RecentUsageModels、ListAudit）既可以用事务也可以用 PriceWriteStore.Reader()。
type PricingStageOps interface {
	// LockConfig 对分组的 group_model_config 行 SELECT ... FOR UPDATE（与派生钩子互斥）。
	// 分组不存在或已软删除返回 ErrGroupNotFound；分组没有配置行返回 ErrPricingStageNotDerived。
	LockConfig(ctx context.Context, tx MatrixExecutor, groupID int64) (*StoredGroupConfig, error)
	// LoadGateFacts 读取闸门的事实：配置行、所属渠道最近保存时间、影子差异样本、最近一次回放。
	// 错误同 LockConfig。
	LoadGateFacts(ctx context.Context, exec MatrixExecutor, groupID int64, now time.Time) (*StageGateFacts, error)
	// LoadSnapshot 读取分组在库里的矩阵现状（配置、单元格、成本核算行）。
	LoadSnapshot(ctx context.Context, exec MatrixExecutor, groupID int64) (GroupStateSnapshot, error)
	// ApplyPlan 执行派生的落库计划（与派生钩子同一份实现）。调用方已持有配置行锁并确认阶段不是 v2。
	ApplyPlan(ctx context.Context, tx MatrixExecutor, plan GroupApplyPlan) error
	// FreezeDerived 把分组的 legacy_derived 单元格与成本核算行改为 legacy_frozen，返回各改了多少行。
	FreezeDerived(ctx context.Context, tx MatrixExecutor, groupID int64) (cells, rules int64, err error)
	// ArchiveNonDerived 把分组的非 legacy_derived 单元格归档进 model_group_price_history 后删除，
	// 并读出、删除非派生的成本核算行。
	ArchiveNonDerived(ctx context.Context, tx MatrixExecutor, groupID, operatorID, approvalID int64) (StageArchive, error)
	// SetStage 改阶段，记录 stage_changed_at / stage_changed_by，revision 加一，返回新的 revision。
	SetStage(ctx context.Context, tx MatrixExecutor, groupID int64, to PricingStage, operatorID int64, now time.Time) (int64, error)
	// InsertAudit 追加一条审计记录，返回它的 id。
	InsertAudit(ctx context.Context, tx MatrixExecutor, rec StageAuditRecord) (int64, error)
	// RecentUsageModels 返回分组自 since 以来用量里出现过的请求模型（小写去空白）与行数。
	RecentUsageModels(ctx context.Context, exec MatrixExecutor, groupID int64, since time.Time) (map[string]int64, error)
	// ListAudit 返回分组最近的审计记录，新的在前。
	ListAudit(ctx context.Context, exec MatrixExecutor, groupID int64, limit int) ([]StageAuditEntry, error)
}

// PricingStageSwitchStore 审批与事务（PriceWriteStore）加阶段原语（PricingStageOps），由同一个仓储实现。
type PricingStageSwitchStore interface {
	PriceWriteStore
	PricingStageOps
}

// PricingStageFingerprinter 渠道配置摘要的读口（回放用的同一份实现）。
type PricingStageFingerprinter interface {
	Fingerprint(ctx context.Context, groupIDs []int64) (*PricingReplayFingerprint, error)
}

// stageDeriver 按渠道当前配置实时派生一个分组。
type stageDeriver interface {
	DeriveGroupCurrent(ctx context.Context, groupID int64) (DerivedGroupState, string, error)
}

// stageSnapshotSync 阶段变更提交之后的快照处理。
type stageSnapshotSync interface {
	InvalidateGroups(groupIDs ...int64)
	// EnsureGroupsLoaded 同步把这些分组的快照加载进本实例的缓存；加载失败返回错误。
	EnsureGroupsLoaded(ctx context.Context, groupIDs ...int64) error
}

// 一次变更的种类（审计表 kind）。
const (
	StageKindAdvance  = "advance"
	StageKindRollback = "rollback"
	stageKindNoop     = "noop"
)

// stageSnapshotWarmTimeout 提交之后在本实例同步加载快照的上限。
const stageSnapshotWarmTimeout = 10 * time.Second

// stageCatalogTimeout 预览里查「目录 draft、retired 且近 7 天有流量」的上限（要扫一个分组 7 天的用量）。
const stageCatalogTimeout = 20 * time.Second

// PricingStageSwitcher 阶段切换的预览与事务。
type PricingStageSwitcher struct {
	store       PriceWriteStore
	ops         PricingStageOps
	derive      stageDeriver
	fingerprint PricingStageFingerprinter
	catalog     runtimeCatalogSource
	sync        stageSnapshotSync
	// compared 返回本实例进程内某个分组的影子比对次数（仅供参考）；可为 nil。
	compared func(groupID int64) int64
	// translationInProcess 返回本实例进程内某个分组 translation 类差异的累计数与最近一次的时间（没有为零值）；可为 nil（不检查）。
	translationInProcess func(groupID int64) (int64, time.Time)
	// observation 影子观察的最短时长（部署配置 pricing.gate_observation_hours）；构造时默认 72 小时。
	observation time.Duration
	// exposure 切到 v2 时的无价与开放范围检查（预览与提交共用）；没接上时 v2 的预览与提交都失败关闭。
	exposure *StageExposureChecker
	now      func() time.Time
}

// SetExposureChecker 接上切到 v2 时的暴露检查。
func (sw *PricingStageSwitcher) SetExposureChecker(c *StageExposureChecker) {
	sw.exposure = c
}

// SetObservationHours 设置影子观察的最短时长（小时）。0 表示不要求观察时长；越界值钳制到 [0, 720]（配置校验已在启动时拦住越界值，这里是兜底）。
func (sw *PricingStageSwitcher) SetObservationHours(hours int) {
	if hours < 0 {
		hours = 0
	}
	if hours > PricingGateObservationMaxHours {
		hours = PricingGateObservationMaxHours
	}
	sw.observation = time.Duration(hours) * time.Hour
}

// SetInProcessTranslationDiffs 接上进程内翻译差异计数：大于 0 时闸门不放行（W6 PR7b-1 审查偏差 1）。
func (sw *PricingStageSwitcher) SetInProcessTranslationDiffs(fn func(groupID int64) (int64, time.Time)) {
	sw.translationInProcess = fn
}

// loadInProcessShadow 把本实例进程内的比对次数与翻译差异计数补进影子证据。
func (sw *PricingStageSwitcher) loadInProcessShadow(facts *StageGateFacts, groupID int64) {
	if sw.compared != nil {
		facts.Shadow.ComparedInProcess = sw.compared(groupID)
	}
	if sw.translationInProcess != nil {
		count, last := sw.translationInProcess(groupID)
		facts.Shadow.TranslationDiffsInProcess = count
		if !last.IsZero() {
			facts.Shadow.LastTranslationDiffAt = &last
		}
	}
}

// NewPricingStageSwitcher 创建阶段切换器。catalog、sync、compared 可以为 nil（目录检查按「查不到」处理、不处理快照、不报样本数）。
func NewPricingStageSwitcher(store PricingStageSwitchStore, derive stageDeriver, fp PricingStageFingerprinter,
	catalog runtimeCatalogSource, sync stageSnapshotSync, compared func(groupID int64) int64) *PricingStageSwitcher {
	return &PricingStageSwitcher{
		store: store, ops: store, derive: derive, fingerprint: fp, catalog: catalog, sync: sync,
		compared: compared, now: time.Now,
		observation: PricingGateObservationDefaultHours * time.Hour,
	}
}

// StagePreviewRequest 预览请求。
type PricingStagePreviewRequest struct {
	GroupID    int64
	To         PricingStage
	OperatorID int64
}

// StageDrift 回滚时「按渠道当前配置重新派生」与「分组现在的矩阵」之间的差异（不含非派生行）。
type StageDrift struct {
	Changed       bool `json:"changed"`
	ConfigChanged bool `json:"config_changed"`
	CellsInserted int  `json:"cells_inserted"`
	CellsUpdated  int  `json:"cells_updated"`
	CellsDeleted  int  `json:"cells_deleted"`
	RulesReplaced int  `json:"rules_replaced"`
}

// StageRollbackPreview 回拨预览：哪些行会被归档删除，重新派生会改多少。
type StageRollbackPreview struct {
	ArchivedCells int        `json:"archived_cells"`
	ArchivedRules int        `json:"archived_rules"`
	Drift         StageDrift `json:"drift"`
}

// PricingStagePreview 预览结果。Gate 只在目标是 v2 时有；ApprovalID 为 0 表示不需要凭证（或闸门没通过，没有登记）。
type PricingStagePreview struct {
	Action       string                `json:"action"`
	Category     string                `json:"category"`
	TouchesPrice bool                  `json:"touches_price"`
	GroupID      int64                 `json:"group_id"`
	From         PricingStage          `json:"from"`
	To           PricingStage          `json:"to"`
	Kind         string                `json:"kind"`
	PriceDelta   PriceDelta            `json:"price_delta"`
	ApprovalID   int64                 `json:"approval_id"`
	PlanHash     string                `json:"plan_hash,omitempty"`
	ExpiresAt    *time.Time            `json:"expires_at,omitempty"`
	Executable   bool                  `json:"executable"`
	Gate         *StageGateReport      `json:"gate,omitempty"`
	Accepted     []AcceptedDifference  `json:"accepted_differences"`
	Rollback     *StageRollbackPreview `json:"rollback,omitempty"`
}

// stageSwitchSummary 审批行的 summary：闸门证据、被接受的差异与价格方向，兼作切换的审计依据。
type stageSwitchSummary struct {
	GroupID    int64                `json:"group_id"`
	OperatorID int64                `json:"operator_id"`
	Plan       StageSwitchPlan      `json:"plan"`
	PriceDelta PriceDelta           `json:"price_delta"`
	Gate       StageGateReport      `json:"gate"`
	Accepted   []AcceptedDifference `json:"accepted_differences"`
}

func (sw *PricingStageSwitcher) validate(groupID int64, to PricingStage, operatorID int64) error {
	if operatorID <= 0 {
		return infraerrors.Forbidden(ReasonPricingStageActor, "an administrator is required")
	}
	if groupID <= 0 {
		return infraerrors.BadRequest("INVALID_PARAMETER", "group id must be a positive integer")
	}
	switch to {
	case PricingStageLegacy, PricingStageShadow, PricingStageV2:
		return nil
	}
	return infraerrors.BadRequest(ReasonPricingStageNotAllowed, "stage must be legacy, shadow or v2")
}

func newStagePreview(groupID int64, from, to PricingStage) *PricingStagePreview {
	kind := StageKindAdvance
	switch {
	case from == to:
		kind = stageKindNoop
	case stageRank(to) < stageRank(from):
		kind = StageKindRollback
	}
	return &PricingStagePreview{
		Action: PricingActionStageSwitch, Category: PricingActionCategoryStage, TouchesPrice: true,
		GroupID: groupID, From: from, To: to, Kind: kind, PriceDelta: PriceDeltaNone, Accepted: []AcceptedDifference{},
	}
}

// Preview 预览一次阶段变更。目标是 v2 时评估闸门；闸门通过才登记预览凭证（approval_id），不通过时凭证为 0、gate.failures 列出原因。
// 回拨预览给出会被归档删除的行数与重新派生的差异。预览不改任何矩阵数据。
func (sw *PricingStageSwitcher) Preview(ctx context.Context, req PricingStagePreviewRequest) (*PricingStagePreview, error) {
	if err := sw.validate(req.GroupID, req.To, req.OperatorID); err != nil {
		return nil, err
	}
	now := sw.now()
	exec := sw.store.Reader()
	facts, err := sw.ops.LoadGateFacts(ctx, exec, req.GroupID, now)
	if err != nil {
		return nil, err
	}
	from := facts.Config.Stage
	out := newStagePreview(req.GroupID, from, req.To)
	switch {
	case out.Kind == stageKindNoop:
		out.Executable = true
		return out, nil
	case req.To != PricingStageV2 && from != PricingStageV2:
		// legacy 与 shadow 之间：不改账单与准入。
		out.Executable = true
		return out, nil
	case from == PricingStageV2:
		return sw.previewRollback(ctx, exec, out)
	}
	return sw.previewV2(ctx, exec, out, facts, now, req.OperatorID)
}

func (sw *PricingStageSwitcher) previewRollback(ctx context.Context, exec MatrixExecutor, out *PricingStagePreview) (*PricingStagePreview, error) {
	derived, _, err := sw.derive.DeriveGroupCurrent(ctx, out.GroupID)
	if err != nil {
		return nil, err
	}
	snap, err := sw.ops.LoadSnapshot(ctx, exec, out.GroupID)
	if err != nil {
		return nil, err
	}
	rb := StageRollbackPreview{Drift: stageDrift(derived, snap)}
	for _, c := range snap.Cells {
		if c.Source != MatrixSourceLegacyDerived {
			rb.ArchivedCells++
		}
	}
	for _, r := range snap.Rules {
		if r.Source != MatrixSourceLegacyDerived {
			rb.ArchivedRules++
		}
	}
	out.Rollback = &rb
	out.Executable = true
	// 回拨回到渠道当前的配置。派生比较忽略来源，只看内容：现有的行（含冻结行与手工编辑）按渠道当前配置重新派生之后
	// 一行都不变，账单就不会变（none）；有任何一行会被改、删或新增，方向证明不了，一律 unknown。
	if rb.Drift.Changed {
		out.PriceDelta = PriceDeltaUnknown
	}
	return out, nil
}

// stageDrift 把分组现有的行都当成派生行（冻结只改了来源），计算按渠道当前配置重新派生会写什么。
func stageDrift(derived DerivedGroupState, snap GroupStateSnapshot) StageDrift {
	view := GroupStateSnapshot{Cells: make([]StoredMatrixCell, len(snap.Cells)), Rules: make([]StoredMatrixCostRule, len(snap.Rules))}
	if snap.Config != nil {
		cfg := *snap.Config
		cfg.PricingStage = PricingStageShadow
		view.Config = &cfg
	}
	for i, c := range snap.Cells {
		c.Source = MatrixSourceLegacyDerived
		view.Cells[i] = c
	}
	for i, r := range snap.Rules {
		r.Source = MatrixSourceLegacyDerived
		view.Rules[i] = r
	}
	plan := PlanGroupApply(derived, view)
	return stageDriftOf(plan)
}

func stageDriftOf(plan GroupApplyPlan) StageDrift {
	return StageDrift{
		Changed:       !plan.Empty(),
		ConfigChanged: plan.ConfigWrite != nil,
		CellsInserted: len(plan.CellInserts),
		CellsUpdated:  len(plan.CellUpdates),
		CellsDeleted:  len(plan.CellDeletes),
		RulesReplaced: len(plan.RuleDeletes) + len(plan.RuleInserts),
	}
}

// stageEvidence 取「现在」的派生 revision 与渠道配置摘要。任何一个取不到都返回错误。
func (sw *PricingStageSwitcher) stageEvidence(ctx context.Context, groupID int64) (derived DerivedGroupState, platform, channelHash string, err error) {
	derived, platform, err = sw.derive.DeriveGroupCurrent(ctx, groupID)
	if err != nil {
		return derived, "", "", err
	}
	if sw.fingerprint == nil {
		return derived, platform, "", fmt.Errorf("no channel configuration fingerprinter")
	}
	fp, err := sw.fingerprint.Fingerprint(ctx, []int64{groupID})
	if err != nil {
		return derived, platform, "", err
	}
	return derived, platform, fp.ChannelConfigHash, nil
}

func (sw *PricingStageSwitcher) previewV2(ctx context.Context, exec MatrixExecutor, out *PricingStagePreview, facts *StageGateFacts, now time.Time, operatorID int64) (*PricingStagePreview, error) {
	derived, platform, channelHash, evErr := sw.stageEvidence(ctx, out.GroupID)
	if evErr != nil {
		slog.Warn("pricing stage gate: current evidence unavailable", "group_id", out.GroupID, "error", evErr)
	}
	sw.loadInProcessShadow(facts, out.GroupID)
	report := EvaluateStageGate(StageGateInput{
		Facts: facts, Now: now, ObservationRequired: sw.observation, CurrentDeriveRevision: derived.Revision, CurrentChannelConfigHash: channelHash, EvidenceErr: evErr,
	})
	if evErr == nil { // 派生取不到时闸门已经以 derive_failed 拒绝，目标态无从算起
		failure, err := sw.previewExposure(ctx, exec, out.GroupID, derived)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			report.Failures = append(report.Failures, *failure)
			report.Passed = false
		}
	}
	delta, accepted := StageSwitchAccepted(facts.Replay, facts.Shadow, sw.catalogAccepted(ctx, exec, out.GroupID, platform, now))
	out.Gate, out.Accepted, out.PriceDelta = &report, accepted, delta
	if !report.Passed {
		return out, nil
	}

	plan := StageSwitchPlan{
		GroupID: out.GroupID, From: out.From, To: out.To, ConfigRevision: facts.Config.Revision,
		DeriveRevision: derived.Revision, ChannelConfigHash: channelHash, ReplayID: facts.Replay.ID,
	}
	out.PlanHash = StageSwitchPlanHash(plan)
	approval := PriceWriteApproval{
		Kind:         PriceWriteKindStageSwitch,
		PlanHash:     out.PlanHash,
		TouchesPrice: true,
		Delta:        delta,
		GroupIDs:     []int64{out.GroupID},
		Summary: []byte(matrixCanonicalJSON(stageSwitchSummary{
			GroupID: out.GroupID, OperatorID: operatorID, Plan: plan, PriceDelta: delta, Gate: report, Accepted: accepted,
		})),
		PreviewedBy: operatorID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(PriceWriteApprovalTTL),
	}
	id, err := sw.store.InsertApproval(ctx, approval)
	if err != nil {
		return nil, err
	}
	if _, perr := sw.store.PurgeStale(ctx, now.Add(-priceWriteStaleAfter)); perr != nil {
		slog.Warn("purge stale price write previews failed", "error", perr)
	}
	out.ApprovalID = id
	expires := approval.ExpiresAt
	out.ExpiresAt = &expires
	out.Executable = true
	return out, nil
}

// previewExposure 对「按渠道当前配置派生并追平之后」的目标态跑暴露检查（只读，不写库）。有问题返回一条闸门失败项。
func (sw *PricingStageSwitcher) previewExposure(ctx context.Context, exec MatrixExecutor, groupID int64, derived DerivedGroupState) (*StageGateFailure, error) {
	if sw.exposure == nil {
		return nil, infraerrors.InternalServer(ReasonExposureGuardMissing, "exposure validation is not configured")
	}
	snap, err := sw.ops.LoadSnapshot(ctx, exec, groupID)
	if err != nil {
		return nil, err
	}
	target := applyPlanToSnapshot(snap, PlanGroupApply(derived, snap))
	issues, err := sw.exposure.CheckTarget(ctx, groupID, target)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, nil
	}
	f := stageExposureFailure(issues)
	return &f, nil
}

// catalogAccepted 目录里 draft、retired 且近 7 天有流量的模型：切到 v2 的那一刻起这些请求会被挡（设计 4.3，S-11）。
// 查不出来时不能证明没有，留一条 unknown 的记录，价格方向随之是 unknown。
func (sw *PricingStageSwitcher) catalogAccepted(ctx context.Context, exec MatrixExecutor, groupID int64, platform string, now time.Time) []AcceptedDifference {
	unknown := []AcceptedDifference{{Source: "catalog", Reason: "catalog_check_unavailable", PriceDelta: PriceDeltaUnknown}}
	if sw.catalog == nil {
		return unknown
	}
	ctx, cancel := context.WithTimeout(ctx, stageCatalogTimeout)
	defer cancel()
	entries, err := sw.catalog.List(ctx, ModelCatalogFilter{Platform: strings.TrimSpace(platform)})
	if err != nil {
		slog.Warn("pricing stage preview: model catalog unavailable", "group_id", groupID, "error", err)
		return unknown
	}
	models, err := sw.ops.RecentUsageModels(ctx, exec, groupID, now.Add(-PricingGateExpectedTrafficWindow))
	if err != nil {
		slog.Warn("pricing stage preview: recent usage unavailable", "group_id", groupID, "error", err)
		return unknown
	}
	return catalogAcceptedFrom(entries, models)
}

// catalogAcceptedFrom 是 catalogAccepted 的纯函数部分：models 是近 7 天用量里的请求模型与行数。
func catalogAcceptedFrom(entries []ModelCatalogEntry, models map[string]int64) []AcceptedDifference {
	out := []AcceptedDifference{}
	for model, rows := range models {
		entry := ResolveCatalogEntry(entries, model)
		if entry == nil {
			continue
		}
		switch entry.Status {
		case ModelCatalogDraft:
			out = append(out, AcceptedDifference{Source: "catalog", Reason: AcceptedReasonCatalogDraft, Model: model, Count: rows, PriceDelta: PriceDeltaUnknown})
		case ModelCatalogRetired:
			out = append(out, AcceptedDifference{Source: "catalog", Reason: AcceptedReasonCatalogRetired, Model: model, Count: rows, PriceDelta: PriceDeltaUnknown})
		}
	}
	return out
}

// Commit 提交一次阶段变更（事务见文件头）。
func (sw *PricingStageSwitcher) Commit(ctx context.Context, req PricingStageSwitchRequest) (*PricingStageSwitchResult, error) {
	if err := sw.validate(req.GroupID, req.To, req.OperatorID); err != nil {
		return nil, err
	}
	if !req.Confirm {
		return nil, infraerrors.BadRequest(ReasonPricingStageConfirm, "confirm the stage switch explicitly")
	}
	var result *PricingStageSwitchResult
	err := sw.store.WithTx(ctx, func(ctx context.Context, tx MatrixTx) error {
		cfg, err := sw.ops.LockConfig(ctx, tx, req.GroupID)
		if err != nil {
			return err
		}
		now := sw.now()
		switch {
		case cfg.PricingStage == req.To:
			result = &PricingStageSwitchResult{
				Action: PricingActionStageSwitch, Category: PricingActionCategoryStage, TouchesPrice: true, PriceDelta: PriceDeltaNone,
				Kind:               stageKindNoop,
				PricingStageChange: PricingStageChange{GroupID: req.GroupID, From: cfg.PricingStage, To: req.To, Revision: cfg.Revision, ChangedAt: now},
			}
			return nil
		case req.To == PricingStageV2:
			result, err = sw.commitV2(ctx, tx, req, cfg, now)
		case cfg.PricingStage == PricingStageV2:
			result, err = sw.commitRollback(ctx, tx, req, cfg, now)
		default:
			result, err = sw.commitLateral(ctx, tx, req, cfg, now)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if result.Kind != stageKindNoop {
		sw.afterCommit(ctx, req.GroupID, result)
	}
	return result, nil
}

// afterCommit 失效快照（本实例同步、其他实例经通知），并在本实例同步把快照加载好：
// 切到 v2 之后第一批请求不能因为快照没就绪而退回 legacy。
func (sw *PricingStageSwitcher) afterCommit(ctx context.Context, groupID int64, result *PricingStageSwitchResult) {
	result.SnapshotReady = true
	if sw.sync == nil {
		return
	}
	sw.sync.InvalidateGroups(groupID)
	warmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stageSnapshotWarmTimeout)
	defer cancel()
	if err := sw.sync.EnsureGroupsLoaded(warmCtx, groupID); err != nil {
		result.SnapshotReady = false
		slog.Warn("pricing stage switch committed but the group snapshot could not be loaded on this instance",
			"group_id", groupID, "to", string(result.To), "error", err)
	}
}

func stageResult(req PricingStageSwitchRequest, from PricingStage, kind string, delta PriceDelta, revision int64, auditID int64, now time.Time) *PricingStageSwitchResult {
	slog.Info(PricingActionStageSwitch,
		"group_id", req.GroupID, "from", string(from), "to", string(req.To), "kind", kind, "changed", true,
		"operator_id", req.OperatorID, "revision", revision, "touches_price", true, "price_delta", string(delta), "audit_id", auditID)
	return &PricingStageSwitchResult{
		Action: PricingActionStageSwitch, Category: PricingActionCategoryStage, TouchesPrice: true, PriceDelta: delta,
		Kind: kind, AuditID: auditID, ApprovalID: req.ApprovalID,
		PricingStageChange: PricingStageChange{GroupID: req.GroupID, From: from, To: req.To, Changed: true, Revision: revision, ChangedAt: now},
	}
}

// commitLateral legacy 与 shadow 之间：只改阶段、记审计。
func (sw *PricingStageSwitcher) commitLateral(ctx context.Context, tx MatrixTx, req PricingStageSwitchRequest, cfg *StoredGroupConfig, now time.Time) (*PricingStageSwitchResult, error) {
	kind := StageKindAdvance
	if stageRank(req.To) < stageRank(cfg.PricingStage) {
		kind = StageKindRollback
	}
	rev, err := sw.ops.SetStage(ctx, tx, req.GroupID, req.To, req.OperatorID, now)
	if err != nil {
		return nil, err
	}
	auditID, err := sw.ops.InsertAudit(ctx, tx, StageAuditRecord{
		GroupID: req.GroupID, From: cfg.PricingStage, To: req.To, Kind: kind, Actor: stageActor(req), PriceDelta: PriceDeltaNone, RevisionBefore: cfg.Revision, RevisionAfter: rev,
		Evidence: []byte(`{}`),
	})
	if err != nil {
		return nil, err
	}
	return stageResult(req, cfg.PricingStage, kind, PriceDeltaNone, rev, auditID, now), nil
}

// commitV2 shadow 到 v2。
func (sw *PricingStageSwitcher) commitV2(ctx context.Context, tx MatrixTx, req PricingStageSwitchRequest, cfg *StoredGroupConfig, now time.Time) (*PricingStageSwitchResult, error) {
	if cfg.PricingStage != PricingStageShadow {
		return nil, StageGateError(req.GroupID, StageGateReport{Failures: []StageGateFailure{{
			Code: ReasonPricingGateNotInShadow, Message: "the group must be in the shadow stage before it can move to v2"}}})
	}
	if req.ApprovalID <= 0 {
		return nil, infraerrors.Forbidden(ReasonPriceWriteApproval, "moving a group to v2 needs a previewed approval")
	}

	// 配置行锁已经拿到：下面「现在」的派生 revision 与读到的事实，在提交之前不会被派生钩子改写。
	derived, _, channelHash, evErr := sw.stageEvidence(ctx, req.GroupID)
	facts, err := sw.ops.LoadGateFacts(ctx, tx, req.GroupID, now)
	if err != nil {
		return nil, err
	}
	sw.loadInProcessShadow(facts, req.GroupID)
	report := EvaluateStageGate(StageGateInput{
		Facts: facts, Now: now, ObservationRequired: sw.observation, CurrentDeriveRevision: derived.Revision, CurrentChannelConfigHash: channelHash, EvidenceErr: evErr,
	})
	if !report.Passed {
		return nil, StageGateError(req.GroupID, report)
	}

	plan := StageSwitchPlan{
		GroupID: req.GroupID, From: cfg.PricingStage, To: PricingStageV2, ConfigRevision: cfg.Revision,
		DeriveRevision: derived.Revision, ChannelConfigHash: channelHash, ReplayID: facts.Replay.ID,
	}

	// 把库里的派生行追平到刚刚验证过的那份派生结果，再整体冻结，然后改阶段。
	snap, err := sw.ops.LoadSnapshot(ctx, tx, req.GroupID)
	if err != nil {
		return nil, err
	}
	apply := PlanGroupApply(derived, snap)
	if !apply.Skipped && !apply.Empty() {
		if err := sw.ops.ApplyPlan(ctx, tx, apply); err != nil {
			return nil, err
		}
	}
	cells, rules, err := sw.ops.FreezeDerived(ctx, tx, req.GroupID)
	if err != nil {
		return nil, err
	}
	rev, err := sw.ops.SetStage(ctx, tx, req.GroupID, PricingStageV2, req.OperatorID, now)
	if err != nil {
		return nil, err
	}

	// 无价、0 元与开放范围检查（B1）：必须在同一个事务里、改完阶段之后读事务自己的状态，
	// 不能用连接池读（那里分组仍是 shadow，什么也查不出）；有问题整体回滚，下面的凭证还没有消耗。
	if sw.exposure == nil {
		return nil, infraerrors.InternalServer(ReasonExposureGuardMissing, "exposure validation is not configured")
	}
	frozen, err := sw.ops.LoadSnapshot(ctx, tx, req.GroupID)
	if err != nil {
		return nil, err
	}
	issues, err := sw.exposure.CheckInTx(ctx, tx, req.GroupID, frozen)
	if err != nil {
		return nil, err
	}
	if len(issues) > 0 {
		return nil, StageExposureError(req.GroupID, issues)
	}

	approval, err := sw.store.ConsumeApproval(ctx, tx, req.ApprovalID, StageSwitchPlanHash(plan), PriceWriteKindStageSwitch, req.OperatorID, now)
	if err != nil {
		return nil, err
	}
	if err := checkApprovalForWrite(approval, true, stageActor(req)); err != nil {
		return nil, err
	}

	_, accepted := StageSwitchAccepted(facts.Replay, facts.Shadow, nil)
	evidence := map[string]any{
		"gate": report, "plan": plan, "plan_hash": StageSwitchPlanHash(plan),
		"accepted_differences": accepted, "approved_price_delta": approval.Delta,
		"frozen_cells": cells, "frozen_rules": rules, "applied_before_freeze": stageDriftOf(apply),
	}
	auditID, err := sw.ops.InsertAudit(ctx, tx, StageAuditRecord{
		GroupID: req.GroupID, From: cfg.PricingStage, To: PricingStageV2, Kind: StageKindAdvance, Actor: stageActor(req), ApprovalID: req.ApprovalID, PriceDelta: approval.Delta,
		RevisionBefore: cfg.Revision, RevisionAfter: rev, Evidence: []byte(matrixCanonicalJSON(evidence)),
	})
	if err != nil {
		return nil, err
	}
	res := stageResult(req, cfg.PricingStage, StageKindAdvance, approval.Delta, rev, auditID, now)
	res.Gate = &report
	return res, nil
}

// commitRollback v2 回拨到 shadow 或 legacy：归档并删除非派生行，改阶段，再按渠道当前配置重新派生。
func (sw *PricingStageSwitcher) commitRollback(ctx context.Context, tx MatrixTx, req PricingStageSwitchRequest, cfg *StoredGroupConfig, now time.Time) (*PricingStageSwitchResult, error) {
	derived, _, err := sw.derive.DeriveGroupCurrent(ctx, req.GroupID)
	if err != nil {
		return nil, err
	}
	before, err := sw.ops.LoadSnapshot(ctx, tx, req.GroupID)
	if err != nil {
		return nil, err
	}
	drift := stageDrift(derived, before)

	archive, err := sw.ops.ArchiveNonDerived(ctx, tx, req.GroupID, req.OperatorID, req.ApprovalID)
	if err != nil {
		return nil, err
	}
	// 阶段先改成目标：派生计划对 v2 行整体跳过，只有改完阶段，下面的重新派生才会落库。
	rev, err := sw.ops.SetStage(ctx, tx, req.GroupID, req.To, req.OperatorID, now)
	if err != nil {
		return nil, err
	}
	after, err := sw.ops.LoadSnapshot(ctx, tx, req.GroupID)
	if err != nil {
		return nil, err
	}
	apply := PlanGroupApply(derived, after)
	if !apply.Skipped && !apply.Empty() {
		if err := sw.ops.ApplyPlan(ctx, tx, apply); err != nil {
			return nil, err
		}
	}

	// 回拨后单元格与成本核算行回到「渠道派生」的状态；价格方向：重新派生没有改任何一行才是 none，其余 unknown。
	delta := PriceDeltaNone
	if drift.Changed {
		delta = PriceDeltaUnknown
	}
	archivedRules := archive.RulesJSON
	if len(archivedRules) == 0 {
		archivedRules = json.RawMessage(`[]`)
	}
	evidence := map[string]any{
		"archived_cells": archive.Cells, "archived_rules": archive.Rules, "archived_rules_detail": archivedRules,
		"drift_before_rollback": drift, "rederived": stageDriftOf(apply),
		// v2 期间对分组配置行的修改会在重新派生时被覆盖，回拨前的配置整份留在证据里以便恢复。
		"config_before_rollback": before.Config,
	}
	auditID, err := sw.ops.InsertAudit(ctx, tx, StageAuditRecord{
		GroupID: req.GroupID, From: cfg.PricingStage, To: req.To, Kind: StageKindRollback, Actor: stageActor(req), PriceDelta: delta, RevisionBefore: cfg.Revision, RevisionAfter: rev,
		Evidence: []byte(matrixCanonicalJSON(evidence)),
	})
	if err != nil {
		return nil, err
	}
	res := stageResult(req, cfg.PricingStage, StageKindRollback, delta, rev, auditID, now)
	res.Archived = &StageArchiveSummary{Cells: archive.Cells, Rules: archive.Rules}
	return res, nil
}

// stageActor 由请求里记下的鉴权方式得出操作人；Interactive 不取自请求体。
func stageActor(req PricingStageSwitchRequest) PriceWriteActor {
	return PriceWriteActorFromAuthMethod(req.OperatorID, req.AuthMethod)
}

// StageArchiveSummary 回拨时归档删除的行数。
type StageArchiveSummary struct {
	Cells int64 `json:"cells"`
	Rules int64 `json:"rules"`
}

// Audit 返回分组最近的阶段变更审计，新的在前。
func (sw *PricingStageSwitcher) Audit(ctx context.Context, groupID int64, limit int) ([]StageAuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return sw.ops.ListAudit(ctx, sw.store.Reader(), groupID, limit)
}
