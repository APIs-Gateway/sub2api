package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2a：分组配置（准入模式、计费来源、模型映射、功能开关、成本模式）的写路径。
//
// 与单元格写入器同一套约定：只写 pricing_stage = 'v2' 的分组；先锁 group_model_config 行（与派生钩子、
// 阶段切换互斥）；带基线 revision，对不上就拒绝；内容变了才把 revision 加一（它是 HasPrice 缓存键的一部分）；
// 只能在事务里写（MatrixTx），而且只经 MatrixTxWriter.ApplyGroupConfigTx 写（写入与保存时校验做成一体）。
//
// PR4b-2b-1：改计费来源、改模型映射会改变「请求按哪个模型计费」，等于改价，所以走审批（pricing_write_approvals，
// kind = group_config）：先 Propose（估算器给出价格方向，summary 存前后对比与操作人），再带着凭证 Commit；
// 方向不是 none 的要求交互式管理员会话。改准入、功能开关、成本模式不涉价，不需要凭证，但仍要二次确认。
// 会让白名单分组出现无价单元格的改动（改成白名单）在同一事务里过一次 ExposureGuard.CheckGroups。
// 审批行是涉价的分组配置写入的审计记录；不涉价的写入，revision 与 updated_at 是仅有的痕迹，审计留给 W5 change-set。

// 分组配置写入的错误原因。
const (
	ReasonGroupConfigEmpty   = "GROUP_CONFIG_EMPTY"
	ReasonGroupConfigInvalid = "GROUP_CONFIG_INVALID"
	ReasonGroupConfigNotV2   = "GROUP_CONFIG_NOT_V2"
)

// MaxGroupMappingEntries 一个分组最多的模型映射条数。
const MaxGroupMappingEntries = 500

// maxFeaturePlatformKeyLen web_search_emulation 里平台键的最大长度。
const maxFeaturePlatformKeyLen = 32

// GroupConfigWriteRequest 对一个分组配置的修改：只改带了的字段，其余保持原样。
// 每个带上的字段都是目标态（整个替换，不做合并）。
type GroupConfigWriteRequest struct {
	GroupID int64 `json:"group_id"`
	// BaselineRevision 调用方读到的 group_model_config.revision，必须大于 0。
	BaselineRevision int64 `json:"baseline_revision"`

	AccessMode *MatrixAccessMode `json:"access_mode,omitempty"`
	// BillingModelSource 设置计费来源；ClearBillingModelSource 把它设回 NULL（无渠道）。二者互斥。
	BillingModelSource      *string `json:"billing_model_source,omitempty"`
	ClearBillingModelSource bool    `json:"clear_billing_model_source,omitempty"`
	// ModelMapping 整个映射数组；写入时规范成固定顺序（精确名在前，通配符前缀长者优先）。
	ModelMapping *[]MatrixMappingEntry `json:"model_mapping,omitempty"`
	// Features 整个功能开关对象，形状见 normalizeGroupFeatures。
	Features *map[string]any `json:"features,omitempty"`
	CostMode *MatrixCostMode `json:"cost_mode,omitempty"`

	OperatorID int64 `json:"-"`
}

// GroupConfigWriteResult 一次分组配置写入的结果。
type GroupConfigWriteResult struct {
	Before StoredGroupConfig `json:"before"`
	After  StoredGroupConfig `json:"after"`
	// Changed 内容是否真的变了；没变就不加 revision。
	Changed bool `json:"changed"`
	// ExposureRelevant 准入模式、模型映射或计费来源变了：这三项会改变「哪些模型按什么价开放」，要过保存时校验。
	ExposureRelevant bool `json:"exposure_relevant"`
}

// GroupConfigWriter 分组配置的 tx-aware 写入（有状态的只有事务）。
type GroupConfigWriter interface {
	// ApplyTx 在调用方的事务里写入：按 group_id 对配置行 SELECT ... FOR UPDATE，确认分组是 v2、基线未变，
	// 内容变了才写并把 revision 加一。提交之后调用方必须失效分组快照缓存。
	// 只能经 MatrixTxWriter.ApplyGroupConfigTx 调用（pricing_write_tx_guard_test.go 守着）。
	ApplyTx(ctx context.Context, tx MatrixTx, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error)
	// PlanTx 读取现状并规划，不写入、不加锁（预览用）；校验（v2、基线）与 ApplyTx 完全一致。
	PlanTx(ctx context.Context, exec MatrixExecutor, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error)
}

