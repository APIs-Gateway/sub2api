//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-1：InterimPriceWriteGate（W5 落地之前的过渡审批）。

type pwFakeWriter struct {
	planned   []PlannedCellWrite
	planErr   error
	applyRes  *CellWriteResult
	applyErr  error
	planReqs  []CellWriteRequest
	applyReqs []CellWriteRequest
}

func (w *pwFakeWriter) PlanTx(_ context.Context, _ MatrixExecutor, req CellWriteRequest) ([]PlannedCellWrite, error) {
	w.planReqs = append(w.planReqs, req)
	return w.planned, w.planErr
}

func (w *pwFakeWriter) ApplyTx(_ context.Context, _ MatrixTx, req CellWriteRequest) (*CellWriteResult, error) {
	w.applyReqs = append(w.applyReqs, req)
	return w.applyRes, w.applyErr
}

type pwConsumeCall struct {
	id       int64
	hash     string
	kind     string
	approver int64
	now      time.Time
}

type pwFakeStore struct {
	inserted     []PriceWriteApproval
	insertErr    error
	purgeErr     error
	purgedBefore []time.Time
	consumeCalls []pwConsumeCall
	consumeRes   *PriceWriteApproval
	consumeErr   error
	txErr        error
	txRuns       int
	txRollbacks  int
}

func (s *pwFakeStore) Reader() MatrixExecutor { return nil }

func (s *pwFakeStore) WithTx(ctx context.Context, fn func(ctx context.Context, tx MatrixTx) error) error {
	if s.txErr != nil {
		return s.txErr
	}
	s.txRuns++
	err := fn(ctx, nil)
	if err != nil {
		s.txRollbacks++
	}
	return err
}

func (s *pwFakeStore) InsertApproval(_ context.Context, a PriceWriteApproval) (int64, error) {
	if s.insertErr != nil {
		return 0, s.insertErr
	}
	s.inserted = append(s.inserted, a)
	return int64(100 + len(s.inserted)), nil
}

func (s *pwFakeStore) ConsumeApproval(_ context.Context, _ MatrixExecutor, id int64, planHash, kind string, approverID int64, now time.Time) (*PriceWriteApproval, error) {
	s.consumeCalls = append(s.consumeCalls, pwConsumeCall{id, planHash, kind, approverID, now})
	return s.consumeRes, s.consumeErr
}

func (s *pwFakeStore) PurgeStale(_ context.Context, before time.Time) (int64, error) {
	s.purgedBefore = append(s.purgedBefore, before)
	return 0, s.purgeErr
}

type pwFakeInvalidator struct{ calls [][]int64 }

func (i *pwFakeInvalidator) InvalidateGroups(ids ...int64) { i.calls = append(i.calls, ids) }

var pwNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type pwGateFixture struct {
	gate   *InterimPriceWriteGate
	store  *pwFakeStore
	writer *pwFakeWriter
	inv    *pwFakeInvalidator
	reader *exFakeReader
	prices exFakePrices
}

func pwNewGate() pwGateFixture {
	f := pwGateFixture{store: &pwFakeStore{}, writer: &pwFakeWriter{}, inv: &pwFakeInvalidator{},
		reader: &exFakeReader{}, prices: exFakePrices{}}
	f.gate = NewInterimPriceWriteGate(f.store, f.writer, NewExposureGuard(f.reader, NewExposureValidator(f.prices, nil)), f.inv)
	f.gate.now = func() time.Time { return pwNow }
	return f
}

// pwPriceRequest 一次涉价写入：group 1 的 gpt-5.5 设为额外倍率 1.5。
func pwPriceRequest() CellWriteRequest {
	op := pwUpsert(1, "GPT-5.5", true, MatrixPriceExtra)
	op.ExtraMultiplier = pwF(1.5)
	return CellWriteRequest{Ops: []CellOp{op}, GroupRevisions: map[int64]int64{1: 3}, OperatorID: 7}
}

func pwPricePlan(touches bool) []PlannedCellWrite {
	return []PlannedCellWrite{{
		Op: CellOp{GroupID: 1, ModelKey: "gpt-5.5", Kind: CellOpUpsert}, Action: CellWriteUpdate, TouchesPrice: touches,
		Before: &StoredMatrixCell{GroupID: 1, Revision: 2, MatrixCell: MatrixCell{ModelKey: "gpt-5.5", Open: true, PriceMode: MatrixPriceInherit}},
		After:  &MatrixCell{ModelKey: "gpt-5.5", Open: true, PriceMode: MatrixPriceExtra, ExtraMultiplier: pwF(1.5)},
	}}
}

