package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2a：ExposureValidator 的「保存时」校验（设计文档 5.2）。
//
// 不变量：白名单（allowlist）分组里每一个 open 的精确模型名单元格都必须「可报价且不是 0 元」。
// 本文件只放校验逻辑与它在写事务里的入口（ExposureGuard），不碰价格数据本身：
// 官方价由调用方注入（OfficialPriceStateSource），已知免费名单读 billing_known_free_list（PR1 的同一份）。
//
// 范围与已知缺口（都写进 PR 说明）：
//   - 通配符单元格不检查：它只由渠道派生产生、写入器不能创建，且不对应单一模型；
//   - 带生效时间窗的单元格不检查：写入器现在拒绝写它们，窗口边界时刻的可报价要等 PR7 统一计价时刻之后；
//   - 开放分组（open）不阻止，只有白名单分组阻止；警告与预览在 PR4b-2b 的开放时预检里给。

// 保存时校验的错误原因。
const (
	// ReasonExposureUnpriced 白名单分组里会出现无价或 0 元的 open 单元格；metadata.violations 列出违规项。
	ReasonExposureUnpriced = "EXPOSURE_UNPRICED"
	// ReasonExposureGuardMissing 写入口没有配置保存时校验，按失败关闭处理。
	ReasonExposureGuardMissing = "EXPOSURE_GUARD_UNAVAILABLE"
)

// maxExposureViolationsListed 错误 metadata 里最多列出的违规项。
const maxExposureViolationsListed = 20

// ExposureViolationReason 违规原因。
type ExposureViolationReason string

const (
	// ExposureUnpriced 没有任何可用价格：官方价里没有这个模型，单元格也没有非零的自定义价。
	ExposureUnpriced ExposureViolationReason = "unpriced"
	// ExposureZeroPrice 有价格来源，但所有价格都是 0，且不在已知免费名单里（S-5）。
	ExposureZeroPrice ExposureViolationReason = "zero_price"
)

// ExposureViolation 一个违规的（分组、模型）。
type ExposureViolation struct {
	GroupID  int64                   `json:"group_id"`
	ModelKey string                  `json:"model_key"`
	Reason   ExposureViolationReason `json:"reason"`
}

// OfficialPriceState 官方价里关于某个模型的事实：有没有价、价是不是全 0。
type OfficialPriceState struct {
	// Known 动态目录或内置兜底里有这个模型的价格。
	Known bool
	// NonZero 至少有一个非零的 token 单价，或者是带按张价的图片模型。
	NonZero bool
}

// OfficialPriceStateSource 查询官方价事实，由 BillingService 实现。
type OfficialPriceStateSource interface {
	LookupOfficialPriceState(model string) OfficialPriceState
}

// ExposureCell 待校验的 open 单元格；Cell 是写入之后的目标态。
type ExposureCell struct {
	GroupID int64
	Cell    MatrixCell
}

// ExposureReader 校验需要读的两样东西，都在调用方的事务（或只读连接）里读。
type ExposureReader interface {
	// AccessModesTx 返回分组的准入模式；没有配置行的分组不在结果里。
	AccessModesTx(ctx context.Context, exec MatrixExecutor, groupIDs []int64) (map[int64]MatrixAccessMode, error)
	// OpenCellsTx 返回分组里 open 的精确模型名单元格（不含通配符，不含带生效时间窗的）。
	OpenCellsTx(ctx context.Context, exec MatrixExecutor, groupIDs []int64) ([]ExposureCell, error)
}

// ExposureValidator 白名单分组不变量的判定函数（不做 I/O，除了读已知免费名单）。
type ExposureValidator struct {
	prices   OfficialPriceStateSource
	settings SettingRepository
}

// NewExposureValidator 创建校验器。prices 为 nil 时官方价一律按「没有」处理（失败关闭）；
// settings 为 nil 时已知免费名单为空。
func NewExposureValidator(prices OfficialPriceStateSource, settings SettingRepository) *ExposureValidator {
	return &ExposureValidator{prices: prices, settings: settings}
}

// Check 判定一批单元格（调用方只传白名单分组的单元格）。返回按（分组、模型）排序的违规项。
func (v *ExposureValidator) Check(ctx context.Context, cells []ExposureCell) []ExposureViolation {
	if len(cells) == 0 {
		return nil
	}
	free := loadBillingKnownFreeList(ctx, v.settings)
	var out []ExposureViolation
	for _, ec := range cells {
		c := ec.Cell
		if !c.Open || c.IsPattern {
			continue
		}
		if reason, bad := v.evaluate(ec.GroupID, c, free); bad {
			out = append(out, ExposureViolation{GroupID: ec.GroupID, ModelKey: c.ModelKey, Reason: reason})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		return out[i].ModelKey < out[j].ModelKey
	})
	return out
}

func (v *ExposureValidator) evaluate(groupID int64, c MatrixCell, free []BillingKnownFreeEntry) (ExposureViolationReason, bool) {
	if c.PriceMode == MatrixPriceCustom && c.CustomPrice != nil && customPriceHasNonZero(c.CustomPrice) {
		return "", false
	}
	if billingKnownFreeMatches(free, groupID, c.ModelKey) {
		return "", false
	}
	// 自定义价里写了显式的 0（而不是留空回落官方价）：官方价不会盖住它，只有已知免费名单能放行。
	if c.PriceMode == MatrixPriceCustom && c.CustomPrice != nil && customPriceHasExplicitValue(c.CustomPrice) {
		return ExposureZeroPrice, true
	}
	var st OfficialPriceState
	if v.prices != nil {
		st = v.prices.LookupOfficialPriceState(c.ModelKey)
	}
	switch {
	case !st.Known:
		return ExposureUnpriced, true
	case !st.NonZero:
		return ExposureZeroPrice, true
	}
	return "", false
}

