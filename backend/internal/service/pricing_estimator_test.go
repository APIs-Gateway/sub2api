//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// W6 PR4b-2b-1：QuoteWith 叠加层与价格方向估算器。

const (
	peGroupID = quoteTestGroupID
	peModelA  = "gpt-5.5"
	peModelB  = "gpt-5.4"
)

func peCell(model string, input, output float64) StoredMatrixCell {
	return StoredMatrixCell{GroupID: peGroupID, MatrixCell: peMatrixCell(model, input, output)}
}

func peMatrixCell(model string, input, output float64) MatrixCell {
	return MatrixCell{
		ModelKey: model, Open: true, PriceMode: MatrixPriceCustom, Source: MatrixSourceManual,
		CustomPrice: &MatrixCustomPrice{BillingMode: BillingModeToken, InputPrice: float64Ptr(input), OutputPrice: float64Ptr(output)},
	}
}

func peSnapshot(cells ...StoredMatrixCell) GroupStateSnapshot {
	return GroupStateSnapshot{
		Config: &StoredGroupConfig{GroupID: peGroupID, MatrixGroupConfig: MatrixGroupConfig{AccessMode: MatrixAccessOpen}, PricingStage: PricingStageV2},
		Cells:  cells,
	}
}

// peEstimator 拼出一个接了矩阵数据源的报价器与估算器：分组 777 是 v2 开放分组，里面有 gpt-5.5 与 gpt-5.4 两个自定义价格的单元格。
func peEstimator() (*PriceEstimator, *PriceQuoter) {
	est, quoter, _ := peEstimatorWith(peSnapshot(peCell(peModelA, 2e-6, 4e-6), peCell(peModelB, 1e-6, 2e-6)))
	return est, quoter
}

// peEstimatorWith 同 peEstimator，但分组 777 的现状由调用方给定，并返回数据源（测试里可以让它出错）。
func peEstimatorWith(snap GroupStateSnapshot) (*PriceEstimator, *PriceQuoter, *mpFakeSource) {
	f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1)}, nil)
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{peGroupID: snap})
	f.quoter.SetMatrixSource(src)
	est := NewPriceEstimator(f.quoter)
	est.now = func() time.Time { return quoteTestAtLow }
	return est, f.quoter, src
}

func peWrite(model string, action CellWriteAction, touches bool, after *MatrixCell) PlannedCellWrite {
	return PlannedCellWrite{Op: CellOp{GroupID: peGroupID, ModelKey: model}, Action: action, After: after, TouchesPrice: touches}
}

