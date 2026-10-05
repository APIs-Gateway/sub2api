package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2a：分组配置（准入模式、计费来源、模型映射、功能开关、成本模式）的写路径。
//
// 与单元格写入器同一套约定：只写 pricing_stage = 'v2' 的分组；先锁 group_model_config 行（与派生钩子、
// 阶段切换互斥）；带基线 revision，对不上就拒绝；内容变了才把 revision 加一（它是 HasPrice 缓存键的一部分）；
// 只能在事务里写（MatrixTx）。分组配置不是「价格字段」，所以不走审批，但会让白名单分组出现无价单元格的改动
// （改成白名单、改映射、改计费来源）在同一事务里过一次 ExposureGuard.CheckGroups。
// 本 PR 不新增分组配置的历史表：revision 与 updated_at 是仅有的痕迹，审计留给 W5 change-set。

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
	ApplyTx(ctx context.Context, tx MatrixTx, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error)
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

// GroupConfigService 分组配置写入的服务层：事务只经 PriceWriteStore.WithTx 打开。
type GroupConfigService struct {
	store       PriceWriteStore
	writer      GroupConfigWriter
	exposure    *ExposureGuard
	invalidator MatrixSnapshotInvalidator
}

// NewGroupConfigService 创建服务。exposure 为 nil 时改动准入、映射、计费来源的写入失败关闭；
// invalidator 可为 nil（没有读取方）。
func NewGroupConfigService(store PriceWriteStore, writer GroupConfigWriter, exposure *ExposureGuard, invalidator MatrixSnapshotInvalidator) *GroupConfigService {
	return &GroupConfigService{store: store, writer: writer, exposure: exposure, invalidator: invalidator}
}

// Apply 写入一个分组的配置。保存时校验在写入之后、提交之前：违规整个事务回滚。
func (s *GroupConfigService) Apply(ctx context.Context, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error) {
	if req.OperatorID <= 0 {
		return nil, infraerrors.Forbidden(ReasonPriceWriteActorRequired, "an administrator is required")
	}
	norm, err := NormalizeGroupConfigWrite(req)
	if err != nil {
		return nil, err
	}
	var result *GroupConfigWriteResult
	err = s.store.WithTx(ctx, func(ctx context.Context, tx MatrixTx) error {
		res, err := s.writer.ApplyTx(ctx, tx, norm)
		if err != nil {
			return err
		}
		if res.Changed && res.ExposureRelevant && res.After.AccessMode == MatrixAccessAllowlist {
			if err := s.exposure.CheckGroups(ctx, tx, []int64{norm.GroupID}); err != nil {
				return err
			}
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