func exposurePositive(p *float64) bool { return p != nil && *p > 0 }

// customPriceHasNonZero 自定义价里至少有一个字段是正数。
func customPriceHasNonZero(cp *MatrixCustomPrice) bool {
	if exposurePositive(cp.InputPrice) || exposurePositive(cp.OutputPrice) || exposurePositive(cp.CacheWritePrice) ||
		exposurePositive(cp.CacheReadPrice) || exposurePositive(cp.ImageOutputPrice) || exposurePositive(cp.PerRequestPrice) {
		return true
	}
	for _, iv := range cp.Intervals {
		if exposurePositive(iv.InputPrice) || exposurePositive(iv.OutputPrice) || exposurePositive(iv.CacheWritePrice) ||
			exposurePositive(iv.CacheReadPrice) || exposurePositive(iv.PerRequestPrice) {
			return true
		}
	}
	return false
}

// customPriceHasExplicitValue 自定义价里至少有一个字段不是留空（含显式的 0）。
func customPriceHasExplicitValue(cp *MatrixCustomPrice) bool {
	if cp.InputPrice != nil || cp.OutputPrice != nil || cp.CacheWritePrice != nil ||
		cp.CacheReadPrice != nil || cp.ImageOutputPrice != nil || cp.PerRequestPrice != nil {
		return true
	}
	for _, iv := range cp.Intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil || iv.CacheWritePrice != nil ||
			iv.CacheReadPrice != nil || iv.PerRequestPrice != nil {
			return true
		}
	}
	return false
}

// ExposureGuard 把校验器接到写事务里：单元格写入与分组配置写入共用。
// 约定：CheckCellWrites / CheckGroups 必须在写入所用的同一个事务里调用（读到的是已加锁、已写入的状态）；
// 预览时传只读连接，结果只作提示。
type ExposureGuard struct {
	reader    ExposureReader
	validator *ExposureValidator
}

// NewExposureGuard 创建关口。
func NewExposureGuard(reader ExposureReader, validator *ExposureValidator) *ExposureGuard {
	return &ExposureGuard{reader: reader, validator: validator}
}

func (g *ExposureGuard) ready() error {
	if g == nil || g.reader == nil || g.validator == nil {
		return infraerrors.InternalServer(ReasonExposureGuardMissing, "exposure validation is not configured")
	}
	return nil
}

// CheckCellWrites 校验规划里会留下 open 单元格的写入（创建或更新，包括只改 open 的）；删除与 noop 不查。
func (g *ExposureGuard) CheckCellWrites(ctx context.Context, exec MatrixExecutor, planned []PlannedCellWrite) error {
	if err := g.ready(); err != nil {
		return err
	}
	var targets []ExposureCell
	seen := make(map[int64]struct{})
	var groupIDs []int64
	for _, p := range planned {
		if (p.Action != CellWriteCreate && p.Action != CellWriteUpdate) || p.After == nil || !p.After.Open {
			continue
		}
		targets = append(targets, ExposureCell{GroupID: p.Op.GroupID, Cell: *p.After})
		if _, ok := seen[p.Op.GroupID]; !ok {
			seen[p.Op.GroupID] = struct{}{}
			groupIDs = append(groupIDs, p.Op.GroupID)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	modes, err := g.reader.AccessModesTx(ctx, exec, groupIDs)
	if err != nil {
		return err
	}
	var checked []ExposureCell
	for _, t := range targets {
		if modes[t.GroupID] == MatrixAccessAllowlist {
			checked = append(checked, t)
		}
	}
	return exposureError(g.validator.Check(ctx, checked))
}

// CheckGroups 校验这些分组：白名单分组里现有的每一个 open 精确单元格。
func (g *ExposureGuard) CheckGroups(ctx context.Context, exec MatrixExecutor, groupIDs []int64) error {
	if err := g.ready(); err != nil {
		return err
	}
	if len(groupIDs) == 0 {
		return nil
	}
	modes, err := g.reader.AccessModesTx(ctx, exec, groupIDs)
	if err != nil {
		return err
	}
	var allow []int64
	for _, id := range groupIDs {
		if modes[id] == MatrixAccessAllowlist {
			allow = append(allow, id)
		}
	}
	if len(allow) == 0 {
		return nil
	}
	cells, err := g.reader.OpenCellsTx(ctx, exec, allow)
	if err != nil {
		return err
	}
	return exposureError(g.validator.Check(ctx, cells))
}

func exposureError(violations []ExposureViolation) error {
	if len(violations) == 0 {
		return nil
	}
	parts := make([]string, 0, len(violations))
	for i, v := range violations {
		if i == maxExposureViolationsListed {
			break
		}
		parts = append(parts, fmt.Sprintf("%d:%s:%s", v.GroupID, v.ModelKey, v.Reason))
	}
	return infraerrors.BadRequest(ReasonExposureUnpriced,
		"an allowlist group cannot expose a model that has no price or a zero price").WithMetadata(map[string]string{
		"count":      strconv.Itoa(len(violations)),
		"violations": strings.Join(parts, ";"),
	})
}
