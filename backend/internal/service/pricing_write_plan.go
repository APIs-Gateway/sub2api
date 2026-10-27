package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 单元格写入的校验、规划与计划指纹（纯函数，没有 I/O）。

const (
	// MaxCellOpsPerWrite 一次写入最多的操作数。
	MaxCellOpsPerWrite = 500
	// maxCellExtraMultiplier 额外倍率的上限（列是 NUMERIC(10,6)，这里取远小于列宽的业务上限）。
	maxCellExtraMultiplier = 1000.0
	// extraMultiplierScale 额外倍率保留 6 位小数，与列定义一致；先取整再写，保证前后对比与库里一致。
	extraMultiplierScale = 1e6
	// MaxCustomTokenPrice token 类单价的业务上限：每 token 0.01 美元（即每百万 token 1 万美元）。
	MaxCustomTokenPrice = 0.01
	// MaxCustomPerRequestPrice 按次 / 图片单价的业务上限：每次 1000 美元。
	MaxCustomPerRequestPrice = 1000.0
	// ReasonPriceTooHigh 价格超过业务上限时 metadata.reason 的取值。
	ReasonPriceTooHigh = "PRICE_TOO_HIGH"
)

// customPriceLimitViolation 检查自定义价格是否超过业务上限（含区间价格）；返回超限的字段名，没有超限返回空串。
// 写成 !(v <= max)，NaN 与 +Inf 也算超限。
func customPriceLimitViolation(cp MatrixCustomPrice) string {
	over := func(v *float64, limit float64) bool { return v != nil && !(*v <= limit) }
	token := []struct {
		field string
		val   *float64
	}{
		{"input_price", cp.InputPrice},
		{"output_price", cp.OutputPrice},
		{"cache_write_price", cp.CacheWritePrice},
		{"cache_read_price", cp.CacheReadPrice},
		{"image_output_price", cp.ImageOutputPrice},
	}
	for _, c := range token {
		if over(c.val, MaxCustomTokenPrice) {
			return c.field
		}
	}
	if over(cp.PerRequestPrice, MaxCustomPerRequestPrice) {
		return "per_request_price"
	}
	for i, iv := range cp.Intervals {
		prefix := "intervals[" + strconv.Itoa(i) + "]."
		for _, c := range []struct {
			field string
			val   *float64
		}{
			{"input_price", iv.InputPrice}, {"output_price", iv.OutputPrice},
			{"cache_write_price", iv.CacheWritePrice}, {"cache_read_price", iv.CacheReadPrice},
		} {
			if over(c.val, MaxCustomTokenPrice) {
				return prefix + c.field
			}
		}
		if over(iv.PerRequestPrice, MaxCustomPerRequestPrice) {
			return prefix + "per_request_price"
		}
	}
	return ""
}

type cellOpKey struct {
	groupID  int64
	modelKey string
}

func cellOpError(op CellOp, reason, msg string) error {
	return infraerrors.BadRequest(reason, msg).WithMetadata(map[string]string{
		"group_id":  strconv.FormatInt(op.GroupID, 10),
		"model_key": op.ModelKey,
	})
}

// NormalizeCellWriteRequest 规范并校验写入请求：模型名归一、目标态校验、按 (group_id, model_key) 排序，
// 并要求分组基线正好覆盖涉及的分组。返回的副本可直接用来算计划指纹。
func NormalizeCellWriteRequest(req CellWriteRequest) (CellWriteRequest, error) {
	ops, err := normalizeCellOps(req.Ops)
	if err != nil {
		return CellWriteRequest{}, err
	}
	groups := CellWriteGroupIDs(ops)
	if len(req.GroupRevisions) != len(groups) {
		return CellWriteRequest{}, infraerrors.BadRequest(ReasonCellGroupBaselineMissing,
			"group revisions must cover exactly the groups touched by the operations")
	}
	for _, g := range groups {
		if req.GroupRevisions[g] <= 0 {
			return CellWriteRequest{}, infraerrors.BadRequest(ReasonCellGroupBaselineMissing,
				"group revision baseline is missing").WithMetadata(map[string]string{"group_id": strconv.FormatInt(g, 10)})
		}
	}
	req.Ops = ops
	return req, nil
}