func TestInterimPriceWriteGate_Propose(t *testing.T) {
	f := pwNewGate()
	f.writer.planned = pwPricePlan(true)

	ticket, err := f.gate.Propose(context.Background(), PriceWriteProposal{Request: pwPriceRequest(), Delta: PriceDeltaUp})
	require.NoError(t, err)

	norm, err := NormalizeCellWriteRequest(pwPriceRequest())
	require.NoError(t, err)
	require.Len(t, f.store.inserted, 1)
	a := f.store.inserted[0]
	require.Equal(t, PriceWriteKindCells, a.Kind)
	require.Equal(t, PriceWritePlanHash(norm), a.PlanHash)
	require.True(t, a.TouchesPrice)
	require.Equal(t, PriceDeltaUp, a.Delta)
	require.Equal(t, []int64{1}, a.GroupIDs)
	require.Equal(t, int64(7), a.PreviewedBy)
	require.Equal(t, pwNow, a.CreatedAt)
	require.Equal(t, pwNow.Add(PriceWriteApprovalTTL), a.ExpiresAt)

	var summary []map[string]any
	require.NoError(t, json.Unmarshal(a.Summary, &summary))
	require.Len(t, summary, 1)
	require.Equal(t, "gpt-5.5", summary[0]["model_key"])
	require.Equal(t, "update", summary[0]["action"])
	require.Contains(t, summary[0], "before")
	require.Contains(t, summary[0], "after")

	require.Equal(t, &PriceWriteTicket{
		ApprovalID: 101, PlanHash: a.PlanHash, TouchesPrice: true, Delta: PriceDeltaUp,
		ExpiresAt: a.ExpiresAt, Planned: f.writer.planned,
	}, ticket)
	require.Equal(t, []time.Time{pwNow.Add(-priceWriteStaleAfter)}, f.store.purgedBefore)
	require.Len(t, f.writer.planReqs, 1)
	require.Equal(t, "gpt-5.5", f.writer.planReqs[0].Ops[0].ModelKey, "规划收到的是规范化后的请求")
}

func TestInterimPriceWriteGate_ProposeFailures(t *testing.T) {
	ctx := context.Background()

	f := pwNewGate()
	req := pwPriceRequest()
	req.OperatorID = 0
	_, err := f.gate.Propose(ctx, PriceWriteProposal{Request: req})
	require.Equal(t, ReasonPriceWriteActorRequired, pwReason(t, err))

	f = pwNewGate()
	req = pwPriceRequest()
	req.Ops = nil
	_, err = f.gate.Propose(ctx, PriceWriteProposal{Request: req})
	require.Equal(t, ReasonCellOpsEmpty, pwReason(t, err))
	require.Empty(t, f.writer.planReqs, "请求不合法时不读库")

	f = pwNewGate()
	f.writer.planErr = infraerrors.Conflict(ReasonPriceBaselineChanged, "stale")
	_, err = f.gate.Propose(ctx, PriceWriteProposal{Request: pwPriceRequest()})
	require.Equal(t, ReasonPriceBaselineChanged, pwReason(t, err))
	require.Empty(t, f.store.inserted)

	f = pwNewGate()
	f.writer.planned = pwPricePlan(true)
	_, err = f.gate.Propose(ctx, PriceWriteProposal{Request: pwPriceRequest(), Delta: "sideways"})
	require.Equal(t, ReasonPriceDeltaInvalid, pwReason(t, err))
	require.Empty(t, f.store.inserted)

	f = pwNewGate()
	f.writer.planned = pwPricePlan(true)
	f.store.insertErr = errors.New("db down")
	_, err = f.gate.Propose(ctx, PriceWriteProposal{Request: pwPriceRequest()})
	require.EqualError(t, err, "db down")
	require.Empty(t, f.store.purgedBefore)

	// 清理过期预览失败不影响预览本身。
	f = pwNewGate()
	f.writer.planned = pwPricePlan(true)
	f.store.purgeErr = errors.New("purge failed")
	ticket, err := f.gate.Propose(ctx, PriceWriteProposal{Request: pwPriceRequest()})
	require.NoError(t, err)
	require.Equal(t, PriceDeltaUnknown, ticket.Delta)
}

