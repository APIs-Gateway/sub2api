//go:build unit

package service

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-1：单元格写入的校验、规划与计划指纹。

func pwF(v float64) *float64 { return &v }

func pwUpsert(group int64, key string, open bool, mode MatrixPriceMode) CellOp {
	return CellOp{GroupID: group, ModelKey: key, Kind: CellOpUpsert, Open: open, PriceMode: mode}
}

func pwStored(group int64, key string, rev int64, cell MatrixCell) StoredMatrixCell {
	cell.ModelKey = key
	return StoredMatrixCell{ID: group*100 + rev, GroupID: group, Revision: rev, UpdatedAt: time.Unix(1, 0), MatrixCell: cell}
}

func pwReason(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	var ae *infraerrors.ApplicationError
	require.True(t, errors.As(err, &ae), "expected an application error, got %v", err)
	return ae.Reason
}

func TestNormalizeCellWriteRequest_RejectsInvalidOps(t *testing.T) {
	longKey := strings.Repeat("a", matrixModelKeyMaxLen+1)
	custom := func(p MatrixCustomPrice) *MatrixCustomPrice { return &p }
	cases := []struct {
		name   string
		mutate func(op *CellOp)
		reason string
	}{
		{"group id", func(op *CellOp) { op.GroupID = 0 }, ReasonCellOpInvalid},
		{"empty key", func(op *CellOp) { op.ModelKey = "  " }, ReasonCellOpInvalid},
		{"long key", func(op *CellOp) { op.ModelKey = longKey }, ReasonCellOpInvalid},
		{"wildcard key", func(op *CellOp) { op.ModelKey = "gpt-5*" }, ReasonCellOpInvalid},
		{"negative baseline", func(op *CellOp) { op.BaselineRevision = -1 }, ReasonCellOpInvalid},
		{"unknown kind", func(op *CellOp) { op.Kind = "merge" }, ReasonCellOpInvalid},
		{"bad source", func(op *CellOp) { op.Source = MatrixSourceLegacyDerived }, ReasonCellOpInvalid},
		{"unknown mode", func(op *CellOp) { op.PriceMode = "bonus" }, ReasonCellOpInvalid},
		{"empty mode", func(op *CellOp) { op.PriceMode = "" }, ReasonCellOpInvalid},
		{"inherit with extra", func(op *CellOp) { op.ExtraMultiplier = pwF(1.5) }, ReasonCellOpInvalid},
		{"inherit with custom", func(op *CellOp) { op.CustomPrice = custom(MatrixCustomPrice{}) }, ReasonCellOpInvalid},
		{"extra missing", func(op *CellOp) { op.PriceMode = MatrixPriceExtra }, ReasonCellOpInvalid},
		{"extra with custom", func(op *CellOp) {
			op.PriceMode, op.ExtraMultiplier, op.CustomPrice = MatrixPriceExtra, pwF(1.5), custom(MatrixCustomPrice{})
		}, ReasonCellOpInvalid},
		{"extra zero", func(op *CellOp) { op.PriceMode, op.ExtraMultiplier = MatrixPriceExtra, pwF(0) }, ReasonCellOpInvalid},
		{"extra rounds to zero", func(op *CellOp) { op.PriceMode, op.ExtraMultiplier = MatrixPriceExtra, pwF(0.0000001) }, ReasonCellOpInvalid},
		{"extra too large", func(op *CellOp) { op.PriceMode, op.ExtraMultiplier = MatrixPriceExtra, pwF(1000.5) }, ReasonCellOpInvalid},
		{"extra nan", func(op *CellOp) { op.PriceMode, op.ExtraMultiplier = MatrixPriceExtra, pwF(math.NaN()) }, ReasonCellOpInvalid},
		{"closed with extra", func(op *CellOp) {
			op.Open, op.PriceMode, op.ExtraMultiplier = false, MatrixPriceExtra, pwF(1.5)
		}, ReasonCellOpInvalid},
		{"closed with custom", func(op *CellOp) {
			op.Open, op.PriceMode, op.CustomPrice = false, MatrixPriceCustom, custom(MatrixCustomPrice{InputPrice: pwF(1)})
		}, ReasonCellOpInvalid},
		{"custom missing", func(op *CellOp) { op.PriceMode = MatrixPriceCustom }, ReasonCellOpInvalid},
		{"custom with extra", func(op *CellOp) {
			op.PriceMode, op.ExtraMultiplier, op.CustomPrice = MatrixPriceCustom, pwF(1.5), custom(MatrixCustomPrice{})
		}, ReasonCellOpInvalid},
		{"custom unknown billing mode", func(op *CellOp) {
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{BillingMode: "weird"})
		}, ReasonCellOpInvalid},
		{"custom negative price", func(op *CellOp) {
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{InputPrice: pwF(-1)})
		}, "NEGATIVE_PRICE"},
		{"custom per request without price", func(op *CellOp) {
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{BillingMode: BillingModePerRequest})
		}, "BILLING_MODE_MISSING_PRICE"},
		{"custom overlapping intervals", func(op *CellOp) {
			two := 2000
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{
				BillingMode: BillingModeToken,
				Intervals: []MatrixPriceInterval{
					{MinTokens: 0, MaxTokens: &two, InputPrice: pwF(1)},
					{MinTokens: 1000, InputPrice: pwF(2)},
				},
			})
		}, "INVALID_PRICING_INTERVALS"},
		{"custom token price over cap", func(op *CellOp) {
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{InputPrice: pwF(1.25)})
		}, ReasonCellOpInvalid},
		{"custom cache price over cap", func(op *CellOp) {
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{InputPrice: pwF(1e-6), CacheReadPrice: pwF(0.02)})
		}, ReasonCellOpInvalid},
		{"custom per request over cap", func(op *CellOp) {
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{BillingMode: BillingModePerRequest, PerRequestPrice: pwF(1000.5)})
		}, ReasonCellOpInvalid},
		{"custom interval price over cap", func(op *CellOp) {
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{
				Intervals: []MatrixPriceInterval{{MinTokens: 0, OutputPrice: pwF(5)}},
			})
		}, ReasonCellOpInvalid},
		{"custom price infinite", func(op *CellOp) {
			op.PriceMode, op.CustomPrice = MatrixPriceCustom, custom(MatrixCustomPrice{InputPrice: pwF(math.Inf(1))})
		}, ReasonCellOpInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := pwUpsert(1, "gpt-5.5", true, MatrixPriceInherit)
			tc.mutate(&op)
			_, err := NormalizeCellWriteRequest(CellWriteRequest{Ops: []CellOp{op}, GroupRevisions: map[int64]int64{1: 1}})
			require.Equal(t, tc.reason, pwReason(t, err))
		})
	}
}

