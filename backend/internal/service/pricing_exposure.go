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
//   - 通配符单元格：open 的通配符单元格放行的是整个前缀，没有办法逐个模型验证价格，所以在白名单分组里按违规处理
//     （原因 wildcard_unverifiable，失败关闭）；它只由渠道派生产生、写入器不能创建；
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
	// ExposureWildcardUnverifiable open 的通配符单元格：放行整个前缀，没有办法验证前缀下每个模型都有价，失败关闭。
	ExposureWildcardUnverifiable ExposureViolationReason = "wildcard_unverifiable"
	// ExposureZeroPrice 有价格来源，但所有价格都是 0，且不在已知免费名单里（S-5）。
	ExposureZeroPrice ExposureViolationReason = "zero_price"
)

// ExposureViolation 一个违规的（分组、模型）。
type ExposureViolation struct {
	GroupID  int64                   `json:"group_id"`
	ModelKey string                  `json:"model_key"`
	Reason   ExposureViolationReason `json:"reason"`
}

// OfficialPriceState 官方价里关于某个模型的事实：有没有价、token 价是不是全 0、是不是图片模型。
type OfficialPriceState struct {
	// Known 动态目录或内置兜底里有这个模型的价格。
	Known bool
	// TokenNonZero 至少有一个正的 token 单价（含图片 token 价）。token 模式的 custom 单元格留空的字段回落到它。
	TokenNonZero bool
	// ImageCapable 图片模型（有按张价或图片 token 价）。只对 inherit、extra 单元格算有价：
	// 无渠道价时图片请求走 CalculateImageCost（有兜底价）；custom 单元格一旦有渠道价，图片请求不再按张计费。
	ImageCapable bool
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
	// OpenCellsTx 返回分组里 open 的单元格，精确名与通配符都包含（不含带生效时间窗的）。
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

// knownFree 读取已知免费名单（读失败或写坏时是空名单）。
func (v *ExposureValidator) knownFree(ctx context.Context) []BillingKnownFreeEntry {
	return loadBillingKnownFreeList(ctx, v.settings)
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
		if !c.Open {
			continue
		}
		if c.IsPattern {
			out = append(out, ExposureViolation{GroupID: ec.GroupID, ModelKey: c.ModelKey, Reason: ExposureWildcardUnverifiable})
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

// evaluate 按计费模式分别判定（与运行时一致，B1）：
//   - 已知免费名单最先放行；
//   - custom 的 per_request / image 模式：只看按次价（顶层与区间），不回落官方价，也不看 token 字段；
//   - custom 的 token 模式：只看 token 字段；有显式值但全是 0 判 zero_price；全部留空才回落官方的 token 价；
//   - inherit、extra：官方有价，且 token 价非零或是图片模型。
func (v *ExposureValidator) evaluate(groupID int64, c MatrixCell, free []BillingKnownFreeEntry) (ExposureViolationReason, bool) {
	if billingKnownFreeMatches(free, groupID, c.ModelKey) {
		return "", false
	}
	var st OfficialPriceState
	if v.prices != nil {
		st = v.prices.LookupOfficialPriceState(c.ModelKey)
	}
	if c.PriceMode == MatrixPriceCustom && c.CustomPrice != nil {
		cp := c.CustomPrice
		switch cp.BillingMode {
		case BillingModePerRequest, BillingModeImage:
			if customPerRequestHasPositive(cp) {
				return "", false
			}
			return ExposureZeroPrice, true
		default:
			// 有有效区间时，运行时不看顶层的 input/output/cache 价，命中区间后区间里留空的字段按 0 计，
			// 也不回落官方价；顶层只有 image_output_price 仍然生效（B1′）。
			if customHasEffectiveInterval(cp) {
				if customIntervalTokenHasPositive(cp) || exposurePositive(cp.ImageOutputPrice) {
					return "", false
				}
				return ExposureZeroPrice, true
			}
			if customTokenHasPositive(cp) {
				return "", false
			}
			if customTokenHasExplicitValue(cp) {
				return ExposureZeroPrice, true
			}
			// 全部留空：回落官方的 token 价，图片能力不算。
			switch {
			case !st.Known:
				return ExposureUnpriced, true
			case !st.TokenNonZero:
				return ExposureZeroPrice, true
			}
			return "", false
		}
	}
	switch {
	case !st.Known:
		return ExposureUnpriced, true
	case !st.TokenNonZero && !st.ImageCapable:
		return ExposureZeroPrice, true
	}
	return "", false
}

func exposurePositive(p *float64) bool { return p != nil && *p > 0 }

// customPerRequestHasPositive 按次价（顶层或区间）至少有一个是正数。
func customPerRequestHasPositive(cp *MatrixCustomPrice) bool {
	if exposurePositive(cp.PerRequestPrice) {
		return true
	}
	for _, iv := range cp.Intervals {
		if exposurePositive(iv.PerRequestPrice) {
			return true
		}
	}
	return false
}

// customHasEffectiveInterval 有「有效区间」：与运行时 filterValidIntervals 同口径，
// 五个价格字段（含 per_request_price）任意一个非 nil 就算。
func customHasEffectiveInterval(cp *MatrixCustomPrice) bool {
	for _, iv := range cp.Intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil || iv.CacheWritePrice != nil ||
			iv.CacheReadPrice != nil || iv.PerRequestPrice != nil {
			return true
		}
	}
	return false
}

// customIntervalTokenHasPositive 区间里的 input、output、cache_write、cache_read 至少有一个是正数。
func customIntervalTokenHasPositive(cp *MatrixCustomPrice) bool {
	for _, iv := range cp.Intervals {
		if exposurePositive(iv.InputPrice) || exposurePositive(iv.OutputPrice) ||
			exposurePositive(iv.CacheWritePrice) || exposurePositive(iv.CacheReadPrice) {
			return true
		}
	}
	return false
}

// customTokenHasPositive token 字段（顶层与区间）至少有一个是正数。
func customTokenHasPositive(cp *MatrixCustomPrice) bool {
	if exposurePositive(cp.InputPrice) || exposurePositive(cp.OutputPrice) || exposurePositive(cp.CacheWritePrice) ||
		exposurePositive(cp.CacheReadPrice) || exposurePositive(cp.ImageOutputPrice) {
		return true
	}
	for _, iv := range cp.Intervals {
		if exposurePositive(iv.InputPrice) || exposurePositive(iv.OutputPrice) ||
			exposurePositive(iv.CacheWritePrice) || exposurePositive(iv.CacheReadPrice) {
			return true
		}
	}
	return false
}

// customTokenHasExplicitValue token 字段（顶层与区间）至少有一个不是留空（含显式的 0）。
func customTokenHasExplicitValue(cp *MatrixCustomPrice) bool {
	if cp.InputPrice != nil || cp.OutputPrice != nil || cp.CacheWritePrice != nil ||
		cp.CacheReadPrice != nil || cp.ImageOutputPrice != nil {
		return true
	}
	for _, iv := range cp.Intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil || iv.CacheWritePrice != nil || iv.CacheReadPrice != nil {
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

// CheckGroupAsAllowlist 预览用：分组还不是白名单时，按「假如它已经是白名单」校验它现有的 open 精确单元格。
// 保存时校验（CheckGroups）读的是库里的准入模式，改成白名单的写入要先预览、后提交，预览时库里还是旧模式。
func (g *ExposureGuard) CheckGroupAsAllowlist(ctx context.Context, exec MatrixExecutor, groupID int64) error {
	if err := g.ready(); err != nil {
		return err
	}
	cells, err := g.reader.OpenCellsTx(ctx, exec, []int64{groupID})
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
