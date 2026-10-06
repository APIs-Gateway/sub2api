package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// W6 PR9b：候选快照的差异预览、批准与审批记录（设计 6.2、6.3）。
//
// 流程：拉取候选（PricingService.FetchCandidateSnapshot）→ 预览（对生效快照的差异，管理员可搁置个别模型，
// 得到 plan_hash）→ 批准（带回 plan_hash 作二次确认；在一个事务里写入 merged 快照、切换 active、落审批记录，
// 并对所有白名单分组做保存时校验）。
//
// 批准动作藏在 SnapshotApprover 接口后面：W5 落地前用内置实现（管理员二次确认 + 审批记录），
// W5 落地后同一入口改为创建 change-set，所以本 PR 对 W5 只有软依赖。

const (
	// pricingSnapshotUsageDays 是差异页找「实际变价的名字」时回看的天数。
	pricingSnapshotUsageDays = 7
	// pricingSnapshotEffectiveChangesLimit 封顶返回的实际变价名字数。
	pricingSnapshotEffectiveChangesLimit = 500
	pricingSnapshotHistoryLimit          = 50
)

// SnapshotApprover 执行一次批准。W5 之前由 builtinSnapshotApprover 实现。
type SnapshotApprover interface {
	Approve(ctx context.Context, plan *SnapshotApprovalPlan, approverID int64) (*PricingSnapshotMeta, error)
}

// SnapshotExposureChecker 是批准事务里的保存时校验：假设 merged 价格数据已经生效，
// 检查所有白名单分组的 open 单元格是否仍然「有价且非 0 元」（W6 设计 5.2 的同一条不变量，即 CheckGroups）。
// exec 是批准所在事务的连接（预览时是只读连接，结果只作提示）；有违规时返回错误，批准整体回滚。
type SnapshotExposureChecker interface {
	CheckSnapshotApproval(ctx context.Context, exec MatrixExecutor, merged map[string]*LiteLLMModelPricing) error
}

// ErrPricingSnapshotExposureUnavailable 表示没有配置保存时校验，批准按失败关闭处理。
var ErrPricingSnapshotExposureUnavailable = errors.New("pricing snapshot approval requires the save-time exposure check, which is not configured")

// SnapshotDiffStats 是差异页顶部的统计。
type SnapshotDiffStats struct {
	Added     int `json:"added"`
	Removed   int `json:"removed"`
	Changed   int `json:"changed"`
	Unchanged int `json:"unchanged"`
	Approved  int `json:"approved"`
	Held      int `json:"held"`
}

// EffectivePriceChange 是「实际变价的名字」：近 7 天出现过的计费模型名，用批准前后两份数据逐个报价，价格不同的就列出。
// 它同时覆盖变体、模糊、系列匹配与内置兜底价带来的间接变化。价格单位 USD / 百万 token。
type EffectivePriceChange struct {
	Model            string  `json:"model"`
	OldMissing       bool    `json:"old_missing"`
	NewMissing       bool    `json:"new_missing"`
	OldInputPerMTok  float64 `json:"old_input_per_mtok"`
	NewInputPerMTok  float64 `json:"new_input_per_mtok"`
	OldOutputPerMTok float64 `json:"old_output_per_mtok"`
	NewOutputPerMTok float64 `json:"new_output_per_mtok"`
}