func TestNormalizeCellWriteRequest_CustomPriceCap(t *testing.T) {
	thousand := 1000
	norm := func(cp MatrixCustomPrice) error {
		op := pwUpsert(1, "gpt-5.5", true, MatrixPriceCustom)
		op.CustomPrice = &cp
		_, err := NormalizeCellWriteRequest(CellWriteRequest{Ops: []CellOp{op}, GroupRevisions: map[int64]int64{1: 1}})
		return err
	}
	// 恰好等于上限放行。
	require.NoError(t, norm(MatrixCustomPrice{InputPrice: pwF(MaxCustomTokenPrice), OutputPrice: pwF(MaxCustomTokenPrice)}))
	require.NoError(t, norm(MatrixCustomPrice{BillingMode: BillingModePerRequest, PerRequestPrice: pwF(MaxCustomPerRequestPrice)}))
	require.NoError(t, norm(MatrixCustomPrice{BillingMode: BillingModeImage, PerRequestPrice: pwF(0.04)}))

	// 超限：reason 是 CELL_OP_INVALID，metadata 带 field 与 reason。
	for name, tc := range map[string]struct {
		cp    MatrixCustomPrice
		field string
	}{
		"input":     {MatrixCustomPrice{InputPrice: pwF(0.011)}, "input_price"},
		"output":    {MatrixCustomPrice{OutputPrice: pwF(1)}, "output_price"},
		"cache w":   {MatrixCustomPrice{CacheWritePrice: pwF(1)}, "cache_write_price"},
		"image out": {MatrixCustomPrice{BillingMode: BillingModeImage, PerRequestPrice: pwF(0.04), ImageOutputPrice: pwF(5)}, "image_output_price"},
		"per req":   {MatrixCustomPrice{BillingMode: BillingModePerRequest, PerRequestPrice: pwF(1001)}, "per_request_price"},
		"interval":  {MatrixCustomPrice{Intervals: []MatrixPriceInterval{{MinTokens: 0, MaxTokens: &thousand, InputPrice: pwF(1e-6)}, {MinTokens: 1000, InputPrice: pwF(3)}}}, "intervals[1].input_price"},
	} {
		err := norm(tc.cp)
		require.Equal(t, ReasonCellOpInvalid, pwReason(t, err), name)
		md := infraerrors.FromError(err).Metadata
		require.Equal(t, tc.field, md["field"], name)
		require.Equal(t, ReasonPriceTooHigh, md["reason"], name)
		require.Equal(t, "1", md["group_id"], name)
		require.Equal(t, "gpt-5.5", md["model_key"], name)
	}
}

