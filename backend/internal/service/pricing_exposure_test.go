//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-2a：ExposureValidator / ExposureGuard（保存时校验，设计 5.2 与 S-5）。

type exFakePrices map[string]OfficialPriceState

func (p exFakePrices) LookupOfficialPriceState(model string) OfficialPriceState { return p[model] }

// exFakeSettings 只实现 GetValue，其余方法留给内嵌的 nil 接口（调用就 panic，说明代码走了不该走的路）。
type exFakeSettings struct {
	SettingRepository
	value string
	err   error
}

func (s exFakeSettings) GetValue(_ context.Context, key string) (string, error) {
	if key != SettingKeyBillingKnownFreeList {
		panic("unexpected setting key " + key)
	}
	return s.value, s.err
}

type exFakeReader struct {
	modes     map[int64]MatrixAccessMode
	cells     []ExposureCell
	modesErr  error
	cellsErr  error
	modeCalls [][]int64
	cellCalls [][]int64
}

func (r *exFakeReader) AccessModesTx(_ context.Context, _ MatrixExecutor, ids []int64) (map[int64]MatrixAccessMode, error) {
	r.modeCalls = append(r.modeCalls, ids)
	return r.modes, r.modesErr
}

func (r *exFakeReader) OpenCellsTx(_ context.Context, _ MatrixExecutor, ids []int64) ([]ExposureCell, error) {
	r.cellCalls = append(r.cellCalls, ids)
	return r.cells, r.cellsErr
}

func exCell(group int64, key string, mode MatrixPriceMode) ExposureCell {
	return ExposureCell{GroupID: group, Cell: MatrixCell{ModelKey: key, Open: true, PriceMode: mode}}
}

func exCustom(group int64, key string, price MatrixCustomPrice) ExposureCell {
	c := exCell(group, key, MatrixPriceCustom)
	c.Cell.CustomPrice = &price
	return c
}

var exOfficial = exFakePrices{
	"priced": {Known: true, TokenNonZero: true},
	"zero":   {Known: true},
	// 官方只有按张价、token 价全 0 的图片模型。
	"img-only": {Known: true, ImageCapable: true},
}

func TestExposureValidator_Check(t *testing.T) {
	ctx := context.Background()
	extra := exCell(1, "priced", MatrixPriceExtra)
	extra.Cell.ExtraMultiplier = pwF(1.5)
	closed := exCell(1, "unknown-closed", MatrixPriceInherit)
	closed.Cell.Open = false
	pattern := exCell(1, "claude-*", MatrixPriceInherit)
	pattern.Cell.IsPattern = true

	cases := []struct {
		name string
		cell ExposureCell
		want ExposureViolationReason // 空串表示通过
	}{
		{"inherit with an official price", exCell(1, "priced", MatrixPriceInherit), ""},
		{"inherit without any price", exCell(1, "nothing", MatrixPriceInherit), ExposureUnpriced},
		{"inherit with an all-zero official price", exCell(1, "zero", MatrixPriceInherit), ExposureZeroPrice},
		{"extra with an official price", extra, ""},
		{"extra without any price", exCell(1, "nothing", MatrixPriceExtra), ExposureUnpriced},
		{"custom with a positive price", exCustom(1, "nothing", MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: pwF(1e-6)}), ""},
		{"custom with a positive interval price", exCustom(1, "nothing", MatrixCustomPrice{BillingMode: BillingModeToken,
			Intervals: []MatrixPriceInterval{{MinTokens: 0, OutputPrice: pwF(2e-6)}}}), ""},
		{"custom with a per-request price", exCustom(1, "nothing", MatrixCustomPrice{BillingMode: BillingModePerRequest, PerRequestPrice: pwF(0.04)}), ""},
		{"empty custom falls back to the official price", exCustom(1, "priced", MatrixCustomPrice{BillingMode: BillingModeToken}), ""},
		{"empty custom without an official price", exCustom(1, "nothing", MatrixCustomPrice{BillingMode: BillingModeToken}), ExposureUnpriced},
		{"explicit zero custom is not covered by the official price", exCustom(1, "priced", MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: pwF(0)}), ExposureZeroPrice},
		{"explicit zero interval price", exCustom(1, "priced", MatrixCustomPrice{BillingMode: BillingModeToken,
			Intervals: []MatrixPriceInterval{{MinTokens: 0, InputPrice: pwF(0)}}}), ExposureZeroPrice},
		// B1：按计费模式分别判定。
		{"per_request with a zero price and a token price is free", exCustom(1, "nothing", MatrixCustomPrice{BillingMode: BillingModePerRequest,
			PerRequestPrice: pwF(0), InputPrice: pwF(1e-6)}), ExposureZeroPrice},
		{"image mode with only token-style interval prices is free", exCustom(1, "nothing", MatrixCustomPrice{BillingMode: BillingModeImage,
			Intervals: []MatrixPriceInterval{{TierLabel: "1K", OutputPrice: pwF(1e-5)}}}), ExposureZeroPrice},
		{"empty per_request does not fall back to the official price", exCustom(1, "priced", MatrixCustomPrice{BillingMode: BillingModePerRequest}), ExposureZeroPrice},
		{"token mode with only a per-request price and no official price", exCustom(1, "nothing", MatrixCustomPrice{BillingMode: BillingModeToken,
			PerRequestPrice: pwF(0.04)}), ExposureUnpriced},
		{"empty token custom on an image-only model is free", exCustom(1, "img-only", MatrixCustomPrice{BillingMode: BillingModeToken}), ExposureZeroPrice},
		{"per_request with a zero price but a positive interval price", exCustom(1, "nothing", MatrixCustomPrice{BillingMode: BillingModePerRequest,
			PerRequestPrice: pwF(0), Intervals: []MatrixPriceInterval{{TierLabel: "1K", PerRequestPrice: pwF(0.04)}}}), ""},
		{"inherit on an image-only model has a price", exCell(1, "img-only", MatrixPriceInherit), ""},
		{"closed cells are not checked", closed, ""},
		{"wildcard cells are not checked", pattern, ""},
	}
	v := NewExposureValidator(exOfficial, nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := v.Check(ctx, []ExposureCell{tc.cell})
			if tc.want == "" {
				require.Empty(t, got)
				return
			}
			require.Equal(t, []ExposureViolation{{GroupID: tc.cell.GroupID, ModelKey: tc.cell.Cell.ModelKey, Reason: tc.want}}, got)
		})
	}
}