// SnapshotApprovalPlan 是预览的结果，也是批准的输入。
type SnapshotApprovalPlan struct {
	BaseSnapshot     PricingSnapshotMeta        `json:"base_snapshot"`
	Candidate        PricingSnapshotMeta        `json:"candidate"`
	Stats            SnapshotDiffStats          `json:"stats"`
	Entries          []PricingSnapshotDiffEntry `json:"entries"`
	HeldModels       []string                   `json:"held_models"`
	MergedSHA256     string                     `json:"merged_sha256"`
	MergedModelCount int                        `json:"merged_model_count"`
	// PlanHash 绑定基线、候选、合成结果与搁置名单；批准必须带回同一个值（二次确认）。
	PlanHash string `json:"plan_hash"`
	// EffectiveChanges 是实际变价的名字；EffectiveChangesError 非空表示这一项没算出来（不阻止预览）。
	EffectiveChanges      []EffectivePriceChange `json:"effective_changes"`
	EffectiveChangesError string                 `json:"effective_changes_error,omitempty"`
	// ExposureError 非空表示批准会让某个白名单分组出现无价单元格（批准时会被拒绝）。
	//
	// Deprecated: 只为兼容旧前端保留一个版本，请改读 Violations（结构化）。
	ExposureError string `json:"exposure_error,omitempty"`
	// Violations 是 ExposureError 的结构化形式：每一项一个违例。没有违例时为空数组。
	// 校验失败但不是违例（例如读库出错）时只有一项，code 为错误原因。
	Violations []PricingPreviewViolation `json:"violations"`
	// ViolationsTotal 是违例总数；Violations 最多列出 maxExposureViolationsListed 项，总数更大时表示列表被截断。
	ViolationsTotal int `json:"violations_total"`

	MergedPayload []byte `json:"-"`
	mergedData    map[string]*LiteLLMModelPricing
}

// SnapshotOverview 是快照页的总览。
type SnapshotOverview struct {
	Mode   string               `json:"mode"`
	Active *PricingSnapshotMeta `json:"active,omitempty"`
	// PendingCandidates 是内容与生效快照不同的候选（红点）。
	PendingCandidates []PricingSnapshotMeta `json:"pending_candidates"`
	// History 是最近的快照（含生效、被替换、被拒绝），按拉取时间倒序。
	History []PricingSnapshotMeta `json:"history"`
}

// PricingSnapshotAdminService 编排快照页的各个动作。
type PricingSnapshotAdminService struct {
	pricing  *PricingService
	store    PricingSnapshotRepository
	approver SnapshotApprover
	checker  SnapshotExposureChecker
	readExec MatrixExecutor
}

// NewPricingSnapshotAdminService 创建服务。checker 为 nil 时批准会失败关闭（预览不受影响）；
// readDB 用于预览时的只读校验，可为 nil（预览不报 exposure_error）。
func NewPricingSnapshotAdminService(pricing *PricingService, store PricingSnapshotRepository, checker SnapshotExposureChecker, readDB *sql.DB) *PricingSnapshotAdminService {
	a := &PricingSnapshotAdminService{
		pricing:  pricing,
		store:    store,
		approver: &builtinSnapshotApprover{store: store, checker: checker},
		checker:  checker,
	}
	if readDB != nil {
		a.readExec = readDB
	}
	return a
}