func normalizeCellOps(in []CellOp) ([]CellOp, error) {
	if len(in) == 0 {
		return nil, infraerrors.BadRequest(ReasonCellOpsEmpty, "no cell operations")
	}
	if len(in) > MaxCellOpsPerWrite {
		return nil, infraerrors.BadRequest(ReasonCellOpsTooMany,
			fmt.Sprintf("at most %d cell operations per write", MaxCellOpsPerWrite))
	}
	out := make([]CellOp, 0, len(in))
	seen := make(map[cellOpKey]struct{}, len(in))
	for _, op := range in {
		op.ModelKey = normalizeChannelPricingModelName(op.ModelKey)
		if err := normalizeCellOp(&op); err != nil {
			return nil, err
		}
		k := cellOpKey{op.GroupID, op.ModelKey}
		if _, dup := seen[k]; dup {
			return nil, infraerrors.BadRequest(ReasonCellOpDuplicate, "more than one operation on the same cell").WithMetadata(map[string]string{
				"group_id":  strconv.FormatInt(op.GroupID, 10),
				"model_key": op.ModelKey,
			})
		}
		seen[k] = struct{}{}
		out = append(out, op)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		return out[i].ModelKey < out[j].ModelKey
	})
	return out, nil
}

// normalizeCellOp 校验并规范一个操作（原地修改副本）。ModelKey 已经归一。
func normalizeCellOp(op *CellOp) error {
	switch {
	case op.GroupID <= 0:
		return cellOpError(*op, ReasonCellOpInvalid, "group_id must be positive")
	case op.ModelKey == "":
		return cellOpError(*op, ReasonCellOpInvalid, "model_key is empty")
	case len(op.ModelKey) > matrixModelKeyMaxLen:
		return cellOpError(*op, ReasonCellOpInvalid, fmt.Sprintf("model_key is longer than %d characters", matrixModelKeyMaxLen))
	case strings.Contains(op.ModelKey, "*"):
		return cellOpError(*op, ReasonCellOpInvalid, "wildcard cells are derived from channels and cannot be written")
	case op.BaselineRevision < 0:
		return cellOpError(*op, ReasonCellOpInvalid, "baseline_revision must not be negative")
	}
	switch op.Kind {
	case CellOpDelete:
		// 删除没有目标态：清空，保证计划指纹与无关字段无关。
		op.Open, op.PriceMode, op.ExtraMultiplier, op.CustomPrice, op.Source = false, "", nil, nil, ""
		return nil
	case CellOpUpsert:
		return normalizeCellTarget(op)
	default:
		return cellOpError(*op, ReasonCellOpInvalid, "unknown operation kind")
	}
}

func normalizeCellTarget(op *CellOp) error {
	switch op.Source {
	case "":
		op.Source = MatrixSourceManual
	case MatrixSourceManual, MatrixSourceCopied:
	default:
		return cellOpError(*op, ReasonCellOpInvalid, "source must be manual or copied")
	}
	if !op.Open && (op.PriceMode == MatrixPriceExtra || op.PriceMode == MatrixPriceCustom) {
		// cxw 口径：关闭的单元格不能带额外倍率或自定义价。
		return cellOpError(*op, ReasonCellOpInvalid, "a closed cell cannot carry an extra multiplier or a custom price")
	}
	switch op.PriceMode {
	case MatrixPriceInherit:
		if op.ExtraMultiplier != nil || op.CustomPrice != nil {
			return cellOpError(*op, ReasonCellOpInvalid, "an inherit cell carries neither an extra multiplier nor a custom price")
		}
	case MatrixPriceExtra:
		return normalizeCellExtra(op)
	case MatrixPriceCustom:
		return normalizeCellCustom(op)
	default:
		return cellOpError(*op, ReasonCellOpInvalid, "price_mode must be inherit, extra or custom")
	}
	return nil
}