func TestResolveProposalDelta(t *testing.T) {
	cases := []struct {
		delta   PriceDelta
		touches bool
		want    PriceDelta
		wantErr bool
	}{
		{"", false, PriceDeltaNone, false},
		{PriceDeltaNone, false, PriceDeltaNone, false},
		{PriceDeltaUp, false, "", true},
		{PriceDeltaDown, false, "", true},
		{PriceDeltaUnknown, false, "", true},
		{"", true, PriceDeltaUnknown, false},
		{PriceDeltaUp, true, PriceDeltaUp, false},
		{PriceDeltaDown, true, PriceDeltaDown, false},
		{PriceDeltaUnknown, true, PriceDeltaUnknown, false},
		{PriceDeltaNone, true, PriceDeltaNone, false},
		{"sideways", true, "", true},
		{"sideways", false, "", true},
	}
	for _, tc := range cases {
		got, err := resolveProposalDelta(tc.delta, tc.touches)
		if tc.wantErr {
			require.Equal(t, ReasonPriceDeltaInvalid, pwReason(t, err), "%q touches=%v", tc.delta, tc.touches)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "%q touches=%v", tc.delta, tc.touches)
	}
}

func pwCommit(approval int64, interactive bool) PriceWriteCommit {
	return PriceWriteCommit{
		ApprovalID: approval, Request: pwPriceRequest(), Confirm: true,
		Actor: PriceWriteActor{ID: 9, Interactive: interactive},
	}
}

func TestInterimPriceWriteGate_CommitPreconditions(t *testing.T) {
	ctx := context.Background()

	f := pwNewGate()
	in := pwCommit(5, true)
	in.Actor.ID = 0
	_, err := f.gate.Commit(ctx, in)
	require.Equal(t, ReasonPriceWriteActorRequired, pwReason(t, err))

	in = pwCommit(5, true)
	in.Confirm = false
	_, err = f.gate.Commit(ctx, in)
	require.Equal(t, ReasonPriceWriteConfirm, pwReason(t, err))

	in = pwCommit(5, true)
	in.Request.GroupRevisions = nil
	_, err = f.gate.Commit(ctx, in)
	require.Equal(t, ReasonCellGroupBaselineMissing, pwReason(t, err))

	require.Zero(t, f.store.txRuns, "前置条件不满足时不开事务")
	require.Empty(t, f.writer.applyReqs)
}

func TestInterimPriceWriteGate_CommitWithoutApproval(t *testing.T) {
	ctx := context.Background()

	// 涉价但没有预览凭证：拒绝，事务回滚，缓存不失效。
	f := pwNewGate()
	f.writer.applyRes = &CellWriteResult{TouchesPrice: true, ChangedGroupIDs: []int64{1}}
	_, err := f.gate.Commit(ctx, pwCommit(0, true))
	require.Equal(t, ReasonPriceWriteApproval, pwReason(t, err))
	require.Equal(t, 1, f.store.txRollbacks)
	require.Empty(t, f.store.consumeCalls)
	require.Empty(t, f.inv.calls)

	// 不涉价：可以不带凭证，写入后失效涉及的分组。
	f = pwNewGate()
	f.writer.applyRes = &CellWriteResult{ChangedGroupIDs: []int64{1, 4}}
	res, err := f.gate.Commit(ctx, pwCommit(0, false))
	require.NoError(t, err)
	require.Equal(t, []int64{1, 4}, res.ChangedGroupIDs)
	require.Equal(t, [][]int64{{1, 4}}, f.inv.calls)
	require.Empty(t, f.store.consumeCalls)
	require.Len(t, f.writer.applyReqs, 1)
	require.Equal(t, int64(9), f.writer.applyReqs[0].OperatorID, "操作人取自提交者，不取自请求体")
	require.Zero(t, f.writer.applyReqs[0].ApprovalID)

	// 全部是 noop：没有变化的分组，不失效；没有失效器也不出错。
	f = pwNewGate()
	f.writer.applyRes = &CellWriteResult{}
	_, err = f.gate.Commit(ctx, pwCommit(0, false))
	require.NoError(t, err)
	require.Empty(t, f.inv.calls)

	gate := NewInterimPriceWriteGate(f.store, f.writer, NewExposureGuard(f.reader, NewExposureValidator(f.prices, nil)), nil)
	f.writer.applyRes = &CellWriteResult{ChangedGroupIDs: []int64{1}}
	_, err = gate.Commit(ctx, pwCommit(0, false))
	require.NoError(t, err)
}