func TestNormalizeCellWriteRequest_RejectsBadBatches(t *testing.T) {
	ok := pwUpsert(1, "gpt-5.5", true, MatrixPriceInherit)
	rev := map[int64]int64{1: 3}

	_, err := NormalizeCellWriteRequest(CellWriteRequest{GroupRevisions: rev})
	require.Equal(t, ReasonCellOpsEmpty, pwReason(t, err))

	many := make([]CellOp, MaxCellOpsPerWrite+1)
	for i := range many {
		many[i] = pwUpsert(1, fmt.Sprintf("model-%d", i), true, MatrixPriceInherit)
	}
	_, err = NormalizeCellWriteRequest(CellWriteRequest{Ops: many, GroupRevisions: rev})
	require.Equal(t, ReasonCellOpsTooMany, pwReason(t, err))

	// 归一后同键：大小写与 claude 的点号都算同一个单元格。
	_, err = NormalizeCellWriteRequest(CellWriteRequest{
		Ops:            []CellOp{pwUpsert(1, "Claude-Opus-4.5", true, MatrixPriceInherit), pwUpsert(1, "claude-opus-4-5", true, MatrixPriceInherit)},
		GroupRevisions: rev,
	})
	require.Equal(t, ReasonCellOpDuplicate, pwReason(t, err))

	// 分组基线必须正好覆盖涉及的分组。
	for name, revs := range map[string]map[int64]int64{
		"missing":  nil,
		"zero":     {1: 0},
		"extra":    {1: 3, 2: 1},
		"mismatch": {2: 1},
	} {
		_, err = NormalizeCellWriteRequest(CellWriteRequest{Ops: []CellOp{ok}, GroupRevisions: revs})
		require.Equal(t, ReasonCellGroupBaselineMissing, pwReason(t, err), name)
	}
}