func TestExposureValidator_KnownFreeListAndFailClosed(t *testing.T) {
	ctx := context.Background()
	cells := []ExposureCell{exCell(1, "zero", MatrixPriceInherit), exCell(2, "zero", MatrixPriceInherit), exCell(1, "nothing", MatrixPriceInherit)}

	// 名单放行的是（分组、模型）：group 1 的 zero 放行，group 2 的不放行；没有官方价的 nothing 也可以被名单放行。
	list := `[{"group_id":1,"model":"ZERO"},{"model":"nothing","group_id":0}]`
	got := NewExposureValidator(exOfficial, exFakeSettings{value: list}).Check(ctx, cells)
	require.Equal(t, []ExposureViolation{{GroupID: 2, ModelKey: "zero", Reason: ExposureZeroPrice}}, got)

	// 名单写坏：整份作废（只会多拦，不会少拦）。
	got = NewExposureValidator(exOfficial, exFakeSettings{value: `[{"group_id":1,"modle":"zero"}]`}).Check(ctx, cells[:1])
	require.Len(t, got, 1)
	got = NewExposureValidator(exOfficial, exFakeSettings{err: errors.New("db down")}).Check(ctx, cells[:1])
	require.Len(t, got, 1)

	// 没有官方价来源：一律按没有价处理。
	got = NewExposureValidator(nil, nil).Check(ctx, []ExposureCell{exCell(1, "priced", MatrixPriceInherit)})
	require.Equal(t, []ExposureViolation{{GroupID: 1, ModelKey: "priced", Reason: ExposureUnpriced}}, got)

	require.Empty(t, NewExposureValidator(nil, nil).Check(ctx, nil))
}

func TestExposureValidator_ResultIsSorted(t *testing.T) {
	got := NewExposureValidator(nil, nil).Check(context.Background(), []ExposureCell{
		exCell(2, "b", MatrixPriceInherit), exCell(1, "z", MatrixPriceInherit), exCell(1, "a", MatrixPriceInherit),
	})
	require.Equal(t, []string{"1/a", "1/z", "2/b"}, []string{
		"1/" + got[0].ModelKey, "1/" + got[1].ModelKey, "2/" + got[2].ModelKey,
	})
	require.Equal(t, int64(2), got[2].GroupID)
}

func exPlanned(group int64, key string, action CellWriteAction, open bool) PlannedCellWrite {
	p := PlannedCellWrite{Op: CellOp{GroupID: group, ModelKey: key}, Action: action}
	if action != CellWriteDelete && action != CellWriteNoop {
		p.After = &MatrixCell{ModelKey: key, Open: open, PriceMode: MatrixPriceInherit}
	}
	return p
}