// Overview 返回快照页总览。
func (a *PricingSnapshotAdminService) Overview(ctx context.Context) (*SnapshotOverview, error) {
	out := &SnapshotOverview{Mode: PricingSnapshotModeAuto, PendingCandidates: []PricingSnapshotMeta{}, History: []PricingSnapshotMeta{}}
	if a.pricing.isPinned() {
		out.Mode = PricingSnapshotModePinned
	}
	active, err := a.store.GetActiveMeta(ctx)
	switch {
	case err == nil:
		out.Active = active
	case errors.Is(err, ErrPricingSnapshotNotFound):
	default:
		return nil, err
	}
	candidates, err := a.store.List(ctx, []string{PricingSnapshotStatusCandidate}, pricingSnapshotHistoryLimit)
	if err != nil {
		return nil, err
	}
	var activeData map[string]*LiteLLMModelPricing
	activeLoaded := false
	for _, c := range candidates {
		if active != nil && strings.EqualFold(c.ContentSHA256, active.ContentSHA256) {
			continue
		}
		// 与生效快照没有任何差异的候选（批准之后合成快照的字节哈希对不上远程原文）不算待批准。
		// 读取或解析失败时保守地留在列表里。
		if active != nil {
			if !activeLoaded {
				activeLoaded = true
				activeData, _ = a.baseData(ctx, active)
			}
			if activeData != nil {
				if _, candData, lerr := a.loadSnapshot(ctx, &c); lerr == nil {
					if entries, _ := computePricingDiff(activeData, candData); len(entries) == 0 {
						continue
					}
				}
			}
		}
		out.PendingCandidates = append(out.PendingCandidates, c)
	}
	out.History, err = a.store.List(ctx, nil, pricingSnapshotHistoryLimit)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Pin 是启动引导：固定当前价格并把模式切到 pinned。
func (a *PricingSnapshotAdminService) Pin(ctx context.Context, adminID int64) (*PricingSnapshotPinResult, error) {
	return a.pricing.PinCurrentPricing(ctx, adminID)
}

// FetchCandidate 拉取远程价格为候选。
func (a *PricingSnapshotAdminService) FetchCandidate(ctx context.Context, adminID int64) (*PricingSnapshotFetchResult, error) {
	return a.pricing.FetchCandidateSnapshot(ctx, optionalAdminID(adminID))
}

// Reject 拒绝一个候选。
func (a *PricingSnapshotAdminService) Reject(ctx context.Context, candidateID int64) error {
	return a.store.RejectCandidate(ctx, candidateID)
}

// Preview 计算候选对生效快照的差异与合成结果。holdModels 是被搁置（保持旧值）的模型。
func (a *PricingSnapshotAdminService) Preview(ctx context.Context, candidateID int64, holdModels []string) (*SnapshotApprovalPlan, error) {
	plan, err := a.buildPlan(ctx, candidateID, holdModels)
	if err != nil {
		return nil, err
	}
	a.annotatePlan(ctx, plan)
	return plan, nil
}

// Approve 批准：按当前状态重新算一次计划，plan_hash 必须与预览时一致，然后交给 SnapshotApprover。
func (a *PricingSnapshotAdminService) Approve(ctx context.Context, candidateID int64, holdModels []string, planHash string, approverID int64) (*PricingSnapshotMeta, error) {
	if !a.pricing.isPinned() {
		return nil, ErrPricingNotPinned
	}
	plan, err := a.buildPlan(ctx, candidateID, holdModels)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(plan.PlanHash), []byte(strings.TrimSpace(planHash))) != 1 {
		return nil, ErrPricingSnapshotPlanMismatch
	}
	if plan.Stats.Approved == 0 {
		return nil, ErrPricingSnapshotNothingToApprove
	}
	meta, err := a.approver.Approve(ctx, plan, approverID)
	if err != nil {
		return nil, err
	}
	a.pricing.afterSnapshotApplied()
	return meta, nil
}

// buildPlan 读取生效快照与候选，算差异、合成 merged 数据与计划哈希。
func (a *PricingSnapshotAdminService) buildPlan(ctx context.Context, candidateID int64, holdModels []string) (*SnapshotApprovalPlan, error) {
	active, err := a.store.GetActiveMeta(ctx)
	if err != nil {
		if errors.Is(err, ErrPricingSnapshotNotFound) {
			return nil, ErrPricingNotPinned
		}
		return nil, err
	}
	candidate, err := a.store.GetMeta(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	if candidate.Status != PricingSnapshotStatusCandidate {
		return nil, ErrPricingSnapshotNotCandidate
	}
	basePayload, baseData, err := a.loadSnapshot(ctx, active)
	if err != nil {
		return nil, err
	}
	candPayload, candData, err := a.loadSnapshot(ctx, candidate)
	if err != nil {
		return nil, err
	}

	entries, unchanged := computePricingDiff(baseData, candData)
	held, err := applyHolds(entries, holdModels)
	if err != nil {
		return nil, err
	}
	stats := SnapshotDiffStats{Unchanged: unchanged, Held: len(held)}
	for _, e := range entries {
		switch e.ChangeType {
		case PricingDiffAdded:
			stats.Added++
		case PricingDiffRemoved:
			stats.Removed++
		default:
			stats.Changed++
		}
		if e.Decision == PricingDiffDecisionApprove {
			stats.Approved++
		}
	}

	merged, err := mergePricingPayload(basePayload, candPayload, entries)
	if err != nil {
		return nil, err
	}
	mergedData, err := a.pricing.parsePricingData(merged)
	if err != nil {
		return nil, fmt.Errorf("parse merged snapshot: %w", err)
	}
	sum := sha256.Sum256(merged)
	mergedSHA := hex.EncodeToString(sum[:])

	return &SnapshotApprovalPlan{
		BaseSnapshot:     *active,
		Candidate:        *candidate,
		Stats:            stats,
		Entries:          entries,
		HeldModels:       held,
		MergedSHA256:     mergedSHA,
		MergedModelCount: len(mergedData),
		PlanHash:         snapshotPlanHash(active, candidate, mergedSHA, held),
		MergedPayload:    merged,
		mergedData:       mergedData,
		EffectiveChanges: []EffectivePriceChange{},
	}, nil
}

// loadSnapshot 读取快照 payload，核对哈希并解析。
func (a *PricingSnapshotAdminService) loadSnapshot(ctx context.Context, meta *PricingSnapshotMeta) ([]byte, map[string]*LiteLLMModelPricing, error) {
	payload, err := a.store.GetPayload(ctx, meta.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load snapshot %d payload: %w", meta.ID, err)
	}
	sum := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), meta.ContentSHA256) {
		return nil, nil, fmt.Errorf("snapshot %d payload does not match its content_sha256", meta.ID)
	}
	data, err := a.pricing.parsePricingData(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("parse snapshot %d: %w", meta.ID, err)
	}
	return payload, data, nil
}