func TestPriceEstimator_EstimateCellWrites(t *testing.T) {
	ctx := context.Background()
	est, _ := peEstimator()

	cell := func(in, out float64) *MatrixCell { c := peMatrixCell(peModelA, in, out); return &c }
	cases := []struct {
		name    string
		planned []PlannedCellWrite
		want    PriceDelta
	}{
		{"no writes", nil, PriceDeltaNone},
		{"noop", []PlannedCellWrite{peWrite(peModelA, CellWriteNoop, true, cell(9e-6, 9e-6))}, PriceDeltaNone},
		{"not touching price", []PlannedCellWrite{peWrite(peModelA, CellWriteUpdate, false, cell(9e-6, 9e-6))}, PriceDeltaNone},
		{"same price", []PlannedCellWrite{peWrite(peModelA, CellWriteUpdate, true, cell(2e-6, 4e-6))}, PriceDeltaNone},
		{"up", []PlannedCellWrite{peWrite(peModelA, CellWriteUpdate, true, cell(3e-6, 4e-6))}, PriceDeltaUp},
		{"down", []PlannedCellWrite{peWrite(peModelA, CellWriteUpdate, true, cell(1e-6, 2e-6))}, PriceDeltaDown},
		{"mixed", []PlannedCellWrite{peWrite(peModelA, CellWriteUpdate, true, cell(3e-6, 1e-6))}, PriceDeltaUnknown},
		{"up and down across cells", []PlannedCellWrite{
			peWrite(peModelA, CellWriteUpdate, true, cell(3e-6, 5e-6)),
			func() PlannedCellWrite {
				c := peMatrixCell(peModelB, 0.5e-6, 1e-6)
				return peWrite(peModelB, CellWriteUpdate, true, &c)
			}(),
		}, PriceDeltaUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := est.EstimateCellWrites(ctx, tc.planned)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	// 删除单元格后价格来源变了，不能报 none。
	got, err := est.EstimateCellWrites(ctx, []PlannedCellWrite{peWrite(peModelA, CellWriteDelete, true, nil)})
	require.NoError(t, err)
	require.NotEqual(t, PriceDeltaNone, got)

	// 报价器没有接数据源：unknown 加错误。
	f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1)}, nil)
	bad := NewPriceEstimator(f.quoter)
	got, err = bad.EstimateCellWrites(ctx, []PlannedCellWrite{peWrite(peModelA, CellWriteUpdate, true, cell(3e-6, 4e-6))})
	require.ErrorIs(t, err, ErrPriceQuoterUnavailable)
	require.Equal(t, PriceDeltaUnknown, got)
	var nilEst *PriceEstimator
	got, err = nilEst.EstimateCellWrites(ctx, nil)
	require.Error(t, err)
	require.Equal(t, PriceDeltaUnknown, got)
}

func TestPriceEstimator_EstimateGroupConfig(t *testing.T) {
	ctx := context.Background()
	est, _ := peEstimator()
	str := func(s string) *string { return &s }
	cfg := func(source *string, mapping ...MatrixMappingEntry) MatrixGroupConfig {
		return MatrixGroupConfig{AccessMode: MatrixAccessOpen, BillingModelSource: source, ModelMapping: mapping}
	}
	toB := MatrixMappingEntry{Src: peModelA, Dst: peModelB}

	cases := []struct {
		name          string
		before, after MatrixGroupConfig
		want          PriceDelta
	}{
		{"not touching price", cfg(nil), MatrixGroupConfig{AccessMode: MatrixAccessAllowlist}, PriceDeltaNone},
		{"billing source upstream", cfg(nil), cfg(str(BillingModelSourceUpstream)), PriceDeltaUnknown},
		{"was upstream", cfg(str(BillingModelSourceUpstream)), cfg(nil), PriceDeltaUnknown},
		{"wildcard mapping", cfg(nil), cfg(nil, MatrixMappingEntry{Src: "gpt-*", Dst: peModelB}), PriceDeltaUnknown},
		{"mapping to a cheaper model", cfg(nil), cfg(nil, toB), PriceDeltaDown},
		{"mapping removed", cfg(nil, toB), cfg(nil), PriceDeltaUp},
		{"requested with a changed mapping target is unprovable", cfg(str(BillingModelSourceRequested)), cfg(str(BillingModelSourceRequested), toB), PriceDeltaUnknown},
		{"requested, alias target moves", cfg(str(BillingModelSourceRequested), MatrixMappingEntry{Src: "alias-x", Dst: peModelA}),
			cfg(str(BillingModelSourceRequested), MatrixMappingEntry{Src: "alias-x", Dst: peModelB}), PriceDeltaUnknown},
		{"wildcard mapping unchanged, billing source switched", cfg(nil, MatrixMappingEntry{Src: "gpt-*", Dst: peModelB}),
			cfg(str(BillingModelSourceRequested), MatrixMappingEntry{Src: "gpt-*", Dst: peModelB}), PriceDeltaUnknown},
		{"source change with no mapping", cfg(nil), cfg(str(BillingModelSourceRequested)), PriceDeltaNone},
		{"switching to requested with a mapping bills the requested model", cfg(nil, toB), cfg(str(BillingModelSourceRequested), toB), PriceDeltaUp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := est.EstimateGroupConfig(ctx, peGroupID, tc.before, tc.after)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	// 映射来源是别名（不是单元格）：切换计费来源也会换计费模型，不能是 none。
	alias := MatrixMappingEntry{Src: "alias-x", Dst: peModelB}
	got, err := est.EstimateGroupConfig(ctx, peGroupID, cfg(nil, alias), cfg(str(BillingModelSourceRequested), alias))
	require.NoError(t, err)
	require.NotEqual(t, PriceDeltaNone, got)
	got, err = est.EstimateGroupConfig(ctx, peGroupID, cfg(str(BillingModelSourceRequested), alias), cfg(nil, alias))
	require.NoError(t, err)
	require.NotEqual(t, PriceDeltaNone, got)

	// 读分组现状失败：unknown 加错误，不能退回「没有单元格」。
	failing, _, failSrc := peEstimatorWith(peSnapshot(peCell(peModelA, 2e-6, 4e-6), peCell(peModelB, 1e-6, 2e-6)))
	failSrc.setErrors(nil, errors.New("snapshot down"))
	got, err = failing.EstimateGroupConfig(ctx, peGroupID, cfg(nil), cfg(nil, toB))
	require.Error(t, err)
	require.Equal(t, PriceDeltaUnknown, got)

	// 报价器没有接数据源。
	f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1)}, nil)
	got, err = NewPriceEstimator(f.quoter).EstimateGroupConfig(ctx, peGroupID, cfg(nil), cfg(nil, toB))
	require.ErrorIs(t, err, ErrPriceQuoterUnavailable)
	require.Equal(t, PriceDeltaUnknown, got)
}