func exGuard(r *exFakeReader) *ExposureGuard {
	return NewExposureGuard(r, NewExposureValidator(exOfficial, nil))
}

func TestExposureGuard_CheckCellWrites(t *testing.T) {
	ctx := context.Background()

	// 白名单分组里开放一个无价模型：阻止，错误里带违规清单。
	r := &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist, 2: MatrixAccessOpen}}
	err := exGuard(r).CheckCellWrites(ctx, nil, []PlannedCellWrite{
		exPlanned(1, "nothing", CellWriteCreate, true),
		exPlanned(1, "priced", CellWriteUpdate, true),
		exPlanned(2, "nothing", CellWriteCreate, true), // 开放分组不阻止
	})
	require.Equal(t, ReasonExposureUnpriced, pwReason(t, err))
	md := infraerrors.FromError(err).Metadata
	require.Equal(t, "1", md["count"])
	require.Equal(t, "1:nothing:unpriced", md["violations"])
	require.Equal(t, [][]int64{{1, 2}}, r.modeCalls)

	// 删除、noop、关闭的单元格不查，也不读库。
	r = &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}}
	require.NoError(t, exGuard(r).CheckCellWrites(ctx, nil, []PlannedCellWrite{
		exPlanned(1, "nothing", CellWriteDelete, false),
		exPlanned(1, "nothing", CellWriteNoop, true),
		exPlanned(1, "nothing", CellWriteUpdate, false),
	}))
	require.Empty(t, r.modeCalls)

	// 读准入模式失败：错误原样返回。
	r = &exFakeReader{modesErr: errors.New("db down")}
	require.EqualError(t, exGuard(r).CheckCellWrites(ctx, nil, []PlannedCellWrite{exPlanned(1, "priced", CellWriteCreate, true)}), "db down")

	// 没有配置关口：失败关闭。
	var nilGuard *ExposureGuard
	require.Equal(t, ReasonExposureGuardMissing, pwReason(t, nilGuard.CheckCellWrites(ctx, nil, nil)))
	require.Equal(t, ReasonExposureGuardMissing, pwReason(t, nilGuard.CheckGroups(ctx, nil, []int64{1})))
	require.Equal(t, ReasonExposureGuardMissing, pwReason(t, NewExposureGuard(nil, nil).CheckCellWrites(ctx, nil, nil)))
}

func TestExposureGuard_ListsAtMostTwentyViolations(t *testing.T) {
	r := &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}}
	var planned []PlannedCellWrite
	for i := 0; i < 25; i++ {
		planned = append(planned, exPlanned(1, string(rune('a'+i)), CellWriteCreate, true))
	}
	err := exGuard(r).CheckCellWrites(context.Background(), nil, planned)
	md := infraerrors.FromError(err).Metadata
	require.Equal(t, "25", md["count"])
	require.Len(t, splitNonEmpty(md["violations"], ';'), 20)
}

func splitNonEmpty(s string, sep rune) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == sep {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestExposureGuard_CheckGroups(t *testing.T) {
	ctx := context.Background()

	// 只对白名单分组读单元格。
	r := &exFakeReader{
		modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist, 2: MatrixAccessOpen},
		cells: []ExposureCell{exCell(1, "priced", MatrixPriceInherit), exCell(1, "nothing", MatrixPriceInherit)},
	}
	err := exGuard(r).CheckGroups(ctx, nil, []int64{1, 2})
	require.Equal(t, ReasonExposureUnpriced, pwReason(t, err))
	require.Equal(t, [][]int64{{1}}, r.cellCalls)

	// 没有白名单分组：不读单元格。
	r = &exFakeReader{modes: map[int64]MatrixAccessMode{2: MatrixAccessOpen}}
	require.NoError(t, exGuard(r).CheckGroups(ctx, nil, []int64{2, 3}))
	require.Empty(t, r.cellCalls)
	require.NoError(t, exGuard(r).CheckGroups(ctx, nil, nil))

	// 错误原样返回。
	r = &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}, cellsErr: errors.New("boom")}
	require.EqualError(t, exGuard(r).CheckGroups(ctx, nil, []int64{1}), "boom")
	r = &exFakeReader{modesErr: errors.New("boom2")}
	require.EqualError(t, exGuard(r).CheckGroups(ctx, nil, []int64{1}), "boom2")
}