func groupConfigError(reason, msg string) error {
	return infraerrors.BadRequest(reason, msg)
}

// NormalizeGroupConfigWrite 校验并规范请求（副本）：枚举、映射、功能开关。
func NormalizeGroupConfigWrite(req GroupConfigWriteRequest) (GroupConfigWriteRequest, error) {
	if req.GroupID <= 0 {
		return req, groupConfigError(ReasonGroupConfigInvalid, "group_id must be positive")
	}
	if req.BaselineRevision <= 0 {
		return req, infraerrors.BadRequest(ReasonCellGroupBaselineMissing, "group revision baseline is missing").
			WithMetadata(map[string]string{"group_id": strconv.FormatInt(req.GroupID, 10)})
	}
	if req.AccessMode == nil && req.BillingModelSource == nil && !req.ClearBillingModelSource &&
		req.ModelMapping == nil && req.Features == nil && req.CostMode == nil {
		return req, groupConfigError(ReasonGroupConfigEmpty, "no group configuration field to change")
	}
	if req.AccessMode != nil {
		switch *req.AccessMode {
		case MatrixAccessOpen, MatrixAccessAllowlist:
		default:
			return req, groupConfigError(ReasonGroupConfigInvalid, "access_mode must be open or allowlist")
		}
	}
	if req.CostMode != nil {
		switch *req.CostMode {
		case MatrixCostAccountRate, MatrixCostCatalogUpstream, MatrixCostFollowBilling:
		default:
			return req, groupConfigError(ReasonGroupConfigInvalid, "cost_mode must be account_rate, catalog_upstream or follow_billing")
		}
	}
	if req.BillingModelSource != nil && req.ClearBillingModelSource {
		return req, groupConfigError(ReasonGroupConfigInvalid, "billing_model_source and clear_billing_model_source are mutually exclusive")
	}
	if req.BillingModelSource != nil {
		switch *req.BillingModelSource {
		case BillingModelSourceRequested, BillingModelSourceUpstream, BillingModelSourceChannelMapped:
		default:
			return req, groupConfigError(ReasonGroupConfigInvalid, "billing_model_source must be requested, upstream or channel_mapped")
		}
	}
	if req.ModelMapping != nil {
		m, err := normalizeGroupMapping(*req.ModelMapping)
		if err != nil {
			return req, err
		}
		req.ModelMapping = &m
	}
	if req.Features != nil {
		f, err := normalizeGroupFeatures(*req.Features)
		if err != nil {
			return req, err
		}
		req.Features = &f
	}
	return req, nil
}