// applyHolds 把搁置名单里的模型决定改成 hold，返回去重排序后的名单；名单里有差异里不存在的模型时报错。
func applyHolds(entries []PricingSnapshotDiffEntry, holdModels []string) ([]string, error) {
	hold := make(map[string]struct{}, len(holdModels))
	for _, m := range holdModels {
		if m = strings.TrimSpace(m); m != "" {
			hold[m] = struct{}{}
		}
	}
	var held []string
	for i := range entries {
		if _, ok := hold[entries[i].ModelKey]; ok {
			entries[i].Decision = PricingDiffDecisionHold
			held = append(held, entries[i].ModelKey)
			delete(hold, entries[i].ModelKey)
		}
	}
	if len(hold) > 0 {
		unknown := make([]string, 0, len(hold))
		for m := range hold {
			unknown = append(unknown, m)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("%w: %s", ErrPricingSnapshotUnknownHold, strings.Join(unknown, ", "))
	}
	sort.Strings(held)
	if held == nil {
		held = []string{}
	}
	return held, nil
}

func snapshotPlanHash(base, candidate *PricingSnapshotMeta, mergedSHA string, held []string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "pricing.snapshot_approve/v1\nbase=%d:%s\ncandidate=%d:%s\nmerged=%s\nheld=%s\n",
		base.ID, strings.ToLower(base.ContentSHA256), candidate.ID, strings.ToLower(candidate.ContentSHA256), mergedSHA, strings.Join(held, "\x00"))
	return hex.EncodeToString(h.Sum(nil))
}

// annotatePlan 给预览补上两项只作提示的信息：实际变价的名字、批准后的无价风险。任何一项失败都不阻止预览。
func (a *PricingSnapshotAdminService) annotatePlan(ctx context.Context, plan *SnapshotApprovalPlan) {
	baseData, err := a.baseData(ctx, &plan.BaseSnapshot)
	if err != nil {
		plan.EffectiveChangesError = err.Error()
	} else if changes, err := a.effectiveChanges(ctx, baseData, plan.mergedData); err != nil {
		plan.EffectiveChangesError = err.Error()
	} else {
		plan.EffectiveChanges = changes
	}
	plan.Violations = []PricingPreviewViolation{}
	if a.checker != nil && a.readExec != nil {
		if err := a.checker.CheckSnapshotApproval(ctx, a.readExec, plan.mergedData); err != nil {
			plan.ExposureError = exposureErrorText(err)
			plan.Violations, plan.ViolationsTotal = exposureViolationsFromError(err)
		}
	}
}