func TestQuoteWith_OverlayDoesNotTouchTheQuoterOrLegacy(t *testing.T) {
	ctx := context.Background()
	_, quoter := peEstimator()
	require.Nil(t, quoter.resolver.policyOverride)

	req := QuoteRequest{Model: peModelA, GroupID: peGroupID, At: quoteTestAtLow}
	base, err := quoter.QuoteWith(ctx, req, CellOverlay{})
	require.NoError(t, err)
	require.True(t, base.Priced)
	require.InDelta(t, 2e-6, base.FinalPrices.PerToken.Input, 1e-18)

	c := peMatrixCell(peModelA, 5e-6, 4e-6)
	over, err := quoter.QuoteWith(ctx, req, CellOverlay{Cells: map[int64]map[string]*MatrixCell{peGroupID: {peModelA: &c}}})
	require.NoError(t, err)
	require.InDelta(t, 5e-6, over.FinalPrices.PerToken.Input, 1e-18)

	// 叠加只对这一次调用有效，也不改报价器自己的策略。
	again, err := quoter.QuoteWith(ctx, req, CellOverlay{})
	require.NoError(t, err)
	require.InDelta(t, 2e-6, again.FinalPrices.PerToken.Input, 1e-18)
	require.Nil(t, quoter.resolver.policyOverride, "报价器自己的策略（legacy）没有被改")

	// 批量版：结果与请求一一对应，叠加层共用。
	out, err := quoter.BatchQuoteWith(ctx, []QuoteRequest{req, {Model: peModelB, GroupID: peGroupID, At: quoteTestAtLow}},
		CellOverlay{Cells: map[int64]map[string]*MatrixCell{peGroupID: {peModelA: &c}}})
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.InDelta(t, 5e-6, out[0].FinalPrices.PerToken.Input, 1e-18)
	require.InDelta(t, 1e-6, out[1].FinalPrices.PerToken.Input, 1e-18)

	// 没有接数据源：不可用。
	f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1)}, nil)
	_, err = f.quoter.QuoteWith(ctx, req, CellOverlay{})
	require.ErrorIs(t, err, ErrPriceQuoterUnavailable)
	_, err = f.quoter.BatchQuoteWith(ctx, []QuoteRequest{req}, CellOverlay{})
	require.ErrorIs(t, err, ErrPriceQuoterUnavailable)
	var nilQuoter *PriceQuoter
	_, err = nilQuoter.QuoteWith(ctx, req, CellOverlay{})
	require.ErrorIs(t, err, ErrPriceQuoterUnavailable)
}