// normalizeGroupMapping 校验映射并规范成固定顺序（与派生同一个排序函数 deriveMatrixMapping）。
// src 与 dst 去首尾空格；src 只允许在末尾带一个 *；src 小写后不能重复。
func normalizeGroupMapping(in []MatrixMappingEntry) ([]MatrixMappingEntry, error) {
	if len(in) > MaxGroupMappingEntries {
		return nil, groupConfigError(ReasonGroupConfigInvalid, fmt.Sprintf("at most %d mapping entries", MaxGroupMappingEntries))
	}
	byKey := make(map[string]string, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, e := range in {
		src, dst := strings.TrimSpace(e.Src), strings.TrimSpace(e.Dst)
		switch {
		case src == "" || src == "*":
			return nil, groupConfigError(ReasonGroupConfigInvalid, "a mapping source must name a model or a prefix before *")
		case len(src) > matrixModelKeyMaxLen || len(dst) > matrixModelKeyMaxLen:
			return nil, groupConfigError(ReasonGroupConfigInvalid, fmt.Sprintf("a mapping name is longer than %d characters", matrixModelKeyMaxLen))
		case strings.Contains(strings.TrimSuffix(src, "*"), "*"):
			return nil, groupConfigError(ReasonGroupConfigInvalid, "a mapping source may only carry one * at the end")
		case strings.Contains(dst, "*"):
			return nil, groupConfigError(ReasonGroupConfigInvalid, "a mapping target cannot contain *")
		}
		lower := strings.ToLower(src)
		if _, dup := seen[lower]; dup {
			return nil, groupConfigError(ReasonGroupConfigInvalid, "the same mapping source appears more than once")
		}
		seen[lower] = struct{}{}
		byKey[src] = dst
	}
	return deriveMatrixMapping(byKey), nil
}

// normalizeGroupFeatures 校验功能开关的形状（与 deriveMatrixFeatures 产出的形状一致，S-2）：
//   - web_search_emulation：{平台: bool}；
//   - bedrock_cc_compat：bool；
//   - codex_image_generation_bridge：bool（null 视为没设，丢掉这个键）。
//
// 其余键一律拒绝。返回新的 map，不改入参。
func normalizeGroupFeatures(in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch k {
		case featureKeyWebSearchEmulation:
			m, ok := v.(map[string]any)
			if !ok {
				return nil, groupConfigError(ReasonGroupConfigInvalid, "web_search_emulation must be an object of platform to boolean")
			}
			per := make(map[string]any, len(m))
			for platform, raw := range m {
				b, isBool := raw.(bool)
				if !isBool || strings.TrimSpace(platform) == "" || len(platform) > maxFeaturePlatformKeyLen {
					return nil, groupConfigError(ReasonGroupConfigInvalid, "web_search_emulation must be an object of platform to boolean")
				}
				per[platform] = b
			}
			out[k] = per
		case featureKeyBedrockCCCompat:
			b, ok := v.(bool)
			if !ok {
				return nil, groupConfigError(ReasonGroupConfigInvalid, "bedrock_cc_compat must be a boolean")
			}
			out[k] = b
		case featureKeyCodexImageGenerationBridge:
			if v == nil {
				continue
			}
			b, ok := v.(bool)
			if !ok {
				return nil, groupConfigError(ReasonGroupConfigInvalid, "codex_image_generation_bridge must be a boolean")
			}
			out[k] = b
		default:
			return nil, infraerrors.BadRequest(ReasonGroupConfigInvalid, "unknown feature switch").
				WithMetadata(map[string]string{"feature": k})
		}
	}
	return out, nil
}

// ApplyGroupConfigPatch 把已规范的请求套到现状上，返回目标配置（纯函数，不改入参）。
func ApplyGroupConfigPatch(cur MatrixGroupConfig, req GroupConfigWriteRequest) MatrixGroupConfig {
	out := cur
	if req.AccessMode != nil {
		out.AccessMode = *req.AccessMode
	}
	if req.BillingModelSource != nil {
		v := *req.BillingModelSource
		out.BillingModelSource = &v
	}
	if req.ClearBillingModelSource {
		out.BillingModelSource = nil
	}
	if req.ModelMapping != nil {
		out.ModelMapping = append([]MatrixMappingEntry{}, *req.ModelMapping...)
	}
	if req.Features != nil {
		out.Features = *req.Features
	}
	if req.CostMode != nil {
		out.CostMode = *req.CostMode
	}
	return out
}

// GroupConfigChange 比较前后配置：是否有变化，以及是否碰了准入、映射或计费来源。
func GroupConfigChange(before, after MatrixGroupConfig) (changed, exposureRelevant bool) {
	exposureRelevant = before.AccessMode != after.AccessMode ||
		matrixCanonicalJSON(before.BillingModelSource) != matrixCanonicalJSON(after.BillingModelSource) ||
		matrixCanonicalJSON(before.ModelMapping) != matrixCanonicalJSON(after.ModelMapping)
	changed = exposureRelevant || before.CostMode != after.CostMode ||
		matrixCanonicalJSON(before.Features) != matrixCanonicalJSON(after.Features)
	return changed, exposureRelevant
}