// PricingPreviewViolation 是预览里一条结构化的违例（取代 exposure_error 字符串）。
type PricingPreviewViolation struct {
	// Code 违例原因：unpriced、wildcard_unverifiable、zero_price；不是违例而是校验本身失败时为错误原因码。
	Code    string `json:"code"`
	Message string `json:"message"`
	Model   string `json:"model,omitempty"`
	GroupID int64  `json:"group_id,omitempty"`
}

// exposureViolationsFromError 把保存时校验错误还原成结构化违例，并返回违例总数。
// 错误 metadata.violations 的格式是「分组id:模型:原因」以分号连接（模型名里可以有冒号，所以取第一个和最后一个冒号切分）。
// 无法还原时返回一项，code 取错误原因码，让调用方至少能看到失败。
func exposureViolationsFromError(err error) ([]PricingPreviewViolation, int) {
	ae := infraerrors.FromError(err)
	if ae != nil && ae.Metadata["violations"] != "" {
		out := make([]PricingPreviewViolation, 0)
		for _, part := range strings.Split(ae.Metadata["violations"], ";") {
			first := strings.Index(part, ":")
			last := strings.LastIndex(part, ":")
			if first <= 0 || last <= first {
				continue
			}
			gid, perr := strconv.ParseInt(part[:first], 10, 64)
			if perr != nil {
				continue
			}
			reason := part[last+1:]
			model := part[first+1 : last]
			out = append(out, PricingPreviewViolation{
				Code: reason, Message: exposureViolationMessage(reason), Model: model, GroupID: gid,
			})
		}
		if len(out) > 0 {
			total := len(out)
			if n, cerr := strconv.Atoi(ae.Metadata["count"]); cerr == nil && n > total {
				total = n
			}
			return out, total
		}
	}
	code, msg := UnknownExposureCheckCode, err.Error()
	if ae != nil && ae.Reason != "" {
		code = ae.Reason
	}
	return []PricingPreviewViolation{{Code: code, Message: msg}}, 1
}

// UnknownExposureCheckCode 是预览里校验本身失败（不是违例）且错误没有原因码时的 code。
const UnknownExposureCheckCode = "EXPOSURE_CHECK_FAILED"

func exposureViolationMessage(reason string) string {
	switch ExposureViolationReason(reason) {
	case ExposureUnpriced:
		return "the model has no price"
	case ExposureWildcardUnverifiable:
		return "an open wildcard cell cannot be verified to be priced"
	case ExposureZeroPrice:
		return "the model price is zero and it is not on the known-free list"
	}
	return "the model cannot be exposed"
}

// exposureErrorText 把保存时校验的违规项（metadata.violations）带进预览文本，否则管理员只看到一句笼统的话。
func exposureErrorText(err error) string {
	if ae := infraerrors.FromError(err); ae != nil && ae.Metadata["violations"] != "" {
		return fmt.Sprintf("%s (%s of them: %s)", ae.Message, ae.Metadata["count"], ae.Metadata["violations"])
	}
	return err.Error()
}

func (a *PricingSnapshotAdminService) baseData(ctx context.Context, meta *PricingSnapshotMeta) (map[string]*LiteLLMModelPricing, error) {
	_, data, err := a.loadSnapshot(ctx, meta)
	return data, err
}

// effectiveChanges 用批准前后两份数据，对近 7 天出现过的计费模型名逐个报价，返回价格有变化的名字。
func (a *PricingSnapshotAdminService) effectiveChanges(ctx context.Context, before, after map[string]*LiteLLMModelPricing) ([]EffectivePriceChange, error) {
	names, err := a.store.RecentBillingModels(ctx, pricingSnapshotUsageDays)
	if err != nil {
		return nil, fmt.Errorf("read recent billing models: %w", err)
	}
	sort.Strings(names)
	oldBilling := newSnapshotBilling(a.pricing.cfg, before)
	newBilling := newSnapshotBilling(a.pricing.cfg, after)
	out := []EffectivePriceChange{}
	for _, name := range names {
		op, oerr := oldBilling.GetModelPricing(name)
		np, nerr := newBilling.GetModelPricing(name)
		if oerr == nil && nerr == nil && reflect.DeepEqual(op, np) {
			continue
		}
		if oerr != nil && nerr != nil {
			continue
		}
		change := EffectivePriceChange{Model: name, OldMissing: oerr != nil, NewMissing: nerr != nil}
		if op != nil {
			change.OldInputPerMTok, change.OldOutputPerMTok = op.InputPricePerToken*1e6, op.OutputPricePerToken*1e6
		}
		if np != nil {
			change.NewInputPerMTok, change.NewOutputPerMTok = np.InputPricePerToken*1e6, np.OutputPricePerToken*1e6
		}
		out = append(out, change)
		if len(out) >= pricingSnapshotEffectiveChangesLimit {
			break
		}
	}
	return out, nil
}