func TestOverlayFromPlannedAndApply(t *testing.T) {
	c := peMatrixCell(peModelA, 5e-6, 4e-6)
	planned := []PlannedCellWrite{
		peWrite("ignored-noop", CellWriteNoop, false, &c),
		peWrite("no-after", CellWriteUpdate, true, nil),
		peWrite("GPT-5.5", CellWriteUpdate, true, &c),
		peWrite(peModelB, CellWriteDelete, true, nil),
		peWrite("brand-new", CellWriteCreate, true, &c),
	}
	o := OverlayFromPlanned(planned)
	require.Len(t, o.Cells[peGroupID], 3)
	require.NotNil(t, o.Cells[peGroupID][normalizeChannelPricingModelName("GPT-5.5")])
	require.Nil(t, o.Cells[peGroupID][peModelB])
	require.Contains(t, o.Cells[peGroupID], peModelB, "删除叠成 nil 条目")
	require.Empty(t, OverlayFromPlanned(nil).Cells)

	pattern := StoredMatrixCell{GroupID: peGroupID, MatrixCell: MatrixCell{ModelKey: "gpt-*", IsPattern: true, Open: true}}
	snap := peSnapshot(peCell(peModelA, 2e-6, 4e-6), peCell(peModelB, 1e-6, 2e-6), pattern)
	got := o.applyTo(peGroupID, snap)
	require.Len(t, snap.Cells, 3, "不改入参")
	keys := make([]string, 0)
	for _, cell := range got.Cells {
		keys = append(keys, cell.ModelKey)
	}
	require.Equal(t, []string{peModelA, "gpt-*", peModelA}, keys, "新建的单元格追加在最后（这里借用了同一份目标态，所以名字相同）")
	require.InDelta(t, 5e-6, *got.Cells[0].CustomPrice.InputPrice, 1e-18)

	// 配置整体替换；分组没有配置行时不凭空造一行；别的分组不受影响。
	o = CellOverlay{Configs: map[int64]MatrixGroupConfig{peGroupID: {AccessMode: MatrixAccessAllowlist}}}
	require.Equal(t, MatrixAccessAllowlist, o.applyTo(peGroupID, snap).Config.AccessMode)
	require.Equal(t, MatrixAccessOpen, snap.Config.AccessMode)
	require.Nil(t, o.applyTo(peGroupID, GroupStateSnapshot{}).Config)
	require.Equal(t, snap, o.applyTo(peGroupID+1, snap))
	require.Equal(t, snap, CellOverlay{}.applyTo(peGroupID, snap))
}

func TestEstimatorHelpers(t *testing.T) {
	now := quoteTestAtLow
	require.Len(t, estimateVariants(peModelA, now), len(estimatorServiceTiers))
	ds := estimateVariants("deepseek-v4-flash", now)
	require.Len(t, ds, 2*len(estimatorServiceTiers))
	peak, off := deepseekReferenceTimes(now)
	require.Greater(t, deepseekPeakMultiplierAt(peak), 1.0)
	require.LessOrEqual(t, deepseekPeakMultiplierAt(off), 1.0)

	exact, wildcard := changedMappingSources(
		[]MatrixMappingEntry{{Src: "A", Dst: "x"}, {Src: "b", Dst: "y"}, {Src: "same", Dst: "z"}},
		[]MatrixMappingEntry{{Src: "a", Dst: "x2"}, {Src: "same", Dst: "z"}, {Src: "c", Dst: "w"}})
	require.Equal(t, []string{"a", "b", "c"}, exact)
	require.False(t, wildcard)
	_, wildcard = changedMappingSources(nil, []MatrixMappingEntry{{Src: "g*", Dst: "x"}})
	require.True(t, wildcard)
	_, wildcard = changedMappingSources([]MatrixMappingEntry{{Src: "g*", Dst: "x"}}, nil)
	require.True(t, wildcard)

	require.NoError(t, validPriceDelta(PriceDeltaUp))
	require.NoError(t, validPriceDelta(PriceDeltaUnknown))
	require.ErrorIs(t, validPriceDelta("sideways"), errPriceEstimateInvalid)
	require.Equal(t, PriceDeltaUnknown, diffQuotes([]*Quote{nil}, nil))
	require.Equal(t, PriceDeltaUnknown, diffQuotes([]*Quote{nil}, []*Quote{nil}))
}