func normalizeCellExtra(op *CellOp) error {
	if op.CustomPrice != nil || op.ExtraMultiplier == nil {
		return cellOpError(*op, ReasonCellOpInvalid, "an extra cell needs an extra multiplier and no custom price")
	}
	v := math.Round(*op.ExtraMultiplier*extraMultiplierScale) / extraMultiplierScale
	if math.IsNaN(v) || v <= 0 || v > maxCellExtraMultiplier {
		return cellOpError(*op, ReasonCellOpInvalid, fmt.Sprintf("extra_multiplier must be in (0, %g]", maxCellExtraMultiplier))
	}
	op.ExtraMultiplier = &v
	return nil
}

func normalizeCellCustom(op *CellOp) error {
	if op.ExtraMultiplier != nil || op.CustomPrice == nil {
		return cellOpError(*op, ReasonCellOpInvalid, "a custom cell needs a custom price and no extra multiplier")
	}
	cp := *op.CustomPrice
	if cp.BillingMode == "" {
		cp.BillingMode = BillingModeToken
	}
	switch cp.BillingMode {
	case BillingModeToken, BillingModePerRequest, BillingModeImage:
	default:
		return cellOpError(*op, ReasonCellOpInvalid, "unknown billing_mode in custom_price")
	}
	// 与渠道保存同一套价格校验：负价、按次缺价、区间缺价与重叠。
	pricing := []ChannelModelPricing{cp.ToChannelModelPricing("", []string{op.ModelKey})}
	if err := validatePricingBillingMode(pricing); err != nil {
		return cellOpError(*op, infraerrors.Reason(err), infraerrors.Message(err))
	}
	if err := validatePricingIntervals(pricing); err != nil {
		return cellOpError(*op, infraerrors.Reason(err), infraerrors.Message(err))
	}
	if field := customPriceLimitViolation(cp); field != "" {
		return infraerrors.BadRequest(ReasonCellOpInvalid, "custom price exceeds the allowed maximum").WithMetadata(map[string]string{
			"group_id":  strconv.FormatInt(op.GroupID, 10),
			"model_key": op.ModelKey,
			"field":     field,
			"reason":    ReasonPriceTooHigh,
		})
	}
	op.CustomPrice = &cp
	return nil
}