func TestInterimPriceWriteGate_CommitConsumesApprovalInTheWriteTx(t *testing.T) {
	ctx := context.Background()

	// 预览与提交的指纹一致：同一份请求得到同一个计划指纹。
	proposer := pwNewGate()
	proposer.writer.planned = pwPricePlan(true)
	ticket, err := proposer.gate.Propose(ctx, PriceWriteProposal{Request: pwPriceRequest(), Delta: PriceDeltaUp})
	require.NoError(t, err)

	f := pwNewGate()
	f.writer.applyRes = &CellWriteResult{TouchesPrice: true, ChangedGroupIDs: []int64{1}}
	f.store.consumeRes = &PriceWriteApproval{ID: 101, TouchesPrice: true, Delta: PriceDeltaUp}
	res, err := f.gate.Commit(ctx, pwCommit(101, true))
	require.NoError(t, err)
	require.Equal(t, []int64{1}, res.ChangedGroupIDs)
	require.Equal(t, []pwConsumeCall{{id: 101, hash: ticket.PlanHash, kind: PriceWriteKindCells, approver: 9, now: pwNow}}, f.store.consumeCalls)
	require.Equal(t, int64(101), f.writer.applyReqs[0].ApprovalID, "历史行带着审批记录的 id")
	require.Equal(t, [][]int64{{1}}, f.inv.calls)
	require.Equal(t, 1, f.store.txRuns)
	require.Zero(t, f.store.txRollbacks)
}

func TestInterimPriceWriteGate_CommitAuthorization(t *testing.T) {
	ctx := context.Background()
	touching := &CellWriteResult{TouchesPrice: true, ChangedGroupIDs: []int64{1}}

	cases := []struct {
		name        string
		approval    *PriceWriteApproval
		result      *CellWriteResult
		interactive bool
		reason      string
	}{
		{"price up needs an interactive admin", &PriceWriteApproval{TouchesPrice: true, Delta: PriceDeltaUp}, touching, false, ReasonPriceWriteInteractive},
		{"price down needs an interactive admin", &PriceWriteApproval{TouchesPrice: true, Delta: PriceDeltaDown}, touching, false, ReasonPriceWriteInteractive},
		{"unknown direction needs an interactive admin", &PriceWriteApproval{TouchesPrice: true, Delta: PriceDeltaUnknown}, touching, false, ReasonPriceWriteInteractive},
		{"interactive admin may write", &PriceWriteApproval{TouchesPrice: true, Delta: PriceDeltaUp}, touching, true, ""},
		{"proven no-change may use a machine token", &PriceWriteApproval{TouchesPrice: true, Delta: PriceDeltaNone}, touching, false, ""},
		{"preview did not touch prices but the write does", &PriceWriteApproval{TouchesPrice: false, Delta: PriceDeltaNone}, touching, true, ReasonPriceWritePlanChanged},
		{"non-price preview, non-price write", &PriceWriteApproval{TouchesPrice: false, Delta: PriceDeltaNone}, &CellWriteResult{}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := pwNewGate()
			f.writer.applyRes = tc.result
			f.store.consumeRes = tc.approval
			_, err := f.gate.Commit(ctx, pwCommit(7, tc.interactive))
			if tc.reason == "" {
				require.NoError(t, err)
				require.Zero(t, f.store.txRollbacks)
				return
			}
			require.Equal(t, tc.reason, pwReason(t, err))
			require.Equal(t, 1, f.store.txRollbacks, "拒绝时整个事务回滚，写入与历史一并撤销")
			require.Empty(t, f.inv.calls)
		})
	}
}