// GroupConfigTouchesPrice 改动是否涉价：计费来源或模型映射变了。它们决定请求按哪个模型计费，所以按改价处理；
// 准入模式、功能开关、成本模式不改用户实付的价格。
func GroupConfigTouchesPrice(before, after MatrixGroupConfig) bool {
	return matrixCanonicalJSON(before.BillingModelSource) != matrixCanonicalJSON(after.BillingModelSource) ||
		matrixCanonicalJSON(before.ModelMapping) != matrixCanonicalJSON(after.ModelMapping)
}

// GroupConfigPlanHash 计划指纹：规范化之后的请求（含基线 revision，不含操作人）的 SHA-256。
// req 必须已经过 NormalizeGroupConfigWrite。
func GroupConfigPlanHash(req GroupConfigWriteRequest) string {
	sum := sha256.Sum256([]byte(matrixCanonicalJSON(req)))
	return hex.EncodeToString(sum[:])
}

// groupConfigSummary 审批行的 summary：前后对比、价格方向与操作人，兼作涉价的分组配置写入的审计记录。
type groupConfigSummary struct {
	GroupID    int64             `json:"group_id"`
	OperatorID int64             `json:"operator_id"`
	Delta      PriceDelta        `json:"price_delta"`
	Before     MatrixGroupConfig `json:"before"`
	After      MatrixGroupConfig `json:"after"`
}

// GroupConfigService 分组配置写入的服务层：事务只经 PriceWriteStore.WithTx 打开，写入只经 MatrixTxWriter。
type GroupConfigService struct {
	store       PriceWriteStore
	tx          *MatrixTxWriter
	estimator   PriceDeltaEstimator
	invalidator MatrixSnapshotInvalidator
	now         func() time.Time
}

// NewGroupConfigService 创建服务。tx 是写入与保存时校验的唯一入口（nil 则失败关闭）；
// estimator 是价格方向的唯一来源（nil 时涉价写入一律按 unknown，即必须交互式会话）；invalidator 可为 nil（没有读取方）。
func NewGroupConfigService(store PriceWriteStore, tx *MatrixTxWriter, estimator PriceDeltaEstimator, invalidator MatrixSnapshotInvalidator) *GroupConfigService {
	return &GroupConfigService{store: store, tx: tx, estimator: estimator, invalidator: invalidator, now: time.Now}
}

// GroupConfigTicket 一次分组配置预览的结果。
type GroupConfigTicket struct {
	// ApprovalID 预览凭证；0 表示不涉价（或没有变化），不需要凭证。
	ApprovalID       int64             `json:"approval_id"`
	PlanHash         string            `json:"plan_hash"`
	Changed          bool              `json:"changed"`
	ExposureRelevant bool              `json:"exposure_relevant"`
	TouchesPrice     bool              `json:"touches_price"`
	Delta            PriceDelta        `json:"price_delta"`
	ExpiresAt        time.Time         `json:"expires_at"`
	Before           MatrixGroupConfig `json:"before"`
	After            MatrixGroupConfig `json:"after"`
}

// GroupConfigCommit 提交请求。
type GroupConfigCommit struct {
	// ApprovalID 预览凭证；0 表示没有预览，只允许不涉价的写入。
	ApprovalID int64
	Request    GroupConfigWriteRequest
	// Confirm 管理员的二次确认。
	Confirm bool
	Actor   PriceWriteActor
}