// CellWriteGroupIDs 返回操作涉及的分组（升序、去重）。
func CellWriteGroupIDs(ops []CellOp) []int64 {
	seen := make(map[int64]struct{}, len(ops))
	var out []int64
	for _, op := range ops {
		if _, ok := seen[op.GroupID]; ok {
			continue
		}
		seen[op.GroupID] = struct{}{}
		out = append(out, op.GroupID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CheckWritableGroups 校验所有分组都是 v2 且配置 revision 与基线一致。
// states 里没有的分组（没有 group_model_config 行或分组已删除）按非 v2 处理。
func CheckWritableGroups(groupIDs []int64, states map[int64]CellGroupState, baseline map[int64]int64) error {
	for _, g := range groupIDs {
		md := map[string]string{"group_id": strconv.FormatInt(g, 10)}
		st, ok := states[g]
		if !ok || st.Stage != PricingStageV2 {
			return infraerrors.Conflict(ReasonCellGroupNotV2, "cells are only editable for groups on the v2 pricing stage").WithMetadata(md)
		}
		if st.Revision != baseline[g] {
			return infraerrors.Conflict(ReasonPriceBaselineChanged, "the group configuration changed since the preview").WithMetadata(md)
		}
	}
	return nil
}

// PlanCellWrites 对照库里现状规划写入。ops 必须已经过 NormalizeCellWriteRequest；
// existing 是各分组的精确模型名单元格（通配符单元格不参与）。
func PlanCellWrites(ops []CellOp, existing map[int64][]StoredMatrixCell) ([]PlannedCellWrite, error) {
	index := make(map[cellOpKey]StoredMatrixCell)
	for g, cells := range existing {
		for _, c := range cells {
			if !c.IsPattern {
				index[cellOpKey{g, c.ModelKey}] = c
			}
		}
	}
	out := make([]PlannedCellWrite, 0, len(ops))
	for _, op := range ops {
		cur, ok := index[cellOpKey{op.GroupID, op.ModelKey}]
		if err := checkCellBaseline(op, cur, ok); err != nil {
			return nil, err
		}
		var before *StoredMatrixCell
		if ok {
			c := cur
			before = &c
		}
		p := PlannedCellWrite{Op: op, Before: before}
		if op.Kind == CellOpDelete {
			planCellDelete(&p)
		} else {
			planCellUpsert(&p)
		}
		out = append(out, p)
	}
	return out, nil
}

func checkCellBaseline(op CellOp, cur StoredMatrixCell, exists bool) error {
	md := map[string]string{"group_id": strconv.FormatInt(op.GroupID, 10), "model_key": op.ModelKey}
	if !exists {
		if op.BaselineRevision != 0 {
			return infraerrors.Conflict(ReasonPriceBaselineChanged, "the cell no longer exists").WithMetadata(md)
		}
		return nil
	}
	if op.BaselineRevision != cur.Revision {
		return infraerrors.Conflict(ReasonPriceBaselineChanged, "the cell changed since the preview").WithMetadata(md)
	}
	if cur.Source == MatrixSourceLegacyDerived {
		return infraerrors.Conflict(ReasonCellReadonly, "derived cells are maintained by the channel and read-only").WithMetadata(md)
	}
	if cur.EffectiveFrom != nil || cur.EffectiveTo != nil {
		// 生效时间窗要等 PR7 统一计价时刻（PricingInput.At）之后才开放写入。
		return infraerrors.Conflict(ReasonCellReadonly, "cells with an effective window are not writable yet").WithMetadata(md)
	}
	return nil
}

func planCellDelete(p *PlannedCellWrite) {
	if p.Before == nil {
		p.Action = CellWriteNoop
		return
	}
	p.Action = CellWriteDelete
	// 删除一律算涉价：单元格按字面名查找，字面名上的单元格（哪怕是 inherit）会遮住基名与通配符单元格的价，
	// 删掉它就把被遮住的价放开了（价格方向交给估算器，纯开放类的删除估出来是 none）。
	p.TouchesPrice = true
}

func planCellUpsert(p *PlannedCellWrite) {
	op := p.Op
	target := MatrixCell{
		ModelKey: op.ModelKey, Open: op.Open, PriceMode: op.PriceMode,
		ExtraMultiplier: op.ExtraMultiplier, CustomPrice: op.CustomPrice, Source: op.Source,
	}
	p.After = &target
	if p.Before == nil {
		p.Action = CellWriteCreate
		// 新建一律算涉价：在变体名上新建 inherit 单元格会遮住基名单元格的额外倍率（lookupCell 字面名优先）。
		p.TouchesPrice = true
		return
	}
	if matrixCellContent(p.Before.MatrixCell) == matrixCellContent(target) {
		p.Action = CellWriteNoop
		return
	}
	p.Action = CellWriteUpdate
	p.TouchesPrice = cellPriceFingerprint(p.Before.PriceMode, p.Before.ExtraMultiplier, p.Before.CustomPrice) !=
		cellPriceFingerprint(op.PriceMode, op.ExtraMultiplier, op.CustomPrice)
}

// cellPriceFingerprint 价格字段的规范串；open、source 不在内。
func cellPriceFingerprint(mode MatrixPriceMode, extra *float64, custom *MatrixCustomPrice) string {
	return matrixCanonicalJSON(struct {
		Mode   MatrixPriceMode    `json:"m"`
		Extra  *float64           `json:"e,omitempty"`
		Custom *MatrixCustomPrice `json:"c,omitempty"`
	}{mode, extra, custom})
}

// PlannedTouchesPrice 规划里是否有涉价的写入。
func PlannedTouchesPrice(planned []PlannedCellWrite) bool {
	for _, p := range planned {
		if p.TouchesPrice {
			return true
		}
	}
	return false
}

// PriceWritePlanHash 计划指纹：操作（目标态加单元格基线）与分组基线的 SHA-256。
// req 必须已经过 NormalizeCellWriteRequest（排序与归一后同一份写入得到同一个指纹）。
func PriceWritePlanHash(req CellWriteRequest) string {
	sum := sha256.Sum256([]byte(matrixCanonicalJSON(struct {
		Ops            []CellOp        `json:"ops"`
		GroupRevisions map[int64]int64 `json:"group_revisions"`
	}{req.Ops, req.GroupRevisions})))
	return hex.EncodeToString(sum[:])
}