func TestNormalizeCellWriteRequest_NormalizesAndSorts(t *testing.T) {
	extra := pwUpsert(2, " GPT-5.5 ", true, MatrixPriceExtra)
	extra.ExtraMultiplier = pwF(1.23456789)
	custom := pwUpsert(1, "gpt-4o", true, MatrixPriceCustom)
	custom.CustomPrice = &MatrixCustomPrice{InputPrice: pwF(0.000002)}
	custom.Source = MatrixSourceCopied
	del := CellOp{GroupID: 1, ModelKey: "claude-opus-4.5", Kind: CellOpDelete, Open: true, PriceMode: MatrixPriceExtra,
		ExtraMultiplier: pwF(9), Source: MatrixSourceCopied, BaselineRevision: 4}

	req, err := NormalizeCellWriteRequest(CellWriteRequest{
		Ops:            []CellOp{extra, custom, del},
		GroupRevisions: map[int64]int64{1: 7, 2: 9},
		OperatorID:     5, ApprovalID: 6, ChangeSetID: 8,
	})
	require.NoError(t, err)
	require.Equal(t, int64(5), req.OperatorID)
	require.Equal(t, int64(6), req.ApprovalID)
	require.Equal(t, int64(8), req.ChangeSetID)
	require.Len(t, req.Ops, 3)

	// 排序：分组升序、同组按归一后的模型名。
	require.Equal(t, "claude-opus-4-5", req.Ops[0].ModelKey)
	require.Equal(t, "gpt-4o", req.Ops[1].ModelKey)
	require.Equal(t, "gpt-5.5", req.Ops[2].ModelKey)
	require.Equal(t, []int64{1, 2}, CellWriteGroupIDs(req.Ops))

	// 删除没有目标态：无关字段被清空。
	require.Equal(t, CellOp{GroupID: 1, ModelKey: "claude-opus-4-5", Kind: CellOpDelete, BaselineRevision: 4}, req.Ops[0])
	// custom：billing_mode 默认 token，来源保留 copied。
	require.Equal(t, BillingModeToken, req.Ops[1].CustomPrice.BillingMode)
	require.Equal(t, MatrixSourceCopied, req.Ops[1].Source)
	// extra：保留 6 位小数，来源默认 manual。
	require.Equal(t, 1.234568, *req.Ops[2].ExtraMultiplier)
	require.Equal(t, MatrixSourceManual, req.Ops[2].Source)
	// 不改调用方传入的价格对象。
	require.Equal(t, BillingMode(""), custom.CustomPrice.BillingMode)

	// 规范化是幂等的，计划指纹因此稳定。
	again, err := NormalizeCellWriteRequest(req)
	require.NoError(t, err)
	require.Equal(t, req, again)
	require.Equal(t, PriceWritePlanHash(req), PriceWritePlanHash(again))
}

func TestPriceWritePlanHash_BindsOpsAndBaselinesButNotAudit(t *testing.T) {
	base := func() CellWriteRequest {
		return CellWriteRequest{
			Ops:            []CellOp{pwUpsert(1, "gpt-5.5", true, MatrixPriceInherit), pwUpsert(2, "gpt-4o", false, MatrixPriceInherit)},
			GroupRevisions: map[int64]int64{1: 3, 2: 4},
		}
	}
	norm := func(r CellWriteRequest) CellWriteRequest {
		out, err := NormalizeCellWriteRequest(r)
		require.NoError(t, err)
		return out
	}
	want := PriceWritePlanHash(norm(base()))
	require.Len(t, want, 64)

	// 操作顺序与审计字段不影响指纹。
	reordered := base()
	reordered.Ops[0], reordered.Ops[1] = reordered.Ops[1], reordered.Ops[0]
	reordered.OperatorID, reordered.ApprovalID, reordered.ChangeSetID = 9, 9, 9
	require.Equal(t, want, PriceWritePlanHash(norm(reordered)))

	changes := map[string]func(r *CellWriteRequest){
		"target":         func(r *CellWriteRequest) { r.Ops[0].Open = false },
		"cell baseline":  func(r *CellWriteRequest) { r.Ops[0].BaselineRevision = 5 },
		"group baseline": func(r *CellWriteRequest) { r.GroupRevisions[1] = 4 },
		"other cell":     func(r *CellWriteRequest) { r.Ops[1].ModelKey = "gpt-4o-mini" },
		"kind":           func(r *CellWriteRequest) { r.Ops[1] = CellOp{GroupID: 2, ModelKey: "gpt-4o", Kind: CellOpDelete} },
	}
	for name, mutate := range changes {
		r := base()
		mutate(&r)
		require.NotEqual(t, want, PriceWritePlanHash(norm(r)), name)
	}
}

func TestCheckWritableGroups(t *testing.T) {
	v2 := CellGroupState{Stage: PricingStageV2, Revision: 3}
	require.NoError(t, CheckWritableGroups([]int64{1, 2}, map[int64]CellGroupState{1: v2, 2: v2}, map[int64]int64{1: 3, 2: 3}))

	require.Equal(t, ReasonCellGroupNotV2, pwReason(t, CheckWritableGroups([]int64{1}, map[int64]CellGroupState{}, map[int64]int64{1: 3})), "没有配置行（legacy 默认值）")
	for _, stage := range []PricingStage{PricingStageLegacy, PricingStageShadow} {
		err := CheckWritableGroups([]int64{1}, map[int64]CellGroupState{1: {Stage: stage, Revision: 3}}, map[int64]int64{1: 3})
		require.Equal(t, ReasonCellGroupNotV2, pwReason(t, err), string(stage))
	}
	err := CheckWritableGroups([]int64{1}, map[int64]CellGroupState{1: v2}, map[int64]int64{1: 2})
	require.Equal(t, ReasonPriceBaselineChanged, pwReason(t, err))
	var ae *infraerrors.ApplicationError
	require.True(t, errors.As(err, &ae))
	require.Equal(t, "1", ae.Metadata["group_id"])
}