func TestInterimPriceWriteGate_CommitFailures(t *testing.T) {
	ctx := context.Background()

	// 凭证无效：整个事务回滚，缓存不失效。
	f := pwNewGate()
	f.writer.applyRes = &CellWriteResult{TouchesPrice: true, ChangedGroupIDs: []int64{1}}
	f.store.consumeErr = infraerrors.Conflict(ReasonApprovalExpired, "expired")
	_, err := f.gate.Commit(ctx, pwCommit(7, true))
	require.Equal(t, ReasonApprovalExpired, pwReason(t, err))
	require.Equal(t, 1, f.store.txRollbacks)
	require.Empty(t, f.inv.calls)

	// 写入本身失败（基线对不上、分组不是 v2……）：不消耗凭证。
	f = pwNewGate()
	f.writer.applyErr = infraerrors.Conflict(ReasonPriceBaselineChanged, "stale")
	_, err = f.gate.Commit(ctx, pwCommit(7, true))
	require.Equal(t, ReasonPriceBaselineChanged, pwReason(t, err))
	require.Empty(t, f.store.consumeCalls, "写入失败时凭证保持可用")
	require.Empty(t, f.inv.calls)

	// 事务开不起来。
	f = pwNewGate()
	f.store.txErr = errors.New("begin failed")
	_, err = f.gate.Commit(ctx, pwCommit(7, true))
	require.EqualError(t, err, "begin failed")
	require.Empty(t, f.writer.applyReqs)
}

func TestInterimPriceWriteGate_ExposureValidation(t *testing.T) {
	ctx := context.Background()
	// 白名单分组里开放一个没有官方价的模型（只改 open、不涉价）。
	unpriced := func() []PlannedCellWrite {
		return []PlannedCellWrite{{
			Op: CellOp{GroupID: 1, ModelKey: "nothing", Kind: CellOpUpsert}, Action: CellWriteCreate,
			After: &MatrixCell{ModelKey: "nothing", Open: true, PriceMode: MatrixPriceInherit},
		}}
	}
	allowlist := map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}
	req := CellWriteRequest{Ops: []CellOp{pwUpsert(1, "nothing", true, MatrixPriceInherit)}, GroupRevisions: map[int64]int64{1: 3}, OperatorID: 7}

	// 预览就阻止，不登记审批行。
	f := pwNewGate()
	f.reader.modes = allowlist
	f.writer.planned = unpriced()
	_, err := f.gate.Propose(ctx, PriceWriteProposal{Request: req})
	require.Equal(t, ReasonExposureUnpriced, pwReason(t, err))
	require.Empty(t, f.store.inserted)

	// 提交：写入之后、消耗审批之前校验，违规整个事务回滚、不消耗凭证、不失效缓存。
	f = pwNewGate()
	f.reader.modes = allowlist
	f.writer.applyRes = &CellWriteResult{Planned: unpriced(), ChangedGroupIDs: []int64{1}}
	_, err = f.gate.Commit(ctx, PriceWriteCommit{ApprovalID: 5, Request: req, Confirm: true, Actor: PriceWriteActor{ID: 9, Interactive: true}})
	require.Equal(t, ReasonExposureUnpriced, pwReason(t, err))
	require.Equal(t, 1, f.store.txRollbacks)
	require.Empty(t, f.store.consumeCalls)
	require.Empty(t, f.inv.calls)

	// 模型有官方价：通过。
	f = pwNewGate()
	f.reader.modes = allowlist
	f.prices["nothing"] = OfficialPriceState{Known: true, TokenNonZero: true}
	f.writer.applyRes = &CellWriteResult{Planned: unpriced(), ChangedGroupIDs: []int64{1}}
	_, err = f.gate.Commit(ctx, PriceWriteCommit{Request: req, Confirm: true, Actor: PriceWriteActor{ID: 9}})
	require.NoError(t, err)

	// 没有配置保存时校验：预览与提交都失败关闭。
	f = pwNewGate()
	gate := NewInterimPriceWriteGate(f.store, f.writer, nil, nil)
	f.writer.planned = unpriced()
	_, err = gate.Propose(ctx, PriceWriteProposal{Request: req})
	require.Equal(t, ReasonExposureGuardMissing, pwReason(t, err))
	f.writer.applyRes = &CellWriteResult{Planned: unpriced()}
	_, err = gate.Commit(ctx, PriceWriteCommit{Request: req, Confirm: true, Actor: PriceWriteActor{ID: 9}})
	require.Equal(t, ReasonExposureGuardMissing, pwReason(t, err))
	require.Equal(t, 1, f.store.txRollbacks)
}