// Propose 登记一次分组配置预览：读取现状、规划、做保存时校验（只作提示），涉价时由估算器给出价格方向并记录审批行。
// 没有变化或不涉价的改动不登记审批行，返回的凭证 ApprovalID 为 0。
func (s *GroupConfigService) Propose(ctx context.Context, req GroupConfigWriteRequest) (*GroupConfigTicket, error) {
	if req.OperatorID <= 0 {
		return nil, infraerrors.Forbidden(ReasonPriceWriteActorRequired, "an administrator is required")
	}
	norm, err := NormalizeGroupConfigWrite(req)
	if err != nil {
		return nil, err
	}
	res, err := s.tx.PreviewGroupConfig(ctx, s.store.Reader(), norm)
	if err != nil {
		return nil, err
	}
	touches := res.Changed && GroupConfigTouchesPrice(res.Before.MatrixGroupConfig, res.After.MatrixGroupConfig)
	ticket := &GroupConfigTicket{
		PlanHash: GroupConfigPlanHash(norm), Changed: res.Changed, ExposureRelevant: res.ExposureRelevant,
		TouchesPrice: touches, Delta: PriceDeltaNone, Before: res.Before.MatrixGroupConfig, After: res.After.MatrixGroupConfig,
	}
	if !touches {
		return ticket, nil
	}
	ticket.Delta = s.estimateDelta(ctx, norm.GroupID, res)

	now := s.now()
	approval := PriceWriteApproval{
		Kind:         PriceWriteKindGroupConfig,
		PlanHash:     ticket.PlanHash,
		TouchesPrice: true,
		Delta:        ticket.Delta,
		GroupIDs:     []int64{norm.GroupID},
		Summary: []byte(matrixCanonicalJSON(groupConfigSummary{
			GroupID: norm.GroupID, OperatorID: norm.OperatorID, Delta: ticket.Delta,
			Before: ticket.Before, After: ticket.After,
		})),
		PreviewedBy: norm.OperatorID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(PriceWriteApprovalTTL),
	}
	id, err := s.store.InsertApproval(ctx, approval)
	if err != nil {
		return nil, err
	}
	if _, perr := s.store.PurgeStale(ctx, now.Add(-priceWriteStaleAfter)); perr != nil {
		slog.Warn("purge stale price write previews failed", "error", perr)
	}
	ticket.ApprovalID = id
	ticket.ExpiresAt = approval.ExpiresAt
	return ticket, nil
}

// estimateDelta 价格方向只取估算器的结果；估算器缺失、出错或返回不认识的值都是 unknown。
func (s *GroupConfigService) estimateDelta(ctx context.Context, groupID int64, res *GroupConfigWriteResult) PriceDelta {
	if s.estimator == nil {
		return PriceDeltaUnknown
	}
	return safePriceDelta(s.estimator.EstimateGroupConfig(ctx, groupID, res.Before.MatrixGroupConfig, res.After.MatrixGroupConfig))
}

// Commit 写入一个分组的配置。写入与保存时校验在同一个事务里（ApplyGroupConfigTx），违规整个事务回滚；
// 涉价的写入还要在同一个事务里消耗预览凭证，方向不是 none 的要求交互式管理员会话。
func (s *GroupConfigService) Commit(ctx context.Context, in GroupConfigCommit) (*GroupConfigWriteResult, error) {
	if in.Actor.ID <= 0 {
		return nil, infraerrors.Forbidden(ReasonPriceWriteActorRequired, "an administrator is required")
	}
	if !in.Confirm {
		return nil, infraerrors.BadRequest(ReasonPriceWriteConfirm, "the write must be confirmed a second time")
	}
	norm, err := NormalizeGroupConfigWrite(in.Request)
	if err != nil {
		return nil, err
	}
	norm.OperatorID = in.Actor.ID
	hash := GroupConfigPlanHash(norm)

	var result *GroupConfigWriteResult
	err = s.store.WithTx(ctx, func(ctx context.Context, tx MatrixTx) error {
		res, err := s.tx.ApplyGroupConfigTx(ctx, tx, norm)
		if err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, in, hash, res); err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.invalidator != nil && result.Changed {
		s.invalidator.InvalidateGroups(norm.GroupID)
	}
	return result, nil
}

// authorize 在写入事务里核对并消耗审批；返回错误会让整个事务回滚。
func (s *GroupConfigService) authorize(ctx context.Context, tx MatrixExecutor, in GroupConfigCommit, hash string, res *GroupConfigWriteResult) error {
	touches := res.Changed && GroupConfigTouchesPrice(res.Before.MatrixGroupConfig, res.After.MatrixGroupConfig)
	if in.ApprovalID == 0 {
		if touches {
			return infraerrors.Forbidden(ReasonPriceWriteApproval, "a write that touches prices needs a previewed approval")
		}
		return nil
	}
	a, err := s.store.ConsumeApproval(ctx, tx, in.ApprovalID, hash, PriceWriteKindGroupConfig, in.Actor.ID, s.now())
	if err != nil {
		return err
	}
	return checkApprovalForWrite(a, touches, in.Actor)
}