func pwPlan(t *testing.T, ops []CellOp, existing map[int64][]StoredMatrixCell) ([]PlannedCellWrite, error) {
	t.Helper()
	groups := map[int64]int64{}
	for _, op := range ops {
		groups[op.GroupID] = 1
	}
	req, err := NormalizeCellWriteRequest(CellWriteRequest{Ops: ops, GroupRevisions: groups})
	require.NoError(t, err)
	return PlanCellWrites(req.Ops, existing)
}

func TestPlanCellWrites_ActionsAndPriceTouch(t *testing.T) {
	customCell := pwUpsert(1, "gpt-custom", true, MatrixPriceCustom)
	customCell.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: pwF(0.000002)}

	existing := map[int64][]StoredMatrixCell{1: {
		pwStored(1, "same", 4, MatrixCell{Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual}),
		pwStored(1, "open-flip", 2, MatrixCell{Open: false, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyFrozen}),
		pwStored(1, "to-extra", 1, MatrixCell{Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual}),
		pwStored(1, "had-custom", 6, MatrixCell{Open: true, PriceMode: MatrixPriceCustom, Source: MatrixSourceManual,
			CustomPrice: &MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: pwF(1)}}),
		pwStored(1, "del-inherit", 3, MatrixCell{Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual}),
		pwStored(1, "del-extra", 5, MatrixCell{Open: true, PriceMode: MatrixPriceExtra, ExtraMultiplier: pwF(2), Source: MatrixSourceManual}),
		{ID: 999, GroupID: 1, Revision: 1, MatrixCell: MatrixCell{ModelKey: "gpt-new", IsPattern: true, Open: true, PriceMode: MatrixPriceInherit}},
	}}
	toExtra := pwUpsert(1, "to-extra", true, MatrixPriceExtra)
	toExtra.ExtraMultiplier, toExtra.BaselineRevision = pwF(1.5), 1
	sameOp := pwUpsert(1, "same", true, MatrixPriceInherit)
	sameOp.BaselineRevision = 4
	flip := pwUpsert(1, "open-flip", true, MatrixPriceInherit)
	flip.BaselineRevision = 2
	toInherit := pwUpsert(1, "had-custom", false, MatrixPriceInherit)
	toInherit.BaselineRevision = 6
	newInherit := pwUpsert(1, "gpt-new", true, MatrixPriceInherit) // 只有通配符单元格同名：精确单元格仍不存在
	delInherit := CellOp{GroupID: 1, ModelKey: "del-inherit", Kind: CellOpDelete, BaselineRevision: 3}
	delExtra := CellOp{GroupID: 1, ModelKey: "del-extra", Kind: CellOpDelete, BaselineRevision: 5}
	delGone := CellOp{GroupID: 1, ModelKey: "never-existed", Kind: CellOpDelete}

	planned, err := pwPlan(t, []CellOp{toExtra, sameOp, flip, toInherit, newInherit, customCell, delInherit, delExtra, delGone}, existing)
	require.NoError(t, err)

	got := map[string]PlannedCellWrite{}
	for _, p := range planned {
		got[p.Op.ModelKey] = p
	}
	type want struct {
		action  CellWriteAction
		touches bool
	}
	for key, w := range map[string]want{
		"to-extra":      {CellWriteUpdate, true},
		"same":          {CellWriteNoop, false},
		"open-flip":     {CellWriteUpdate, false},
		"had-custom":    {CellWriteUpdate, true},
		"gpt-new":       {CellWriteCreate, true},
		"gpt-custom":    {CellWriteCreate, true},
		"del-inherit":   {CellWriteDelete, true},
		"del-extra":     {CellWriteDelete, true},
		"never-existed": {CellWriteNoop, false},
	} {
		require.Equal(t, w.action, got[key].Action, key)
		require.Equal(t, w.touches, got[key].TouchesPrice, key)
	}
	require.True(t, PlannedTouchesPrice(planned))
	require.Nil(t, got["gpt-new"].Before, "通配符单元格不参与精确单元格的查找")
	require.NotNil(t, got["to-extra"].Before)
	require.Equal(t, MatrixPriceInherit, got["to-extra"].Before.PriceMode)
	require.Equal(t, 1.5, *got["to-extra"].After.ExtraMultiplier)
	require.Nil(t, got["del-extra"].After)

	noTouch := []PlannedCellWrite{got["same"], got["open-flip"], got["never-existed"]}
	require.False(t, PlannedTouchesPrice(noTouch))
	require.False(t, PlannedTouchesPrice(nil))
}