// newSnapshotBilling 用一份固定的价格数据搭一个只读的计费服务（含内置兜底价），只用来逐名报价。
func newSnapshotBilling(cfg *config.Config, data map[string]*LiteLLMModelPricing) *BillingService {
	p := NewPricingService(cfg, nil)
	p.mu.Lock()
	p.setPricingDataLocked(data)
	p.mu.Unlock()
	return NewBillingService(cfg, p)
}

// builtinSnapshotApprover 是 W5 之前的内置批准：管理员二次确认（plan_hash）加审批记录。
// 审批记录 = merged 快照行（批准人、批准时间、来源候选、基线）+ 候选的逐模型差异行（含 approve / hold 决定）
// + merged 快照的 note（差异统计与被搁置的模型）。
type builtinSnapshotApprover struct {
	store   PricingSnapshotRepository
	checker SnapshotExposureChecker
}

// snapshotApprovalNote 是写进 merged 快照 note 的审批摘要。
type snapshotApprovalNote struct {
	CandidateID int64             `json:"candidate_id"`
	BaseID      int64             `json:"base_id"`
	Stats       SnapshotDiffStats `json:"stats"`
	Held        []string          `json:"held_models"`
	PlanHash    string            `json:"plan_hash"`
}

func (b *builtinSnapshotApprover) Approve(ctx context.Context, plan *SnapshotApprovalPlan, approverID int64) (*PricingSnapshotMeta, error) {
	if b.checker == nil {
		return nil, ErrPricingSnapshotExposureUnavailable
	}
	note, err := json.Marshal(snapshotApprovalNote{
		CandidateID: plan.Candidate.ID, BaseID: plan.BaseSnapshot.ID, Stats: plan.Stats, Held: plan.HeldModels, PlanHash: plan.PlanHash,
	})
	if err != nil {
		return nil, fmt.Errorf("encode approval note: %w", err)
	}
	baseID, candidateID := plan.BaseSnapshot.ID, plan.Candidate.ID
	meta, err := b.store.ApplyMerged(ctx, ApplyMergedSnapshot{
		New: NewPricingSnapshot{
			Label:               "merged-" + time.Now().UTC().Format("2006-01-02") + "-" + plan.MergedSHA256[:8],
			Source:              PricingSnapshotSourceMerged,
			SourceURL:           plan.Candidate.SourceURL,
			ContentSHA256:       plan.MergedSHA256,
			ModelCount:          plan.MergedModelCount,
			Payload:             plan.MergedPayload,
			ParentSnapshotID:    &baseID,
			CandidateSnapshotID: &candidateID,
			FetchedBy:           plan.Candidate.FetchedBy,
			ApprovedBy:          optionalAdminID(approverID),
			Note:                string(note),
		},
		ExpectedActiveID:  plan.BaseSnapshot.ID,
		ExpectedActiveSHA: plan.BaseSnapshot.ContentSHA256,
		ConsumeCandidate:  len(plan.HeldModels) == 0,
		Diffs:             diffRecords(plan.Entries),
		Check: func(ctx context.Context, exec MatrixExecutor) error {
			return b.checker.CheckSnapshotApproval(ctx, exec, plan.mergedData)
		},
	})
	if err != nil {
		return nil, err
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Snapshot %d approved by %d: %d approved, %d held", meta.ID, approverID, plan.Stats.Approved, plan.Stats.Held)
	return meta, nil
}