func TestPriceEstimator_SnapshotFailureIsUnknownNotNone(t *testing.T) {
	ctx := context.Background()
	c := peMatrixCell(peModelA, 5e-6, 4e-6)
	planned := []PlannedCellWrite{peWrite(peModelA, CellWriteUpdate, true, &c)}
	req := QuoteRequest{Model: peModelA, GroupID: peGroupID, At: quoteTestAtLow}

	for name, setErr := range map[string]func(src *mpFakeSource){
		"snapshots": func(src *mpFakeSource) { src.setErrors(nil, errors.New("snapshot down")) },
		"metadata":  func(src *mpFakeSource) { src.setErrors(errors.New("meta down"), nil) },
	} {
		est, quoter, src := peEstimatorWith(peSnapshot(peCell(peModelA, 2e-6, 4e-6)))
		setErr(src)
		got, err := est.EstimateCellWrites(ctx, planned)
		require.Error(t, err, name)
		require.Equal(t, PriceDeltaUnknown, got, name)
		_, err = quoter.QuoteWith(ctx, req, CellOverlay{})
		require.Error(t, err, name)
		_, err = quoter.BatchQuoteWith(ctx, []QuoteRequest{req}, CellOverlay{})
		require.Error(t, err, name)
	}
}

func TestPriceEstimator_LiteralNameCellsShadowTheBaseName(t *testing.T) {
	ctx := context.Background()
	const base, variant = "gpt-5.6-sol", "gpt-5.6-sol-high"
	extra := 1.5
	baseCell := StoredMatrixCell{GroupID: peGroupID, MatrixCell: MatrixCell{
		ModelKey: base, Open: true, PriceMode: MatrixPriceExtra, ExtraMultiplier: &extra, Source: MatrixSourceManual}}
	inherit := MatrixCell{ModelKey: variant, Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual}

	// 变体名上新建一个 inherit 单元格：字面名优先，把基名的 1.5 倍挡掉，变体名的价降了。
	est, _, _ := peEstimatorWith(peSnapshot(baseCell))
	got, err := est.EstimateCellWrites(ctx, []PlannedCellWrite{peWrite(variant, CellWriteCreate, true, &inherit)})
	require.NoError(t, err)
	require.Equal(t, PriceDeltaDown, got)

	// 反过来，删掉这个单元格，变体名回到基名的价。
	shadow := StoredMatrixCell{GroupID: peGroupID, MatrixCell: inherit}
	est, _, _ = peEstimatorWith(peSnapshot(baseCell, shadow))
	got, err = est.EstimateCellWrites(ctx, []PlannedCellWrite{peWrite(variant, CellWriteDelete, true, nil)})
	require.NoError(t, err)
	require.Equal(t, PriceDeltaUp, got)

	// 没有基名单元格：新建 inherit 单元格不改价。
	est, _, _ = peEstimatorWith(peSnapshot())
	got, err = est.EstimateCellWrites(ctx, []PlannedCellWrite{peWrite(variant, CellWriteCreate, true, &inherit)})
	require.NoError(t, err)
	require.Equal(t, PriceDeltaNone, got)
}