func TestPlanCellWrites_RejectsStaleOrReadonlyCells(t *testing.T) {
	now := time.Unix(100, 0)
	existing := map[int64][]StoredMatrixCell{1: {
		pwStored(1, "m", 4, MatrixCell{Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual}),
		pwStored(1, "derived", 2, MatrixCell{Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived}),
		func() StoredMatrixCell {
			c := pwStored(1, "windowed", 1, MatrixCell{Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual})
			c.EffectiveFrom = &now
			return c
		}(),
		func() StoredMatrixCell {
			c := pwStored(1, "windowed-to", 1, MatrixCell{Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual})
			c.EffectiveTo = &now
			return c
		}(),
	}}
	op := func(key string, rev int64) CellOp {
		o := pwUpsert(1, key, false, MatrixPriceInherit)
		o.BaselineRevision = rev
		return o
	}
	cases := []struct {
		name   string
		op     CellOp
		reason string
	}{
		{"revision moved", op("m", 3), ReasonPriceBaselineChanged},
		{"expected missing but exists", op("m", 0), ReasonPriceBaselineChanged},
		{"expected present but gone", op("gone", 2), ReasonPriceBaselineChanged},
		{"derived cell", op("derived", 2), ReasonCellReadonly},
		{"effective from", op("windowed", 1), ReasonCellReadonly},
		{"effective to", op("windowed-to", 1), ReasonCellReadonly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pwPlan(t, []CellOp{tc.op}, existing)
			require.Equal(t, tc.reason, pwReason(t, err))
		})
	}
}

func TestClassifyApprovalRejection(t *testing.T) {
	now := time.Unix(1000, 0)
	future, past := now.Add(time.Minute), now.Add(-time.Minute)
	cases := []struct {
		name                  string
		found                 bool
		status, hash, kind    string
		expires               time.Time
		wantHash, wantKind    string
		reason                string
		code                  int
		expectedErrorContains string
	}{
		{"missing", false, "", "", "", time.Time{}, "h", PriceWriteKindCells, ReasonApprovalNotFound, 404, "not found"},
		{"consumed", true, "consumed", "h", PriceWriteKindCells, future, "h", PriceWriteKindCells, ReasonApprovalConsumed, 409, "already used"},
		{"expired", true, "previewed", "h", PriceWriteKindCells, past, "h", PriceWriteKindCells, ReasonApprovalExpired, 409, "expired"},
		{"expired exactly now", true, "previewed", "h", PriceWriteKindCells, now, "h", PriceWriteKindCells, ReasonApprovalExpired, 409, "expired"},
		{"hash mismatch", true, "previewed", "other", PriceWriteKindCells, future, "h", PriceWriteKindCells, ReasonApprovalMismatch, 409, "differs"},
		{"kind mismatch", true, "previewed", "h", "other", future, "h", PriceWriteKindCells, ReasonApprovalMismatch, 409, "differs"},
		{"raced", true, "previewed", "h", PriceWriteKindCells, future, "h", PriceWriteKindCells, ReasonApprovalConsumed, 409, "already used"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ClassifyApprovalRejection(tc.found, tc.status, tc.hash, tc.kind, tc.expires, tc.wantHash, tc.wantKind, now)
			require.Equal(t, tc.reason, pwReason(t, err))
			require.Equal(t, tc.code, infraerrors.Code(err))
			require.Contains(t, infraerrors.Message(err), tc.expectedErrorContains)
		})
	}
}
