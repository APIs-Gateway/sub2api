package service

import (
	"context"
	"sort"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2b-2：开放时预检（设计 5.2「开放时」一行；REVIEW_OPUS_2 给 4b-2b 的要求 ④⑤⑥）。
//
// 保存时校验（ExposureGuard）是最后一道防线；开放时预检是操作粒度的、带用户可读预览的检查，与它重叠是有意的：
// 开放时给出「为什么不行」，保存时只返回违规清单。预检只对 pricing_stage = 'v2' 的分组有意义（其余分组的矩阵不生效）。
//
//   - 白名单分组：阻止（Blocking）。包含三类问题：无价或 0 元的 open 单元格；open 的通配符单元格（放行整个前缀，
//     没有办法验证，wildcard_unverifiable，失败关闭）；精确映射命中 open 单元格时，映射目标模型必须官方有价且 token 价非零
//     （目标没价时网关会回落到请求模型，但目标在官方价里存在、却全是 0 时会直接按 0 计费）。
//   - 开放分组：只警告（Warnings），不阻止，要求管理员确认。
//
// W5 的 group.publish 对外公开预检从本 PR 起对 v2 的白名单分组阻止，不依赖运行时开关 billing_unpriced_policy
// （它只管调度阶段的拦截）：W5 落地后调用 OpenPrechecker.PublishCheck。

// 开放时预检的错误原因。
const (
	// ReasonOpenPrecheckBlocked 开放时预检阻止了这次写入：白名单分组里会出现问题；metadata.issues 列出前几项。
	ReasonOpenPrecheckBlocked = "OPEN_PRECHECK_BLOCKED"
	// ReasonGroupPublishBlocked group.publish 被阻止：v2 白名单分组里有预检问题。
	ReasonGroupPublishBlocked = "GROUP_PUBLISH_BLOCKED"
)

// 预检问题的原因（除 ExposureViolationReason 里的三个以外）。
const (
	// OpenIssueMappingTargetUnpriced 精确映射命中 open 单元格，映射目标在官方价里没有价。
	OpenIssueMappingTargetUnpriced = "mapping_target_unpriced"
	// OpenIssueMappingTargetZeroPrice 精确映射命中 open 单元格，映射目标的官方 token 价全是 0。
	OpenIssueMappingTargetZeroPrice = "mapping_target_zero_price"
)

// maxOpenPrecheckIssuesListed 错误 metadata 里最多列出的问题数。
const maxOpenPrecheckIssuesListed = 20

// OpenPrecheckIssue 预检发现的一个问题。
type OpenPrecheckIssue struct {
	GroupID  int64  `json:"group_id"`
	ModelKey string `json:"model_key"`
	Reason   string `json:"reason"`
	// Target 映射目标（只有映射目标类问题有）。
	Target string `json:"target,omitempty"`
}

// OpenPrecheckReport 一个分组的预检结果。
type OpenPrecheckReport struct {
	GroupID    int64            `json:"group_id"`
	Stage      PricingStage     `json:"stage"`
	AccessMode MatrixAccessMode `json:"access_mode"`
	// Applicable 为 false 表示分组不是 v2（或没有配置行）：矩阵对它不生效，不需要预检。
	Applicable bool `json:"applicable"`
	// Blocking 白名单分组里会出现的问题：必须先修好。
	Blocking []OpenPrecheckIssue `json:"blocking"`
	// Warnings 开放分组里的问题：要求管理员确认。
	Warnings []OpenPrecheckIssue `json:"warnings"`
}

// OpenPrecheckSource 预检读分组现状的数据源（*PricingMatrixRepository 满足它）。
type OpenPrecheckSource interface {
	LoadGroupSnapshots(ctx context.Context, groupIDs []int64) (map[int64]GroupStateSnapshot, error)
}

// OpenPrechecker 开放时预检。
type OpenPrechecker struct {
	source    OpenPrecheckSource
	validator *ExposureValidator
	prices    OfficialPriceStateSource
}

// NewOpenPrechecker 创建预检器。validator 与保存时校验共用（官方价、已知免费名单），prices 用来查映射目标的官方价。
func NewOpenPrechecker(source OpenPrecheckSource, validator *ExposureValidator, prices OfficialPriceStateSource) *OpenPrechecker {
	return &OpenPrechecker{source: source, validator: validator, prices: prices}
}

func (p *OpenPrechecker) ready() error {
	if p == nil || p.source == nil || p.validator == nil {
		return infraerrors.InternalServer(ReasonExposureGuardMissing, "open-time precheck is not configured")
	}
	return nil
}

// PrecheckGroups 对这些分组做预检；overlay 是假想的目标态（零值就是现状）。结果按分组 id 排序。
func (p *OpenPrechecker) PrecheckGroups(ctx context.Context, groupIDs []int64, overlay CellOverlay) ([]OpenPrecheckReport, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	ids := dedupeSortedIDs(groupIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	snaps, err := p.source.LoadGroupSnapshots(ctx, ids)
	if err != nil {
		return nil, err
	}
	free := p.validator.knownFree(ctx)
	out := make([]OpenPrecheckReport, 0, len(ids))
	for _, id := range ids {
		out = append(out, p.evaluate(ctx, id, overlay.applyTo(id, snaps[id]), free))
	}
	return out, nil
}

func dedupeSortedIDs(in []int64) []int64 {
	seen := make(map[int64]struct{}, len(in))
	out := make([]int64, 0, len(in))
	for _, id := range in {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (p *OpenPrechecker) evaluate(ctx context.Context, groupID int64, snap GroupStateSnapshot, free []BillingKnownFreeEntry) OpenPrecheckReport {
	if snap.Config == nil || snap.Config.PricingStage != PricingStageV2 {
		return OpenPrecheckReport{GroupID: groupID, Blocking: []OpenPrecheckIssue{}, Warnings: []OpenPrecheckIssue{}}
	}
	return p.evaluateAsV2(ctx, groupID, snap, free)
}

// EvaluateAsV2 按「分组已经是 v2」评估这份快照，忽略快照里的阶段（阶段切换用：目标态的快照里分组还是 shadow，
// 或者在事务里刚冻结、读到的是切换后的状态）。只读，不碰数据库；已知免费名单由调用方的 settings 读取。
func (p *OpenPrechecker) EvaluateAsV2(ctx context.Context, groupID int64, snap GroupStateSnapshot) (OpenPrecheckReport, error) {
	if p == nil || p.validator == nil {
		return OpenPrecheckReport{}, infraerrors.InternalServer(ReasonExposureGuardMissing, "open-time precheck is not configured")
	}
	return p.evaluateAsV2(ctx, groupID, snap, p.validator.knownFree(ctx)), nil
}

// evaluateAsV2 是 evaluate 去掉阶段判断之后的部分：配置行为空（没有准入模式可言）仍然是「不适用」。
func (p *OpenPrechecker) evaluateAsV2(ctx context.Context, groupID int64, snap GroupStateSnapshot, free []BillingKnownFreeEntry) OpenPrecheckReport {
	rep := OpenPrecheckReport{GroupID: groupID, Blocking: []OpenPrecheckIssue{}, Warnings: []OpenPrecheckIssue{}}
	if snap.Config == nil {
		return rep
	}
	rep.Applicable = true
	rep.Stage = PricingStageV2
	rep.AccessMode = snap.Config.AccessMode
	allow := snap.Config.AccessMode == MatrixAccessAllowlist

	var cells []ExposureCell
	openExact := make(map[string]struct{})
	for _, c := range snap.Cells {
		if !c.Open || c.EffectiveFrom != nil || c.EffectiveTo != nil {
			continue
		}
		cells = append(cells, ExposureCell{GroupID: groupID, Cell: c.MatrixCell})
		if !c.IsPattern {
			openExact[normalizeChannelPricingModelName(c.ModelKey)] = struct{}{}
		}
	}

	var issues []OpenPrecheckIssue
	for _, v := range p.validator.Check(ctx, cells) {
		if v.Reason == ExposureWildcardUnverifiable && !allow {
			continue // 开放分组里的通配符单元格很正常，只有白名单分组才无法接受
		}
		issues = append(issues, OpenPrecheckIssue{GroupID: v.GroupID, ModelKey: v.ModelKey, Reason: string(v.Reason)})
	}
	issues = append(issues, p.mappingTargetIssues(groupID, snap.Config.ModelMapping, openExact, free)...)

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].ModelKey != issues[j].ModelKey {
			return issues[i].ModelKey < issues[j].ModelKey
		}
		return issues[i].Reason < issues[j].Reason
	})
	if allow {
		rep.Blocking = append(rep.Blocking, issues...)
	} else {
		rep.Warnings = append(rep.Warnings, issues...)
	}
	return rep
}

// mappingTargetIssues 精确映射命中 open 单元格时，映射目标必须官方有价且 token 价非零（已知免费名单里的放行）。
// 通配符来源的映射不检查（它命中的请求模型数不封顶，且通配符单元格在白名单分组里本来就是违规）。
func (p *OpenPrechecker) mappingTargetIssues(groupID int64, mapping []MatrixMappingEntry, openExact map[string]struct{}, free []BillingKnownFreeEntry) []OpenPrecheckIssue {
	var out []OpenPrecheckIssue
	for _, m := range mapping {
		src, dst := strings.TrimSpace(m.Src), strings.TrimSpace(m.Dst)
		if src == "" || dst == "" || strings.HasSuffix(src, "*") {
			continue
		}
		if _, hit := openExact[normalizeChannelPricingModelName(src)]; !hit {
			continue
		}
		if billingKnownFreeMatches(free, groupID, dst) {
			continue
		}
		var st OfficialPriceState
		if p.prices != nil {
			st = p.prices.LookupOfficialPriceState(dst)
		}
		switch {
		case !st.Known:
			out = append(out, OpenPrecheckIssue{GroupID: groupID, ModelKey: src, Reason: OpenIssueMappingTargetUnpriced, Target: dst})
		case !st.TokenNonZero:
			out = append(out, OpenPrecheckIssue{GroupID: groupID, ModelKey: src, Reason: OpenIssueMappingTargetZeroPrice, Target: dst})
		}
	}
	return out
}

// PrecheckPlanned 对规划好的单元格写入做预检：涉及 open 写入的分组，按写入之后的目标态检查。
func (p *OpenPrechecker) PrecheckPlanned(ctx context.Context, planned []PlannedCellWrite) ([]OpenPrecheckReport, error) {
	var ids []int64
	for _, w := range planned {
		if (w.Action == CellWriteCreate || w.Action == CellWriteUpdate) && w.After != nil && w.After.Open {
			ids = append(ids, w.Op.GroupID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return p.PrecheckGroups(ctx, ids, OverlayFromPlanned(planned))
}

// PrecheckGroupConfig 对分组配置的目标态做预检（改成白名单、改映射都会改变预检结果）。
func (p *OpenPrechecker) PrecheckGroupConfig(ctx context.Context, groupID int64, target MatrixGroupConfig) (*OpenPrecheckReport, error) {
	reports, err := p.PrecheckGroups(ctx, []int64{groupID}, CellOverlay{Configs: map[int64]MatrixGroupConfig{groupID: target}})
	if err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return &OpenPrecheckReport{GroupID: groupID, Blocking: []OpenPrecheckIssue{}, Warnings: []OpenPrecheckIssue{}}, nil
	}
	return &reports[0], nil
}

// PublishCheck W5 group.publish 的对外公开预检：v2 的白名单分组有预检问题就阻止（不依赖 billing_unpriced_policy）；
// 其余分组不阻止（开放分组与 legacy、shadow 分组只给报告）。
func (p *OpenPrechecker) PublishCheck(ctx context.Context, groupID int64) (*OpenPrecheckReport, error) {
	rep, err := p.PrecheckGroupConfigState(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if rep.Applicable && rep.AccessMode == MatrixAccessAllowlist && len(rep.Blocking) > 0 {
		return rep, precheckError(ReasonGroupPublishBlocked, "the group cannot be published: an allowlist group would expose models without a usable price", rep.Blocking)
	}
	return rep, nil
}

// PrecheckGroupConfigState 分组现状的预检（不叠加任何东西）。
func (p *OpenPrechecker) PrecheckGroupConfigState(ctx context.Context, groupID int64) (*OpenPrecheckReport, error) {
	reports, err := p.PrecheckGroups(ctx, []int64{groupID}, CellOverlay{})
	if err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return &OpenPrecheckReport{GroupID: groupID, Blocking: []OpenPrecheckIssue{}, Warnings: []OpenPrecheckIssue{}}, nil
	}
	return &reports[0], nil
}

// BlockingError 把一组报告里的阻止项合成一个错误；没有阻止项返回 nil。
func BlockingError(reports []OpenPrecheckReport) error {
	var issues []OpenPrecheckIssue
	for _, r := range reports {
		issues = append(issues, r.Blocking...)
	}
	if len(issues) == 0 {
		return nil
	}
	return precheckError(ReasonOpenPrecheckBlocked, "an allowlist group would expose models without a usable price", issues)
}

// openIssueList 把问题列成「分组:模型:原因[->映射目标]」，用分号连接，最多 maxOpenPrecheckIssuesListed 项。
func openIssueList(issues []OpenPrecheckIssue) string {
	parts := make([]string, 0, len(issues))
	for i, is := range issues {
		if i == maxOpenPrecheckIssuesListed {
			break
		}
		item := strconv.FormatInt(is.GroupID, 10) + ":" + is.ModelKey + ":" + is.Reason
		if is.Target != "" {
			item += "->" + is.Target
		}
		parts = append(parts, item)
	}
	return strings.Join(parts, ";")
}

func precheckError(reason, message string, issues []OpenPrecheckIssue) error {
	return infraerrors.BadRequest(reason, message).WithMetadata(map[string]string{
		"count":  strconv.Itoa(len(issues)),
		"issues": openIssueList(issues),
	})
}
