//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-1 的价格写入路径集成测试（真实 PostgreSQL，迁移 200 至 202、208 已执行）：
// 单元格写入器、历史、与派生钩子共用的行锁、过渡审批的原子消耗。

func pwiReason(t *testing.T, err error) string {
	t.Helper()
	var ae *infraerrors.ApplicationError
	require.True(t, errors.As(err, &ae), "expected an application error, got %v", err)
	return ae.Reason
}

// pwiGroup 建一个分组并写入它的 group_model_config 行（指定阶段与 revision）。
func pwiGroup(t *testing.T, stage string, revision int64) int64 {
	t.Helper()
	gid := mxIntGroup(t)
	_, err := integrationDB.ExecContext(context.Background(),
		`INSERT INTO group_model_config (group_id, pricing_stage, revision) VALUES ($1, $2, $3)`, gid, stage, revision)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM pricing_write_approvals WHERE $1 = ANY(group_ids)`, gid)
	})
	return gid
}

func pwiV2Group(t *testing.T) int64 { return pwiGroup(t, "v2", 3) }

func pwiUpsert(gid int64, key string, open bool, mode service.MatrixPriceMode, baseline int64) service.CellOp {
	return service.CellOp{GroupID: gid, ModelKey: key, Kind: service.CellOpUpsert, Open: open, PriceMode: mode, BaselineRevision: baseline}
}

func pwiExtra(gid int64, key string, extra float64, baseline int64) service.CellOp {
	op := pwiUpsert(gid, key, true, service.MatrixPriceExtra, baseline)
	op.ExtraMultiplier = mxF(extra)
	return op
}

func pwiApply(store service.PriceWriteStore, req service.CellWriteRequest) (*service.CellWriteResult, error) {
	var res *service.CellWriteResult
	err := store.WithTx(context.Background(), func(ctx context.Context, tx service.MatrixTx) error {
		var err error
		res, err = NewPricingCellWriter().ApplyTx(ctx, tx, req)
		return err
	})
	return res, err
}

func pwiConfigRevision(t *testing.T, gid int64) int64 {
	t.Helper()
	var rev int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT revision FROM group_model_config WHERE group_id = $1`, gid).Scan(&rev))
	return rev
}

func pwiCells(t *testing.T, gid int64) map[string]service.StoredMatrixCell {
	t.Helper()
	out := map[string]service.StoredMatrixCell{}
	for _, c := range mxLoad(t, NewPricingMatrixRepository(integrationDB), gid).Cells {
		out[c.ModelKey] = c
	}
	return out
}

type pwiHistoryRow struct {
	Model, Action          string
	Before, After          sql.NullString
	Operator, Change, Appr sql.NullInt64
}

func pwiHistory(t *testing.T, gid int64) []pwiHistoryRow {
	t.Helper()
	rows, err := integrationDB.QueryContext(context.Background(),
		`SELECT model_key, action, before_state::text, after_state::text, operator_id, change_set_id, approval_id
		 FROM model_group_price_history WHERE group_id = $1 ORDER BY id`, gid)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []pwiHistoryRow
	for rows.Next() {
		var h pwiHistoryRow
		require.NoError(t, rows.Scan(&h.Model, &h.Action, &h.Before, &h.After, &h.Operator, &h.Change, &h.Appr))
		out = append(out, h)
	}
	require.NoError(t, rows.Err())
	return out
}

func pwiCellState(t *testing.T, raw sql.NullString) service.MatrixCell {
	t.Helper()
	require.True(t, raw.Valid)
	var c service.MatrixCell
	require.NoError(t, json.Unmarshal([]byte(raw.String), &c))
	return c
}

type pwiApprovalRow struct {
	Status, Delta         string
	Touches               bool
	PreviewedBy, Approved sql.NullInt64
	Consumed              sql.NullTime
	Summary               string
}

func pwiApproval(t *testing.T, id int64) pwiApprovalRow {
	t.Helper()
	var a pwiApprovalRow
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT status, price_delta, touches_price, previewed_by, approved_by, consumed_at, summary::text
		 FROM pricing_write_approvals WHERE id = $1`, id).
		Scan(&a.Status, &a.Delta, &a.Touches, &a.PreviewedBy, &a.Approved, &a.Consumed, &a.Summary))
	return a
}

func TestPricingCellWriter_Integration_CreateUpdateDeleteWithHistory(t *testing.T) {
	gid := pwiV2Group(t) // revision 3
	store := NewPricingWriteStore(integrationDB)
	custom := pwiUpsert(gid, "pw-custom", true, service.MatrixPriceCustom, 0)
	custom.CustomPrice = &service.MatrixCustomPrice{BillingMode: service.BillingModePerRequest, PerRequestPrice: mxF(0.04)}
	closed := pwiUpsert(gid, "pw-closed", false, service.MatrixPriceInherit, 0)
	closed.Source = service.MatrixSourceCopied

	// 创建四种单元格：一次写入、一个事务、一条历史各一行，分组配置 revision 加一。
	res, err := pwiApply(store, service.CellWriteRequest{
		Ops: []service.CellOp{
			pwiUpsert(gid, "pw-inherit", true, service.MatrixPriceInherit, 0), pwiExtra(gid, "PW-Extra", 1.5, 0), custom, closed,
		},
		GroupRevisions: map[int64]int64{gid: 3}, OperatorID: 11, ChangeSetID: 12,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{gid}, res.ChangedGroupIDs)
	require.True(t, res.TouchesPrice)
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))

	cells := pwiCells(t, gid)
	require.Len(t, cells, 4)
	for _, c := range cells {
		require.Equal(t, int64(1), c.Revision)
		require.False(t, c.IsPattern)
	}
	require.Equal(t, service.MatrixSourceManual, cells["pw-inherit"].Source)
	require.Equal(t, 1.5, *cells["pw-extra"].ExtraMultiplier)
	require.Equal(t, service.BillingModePerRequest, cells["pw-custom"].CustomPrice.BillingMode)
	require.Equal(t, 0.04, *cells["pw-custom"].CustomPrice.PerRequestPrice)
	require.False(t, cells["pw-closed"].Open)
	require.Equal(t, service.MatrixSourceCopied, cells["pw-closed"].Source)

	hist := pwiHistory(t, gid)
	require.Len(t, hist, 4)
	for _, h := range hist {
		require.Equal(t, "create", h.Action)
		require.False(t, h.Before.Valid)
		require.Equal(t, h.Model, pwiCellState(t, h.After).ModelKey)
		require.Equal(t, int64(11), h.Operator.Int64)
		require.Equal(t, int64(12), h.Change.Int64)
		require.False(t, h.Appr.Valid)
	}

	// 更新（带基线）加一个 noop：只有真正变化的单元格写历史。
	res, err = pwiApply(store, service.CellWriteRequest{
		Ops:            []service.CellOp{pwiExtra(gid, "pw-extra", 2, 1), pwiUpsert(gid, "pw-inherit", true, service.MatrixPriceInherit, 1)},
		GroupRevisions: map[int64]int64{gid: 4}, OperatorID: 11,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{gid}, res.ChangedGroupIDs)
	require.Equal(t, int64(5), pwiConfigRevision(t, gid))
	cells = pwiCells(t, gid)
	require.Equal(t, int64(2), cells["pw-extra"].Revision)
	require.Equal(t, int64(1), cells["pw-inherit"].Revision, "noop 不动单元格 revision")
	hist = pwiHistory(t, gid)
	require.Len(t, hist, 5)
	update := hist[4]
	require.Equal(t, "update", update.Action)
	require.Equal(t, 1.5, *pwiCellState(t, update.Before).ExtraMultiplier)
	require.Equal(t, 2.0, *pwiCellState(t, update.After).ExtraMultiplier)

	// 基线过期：分组基线、单元格基线任何一个对不上都拒绝，且什么都不动。
	for name, req := range map[string]service.CellWriteRequest{
		"stale group baseline": {Ops: []service.CellOp{pwiExtra(gid, "pw-extra", 3, 2)}, GroupRevisions: map[int64]int64{gid: 4}},
		"stale cell baseline":  {Ops: []service.CellOp{pwiExtra(gid, "pw-extra", 3, 1)}, GroupRevisions: map[int64]int64{gid: 5}},
	} {
		_, err = pwiApply(store, req)
		require.Equal(t, service.ReasonPriceBaselineChanged, pwiReason(t, err), name)
	}
	require.Equal(t, int64(5), pwiConfigRevision(t, gid))
	require.Len(t, pwiHistory(t, gid), 5)

	// 删除两个 inherit 单元格：删除一律算涉价（可能放开被字面名遮住的基名价），历史只有前态。
	res, err = pwiApply(store, service.CellWriteRequest{
		Ops: []service.CellOp{
			{GroupID: gid, ModelKey: "pw-inherit", Kind: service.CellOpDelete, BaselineRevision: 1},
			{GroupID: gid, ModelKey: "pw-closed", Kind: service.CellOpDelete, BaselineRevision: 1},
		},
		GroupRevisions: map[int64]int64{gid: 5}, OperatorID: 11,
	})
	require.NoError(t, err)
	require.True(t, res.TouchesPrice)
	require.Equal(t, int64(6), pwiConfigRevision(t, gid))
	require.Len(t, pwiCells(t, gid), 2)
	hist = pwiHistory(t, gid)
	require.Len(t, hist, 7)
	for _, h := range hist[5:] {
		require.Equal(t, "delete", h.Action)
		require.True(t, h.Before.Valid)
		require.False(t, h.After.Valid)
	}

	// 全部 noop：不写历史，不动分组 revision，也不报变更分组（调用方因此不失效缓存）。
	res, err = pwiApply(store, service.CellWriteRequest{
		Ops:            []service.CellOp{pwiExtra(gid, "pw-extra", 2, 2)},
		GroupRevisions: map[int64]int64{gid: 6},
	})
	require.NoError(t, err)
	require.Empty(t, res.ChangedGroupIDs)
	require.Equal(t, int64(6), pwiConfigRevision(t, gid))
	require.Len(t, pwiHistory(t, gid), 7)
}

func TestPricingCellWriter_Integration_OnlyWritesV2GroupsAndNeverDerivedOrWindowedCells(t *testing.T) {
	ctx := context.Background()
	store := NewPricingWriteStore(integrationDB)
	untouched := func(t *testing.T, gid, revision int64) {
		t.Helper()
		require.Empty(t, pwiCells(t, gid), "被拒绝的写入不留任何单元格")
		require.Empty(t, pwiHistory(t, gid))
		var rev sql.NullInt64
		err := integrationDB.QueryRowContext(ctx, `SELECT revision FROM group_model_config WHERE group_id = $1`, gid).Scan(&rev)
		if revision == 0 {
			require.ErrorIs(t, err, sql.ErrNoRows, "没有配置行的分组不会被写入补出一行")
			return
		}
		require.NoError(t, err)
		require.Equal(t, revision, rev.Int64)
	}

	// 线上现状：legacy 与无配置行；shadow 同样不可写。
	for _, stage := range []string{"legacy", "shadow"} {
		gid := pwiGroup(t, stage, 1)
		_, err := pwiApply(store, service.CellWriteRequest{
			Ops: []service.CellOp{pwiExtra(gid, "pw-m", 1.5, 0)}, GroupRevisions: map[int64]int64{gid: 1},
		})
		require.Equal(t, service.ReasonCellGroupNotV2, pwiReason(t, err), stage)
		untouched(t, gid, 1)
	}
	noConfig := mxIntGroup(t)
	_, err := pwiApply(store, service.CellWriteRequest{
		Ops: []service.CellOp{pwiExtra(noConfig, "pw-m", 1.5, 0)}, GroupRevisions: map[int64]int64{noConfig: 1},
	})
	require.Equal(t, service.ReasonCellGroupNotV2, pwiReason(t, err))
	untouched(t, noConfig, 0)

	// 已软删除的分组按不可写处理。
	deleted := pwiV2Group(t)
	_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET deleted_at = NOW() WHERE id = $1`, deleted)
	require.NoError(t, err)
	_, err = pwiApply(store, service.CellWriteRequest{
		Ops: []service.CellOp{pwiExtra(deleted, "pw-m", 1.5, 0)}, GroupRevisions: map[int64]int64{deleted: 3},
	})
	require.Equal(t, service.ReasonCellGroupNotV2, pwiReason(t, err))
	untouched(t, deleted, 3)

	// v2 分组里：派生来源与带生效时间窗的单元格只读；通配符单元格不参与，也不会被改动。
	gid := pwiV2Group(t)
	for _, q := range []string{
		`INSERT INTO model_group_prices (group_id, model_key, source) VALUES ($1, 'pw-derived', 'legacy_derived')`,
		`INSERT INTO model_group_prices (group_id, model_key, effective_from) VALUES ($1, 'pw-window', NOW())`,
		`INSERT INTO model_group_prices (group_id, model_key, is_pattern) VALUES ($1, 'pw-pat', TRUE)`,
	} {
		_, err = integrationDB.ExecContext(ctx, q, gid)
		require.NoError(t, err)
	}
	for _, key := range []string{"pw-derived", "pw-window"} {
		_, err = pwiApply(store, service.CellWriteRequest{
			Ops: []service.CellOp{pwiExtra(gid, key, 1.5, 1)}, GroupRevisions: map[int64]int64{gid: 3},
		})
		require.Equal(t, service.ReasonCellReadonly, pwiReason(t, err), key)
	}
	require.Equal(t, int64(3), pwiConfigRevision(t, gid))
	_, err = pwiApply(store, service.CellWriteRequest{
		Ops: []service.CellOp{pwiExtra(gid, "pw-pat", 1.5, 0)}, GroupRevisions: map[int64]int64{gid: 3},
	})
	require.NoError(t, err, "同名的通配符单元格不占用精确单元格的位置")
	var patterns, exact int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FILTER (WHERE is_pattern), COUNT(*) FILTER (WHERE NOT is_pattern AND model_key = 'pw-pat')
		 FROM model_group_prices WHERE group_id = $1`, gid).Scan(&patterns, &exact))
	require.Equal(t, 1, patterns)
	require.Equal(t, 1, exact)
	var patRev int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT revision FROM model_group_prices WHERE group_id = $1 AND is_pattern`, gid).Scan(&patRev))
	require.Equal(t, int64(1), patRev)
}

func TestPricingCellWriter_Integration_RollbackLeavesNoTrace(t *testing.T) {
	gid := pwiV2Group(t)
	store := NewPricingWriteStore(integrationDB)
	abort := errors.New("abort")

	err := store.WithTx(context.Background(), func(ctx context.Context, tx service.MatrixTx) error {
		_, err := NewPricingCellWriter().ApplyTx(ctx, tx, service.CellWriteRequest{
			Ops: []service.CellOp{pwiExtra(gid, "pw-m", 1.5, 0)}, GroupRevisions: map[int64]int64{gid: 3}, OperatorID: 1,
		})
		require.NoError(t, err)
		return abort
	})
	require.ErrorIs(t, err, abort)
	require.Empty(t, pwiCells(t, gid))
	require.Empty(t, pwiHistory(t, gid))
	require.Equal(t, int64(3), pwiConfigRevision(t, gid))
}

// 写入器与派生钩子、阶段切换取同一把 group_model_config 行锁：有人持锁时写入等待并按锁超时失败，
// 松开之后同一份写入立刻成功。
func TestPricingCellWriter_Integration_SharesTheConfigRowLock(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t)
	store := NewPricingWriteStore(integrationDB)
	req := service.CellWriteRequest{Ops: []service.CellOp{pwiExtra(gid, "pw-m", 1.5, 0)}, GroupRevisions: map[int64]int64{gid: 3}}

	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = holder.ExecContext(ctx, `SELECT group_id FROM group_model_config WHERE group_id = $1 FOR UPDATE`, gid)
	require.NoError(t, err)

	err = store.WithTx(ctx, func(ctx context.Context, tx service.MatrixTx) error {
		if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '300ms'`); err != nil {
			return err
		}
		_, err := NewPricingCellWriter().ApplyTx(ctx, tx, req)
		return err
	})
	requireMatrixPGCode(t, err, "55P03")
	require.Empty(t, pwiCells(t, gid))

	require.NoError(t, holder.Rollback())
	_, err = pwiApply(store, req)
	require.NoError(t, err)
	require.Len(t, pwiCells(t, gid), 1)
}

// 与派生钩子的互斥从另一侧验证：钩子整体跳过 v2 分组，写入器写下的单元格与配置原样保留。
func TestPricingCellWriter_Integration_DerivationHookLeavesWrittenV2CellsAlone(t *testing.T) {
	gid := pwiV2Group(t)
	repo := NewPricingMatrixRepository(integrationDB)
	_, err := pwiApply(NewPricingWriteStore(integrationDB), service.CellWriteRequest{
		Ops:            []service.CellOp{pwiExtra(gid, "gpt-5.5", 1.5, 0), pwiUpsert(gid, "pw-closed", false, service.MatrixPriceInherit, 0)},
		GroupRevisions: map[int64]int64{gid: 3}, OperatorID: 1,
	})
	require.NoError(t, err)
	before := mxLoad(t, repo, gid)
	require.Len(t, before.Cells, 2)

	derived := service.DeriveGroupState(mxRichChannel(gid, gid+7_000_000, 1e-6), service.DeriveGroup{ID: gid, Platform: service.PlatformOpenAI}, nil)
	require.NotEmpty(t, derived.Cells)
	mxApplyDerived(t, repo, gid, derived)

	require.Equal(t, before, mxLoad(t, repo, gid), "v2 分组的单元格、配置、revision 都不被钩子改动")
}

// 写入器接受 ent 事务（W5 change-set 的执行器在 ent 事务里调用它）：回滚不留痕，提交才落库。
func TestPricingCellWriter_Integration_RunsInsideAnEntTx(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t)
	req := service.CellWriteRequest{Ops: []service.CellOp{pwiExtra(gid, "pw-m", 1.5, 0)}, GroupRevisions: map[int64]int64{gid: 3}, OperatorID: 1}

	tx, err := testEntClient(t).Tx(ctx)
	require.NoError(t, err)
	res, err := NewPricingCellWriter().ApplyTx(ctx, tx, req)
	require.NoError(t, err)
	require.Equal(t, []int64{gid}, res.ChangedGroupIDs)
	require.NoError(t, tx.Rollback())
	require.Empty(t, pwiCells(t, gid))
	require.Equal(t, int64(3), pwiConfigRevision(t, gid))

	tx, err = testEntClient(t).Tx(ctx)
	require.NoError(t, err)
	_, err = NewPricingCellWriter().ApplyTx(ctx, tx, req)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.Len(t, pwiCells(t, gid), 1)
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))
	require.Len(t, pwiHistory(t, gid), 1)
}

func TestPricingCellWriter_Integration_PlanTxDoesNotWrite(t *testing.T) {
	gid := pwiV2Group(t)
	planned, err := NewPricingCellWriter().PlanTx(context.Background(), integrationDB, service.CellWriteRequest{
		Ops: []service.CellOp{pwiExtra(gid, "pw-m", 1.5, 0)}, GroupRevisions: map[int64]int64{gid: 3},
	})
	require.NoError(t, err)
	require.Len(t, planned, 1)
	require.Equal(t, service.CellWriteCreate, planned[0].Action)
	require.True(t, planned[0].TouchesPrice)
	require.Empty(t, pwiCells(t, gid))
	require.Equal(t, int64(3), pwiConfigRevision(t, gid))
}

type pwiInvalidator struct{ groups []int64 }

func (i *pwiInvalidator) InvalidateGroups(ids ...int64) { i.groups = append(i.groups, ids...) }

func TestInterimPriceWriteGate_Integration_PreviewConfirmCommit(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t) // revision 3
	store := NewPricingWriteStore(integrationDB)
	inv := &pwiInvalidator{}
	gate := service.NewInterimPriceWriteGate(store, service.NewMatrixTxWriter(NewPricingCellWriter(), nil, pwiGuard(nil)), nil, inv)
	priceReq := func(key string, extra float64, groupRev, baseline int64) service.CellWriteRequest {
		return service.CellWriteRequest{
			Ops: []service.CellOp{pwiExtra(gid, key, extra, baseline)}, GroupRevisions: map[int64]int64{gid: groupRev}, OperatorID: 21,
		}
	}
	commit := func(approval int64, req service.CellWriteRequest, confirm, interactive bool) error {
		_, err := gate.Commit(ctx, service.PriceWriteCommit{
			ApprovalID: approval, Request: req, Confirm: confirm, Actor: service.PriceWriteActor{ID: 22, Interactive: interactive},
		})
		return err
	}
	nothingWritten := func(t *testing.T, revision int64, approval int64) {
		t.Helper()
		require.Empty(t, pwiCells(t, gid))
		require.Empty(t, pwiHistory(t, gid))
		require.Equal(t, revision, pwiConfigRevision(t, gid))
		if approval != 0 {
			require.Equal(t, "previewed", pwiApproval(t, approval).Status, "拒绝的写入不消耗预览")
		}
		require.Empty(t, inv.groups, "没有提交就不失效缓存")
	}

	// 预览：登记审批行，不写任何单元格。
	ticket, err := gate.Propose(ctx, service.PriceWriteProposal{Request: priceReq("pw-gate", 1.5, 3, 0)})
	require.NoError(t, err)
	require.True(t, ticket.TouchesPrice)
	require.Equal(t, service.PriceDeltaUnknown, ticket.Delta, "没有估算器时涉价写入按 unknown 处理")
	require.Len(t, ticket.PlanHash, 64)
	require.True(t, ticket.ExpiresAt.After(time.Now()))
	preview := pwiApproval(t, ticket.ApprovalID)
	require.Equal(t, "previewed", preview.Status)
	require.Equal(t, "unknown", preview.Delta)
	require.True(t, preview.Touches)
	require.Equal(t, int64(21), preview.PreviewedBy.Int64)
	require.False(t, preview.Approved.Valid)
	require.Contains(t, preview.Summary, `"model_key": "pw-gate"`)
	nothingWritten(t, 3, ticket.ApprovalID)

	// 各种拒绝：都不写入、不消耗预览。
	require.Equal(t, service.ReasonPriceWriteConfirm, pwiReason(t, commit(ticket.ApprovalID, priceReq("pw-gate", 1.5, 3, 0), false, true)))
	require.Equal(t, service.ReasonPriceWriteInteractive, pwiReason(t, commit(ticket.ApprovalID, priceReq("pw-gate", 1.5, 3, 0), true, false)), "机器令牌不能确认价格变动")
	require.Equal(t, service.ReasonApprovalMismatch, pwiReason(t, commit(ticket.ApprovalID, priceReq("pw-gate", 1.6, 3, 0), true, true)), "提交的内容与预览不同")
	require.Equal(t, service.ReasonPriceWriteApproval, pwiReason(t, commit(0, priceReq("pw-gate", 1.5, 3, 0), true, true)), "涉价写入没有预览不行")
	require.Equal(t, service.ReasonApprovalNotFound, pwiReason(t, commit(ticket.ApprovalID+9_000_000, priceReq("pw-gate", 1.5, 3, 0), true, true)))
	nothingWritten(t, 3, ticket.ApprovalID)

	// 提交：写入、历史带审批记录、预览被消耗、缓存失效。
	require.NoError(t, commit(ticket.ApprovalID, priceReq("pw-gate", 1.5, 3, 0), true, true))
	require.Equal(t, 1.5, *pwiCells(t, gid)["pw-gate"].ExtraMultiplier)
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))
	require.Equal(t, []int64{gid}, inv.groups)
	done := pwiApproval(t, ticket.ApprovalID)
	require.Equal(t, "consumed", done.Status)
	require.Equal(t, int64(22), done.Approved.Int64)
	require.Equal(t, int64(21), done.PreviewedBy.Int64)
	require.True(t, done.Consumed.Valid)
	hist := pwiHistory(t, gid)
	require.Len(t, hist, 1)
	require.Equal(t, "create", hist[0].Action)
	require.Equal(t, int64(22), hist[0].Operator.Int64, "历史记的是提交者，不是请求体里的操作人")
	require.Equal(t, ticket.ApprovalID, hist[0].Appr.Int64)

	// 重放同一个提交：基线已变，被拒绝，不会重复写入。
	require.Equal(t, service.ReasonPriceBaselineChanged, pwiReason(t, commit(ticket.ApprovalID, priceReq("pw-gate", 1.5, 3, 0), true, true)))
	require.Len(t, pwiHistory(t, gid), 1)

	// 过期的预览不能提交。
	expiring, err := gate.Propose(ctx, service.PriceWriteProposal{Request: priceReq("pw-gate-2", 1.2, 4, 0)})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE pricing_write_approvals SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, expiring.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, service.ReasonApprovalExpired, pwiReason(t, commit(expiring.ApprovalID, priceReq("pw-gate-2", 1.2, 4, 0), true, true)))
	require.NotContains(t, pwiCells(t, gid), "pw-gate-2")

	// 新建单元格一律算涉价，要先预览：这里新建一个关闭的 inherit 单元格。
	create := service.CellWriteRequest{
		Ops:            []service.CellOp{pwiUpsert(gid, "pw-closed", false, service.MatrixPriceInherit, 0)},
		GroupRevisions: map[int64]int64{gid: 4}, OperatorID: 21,
	}
	require.Equal(t, service.ReasonPriceWriteApproval, pwiReason(t, commit(0, create, true, true)), "新建没有预览不行")
	created, err := gate.Propose(ctx, service.PriceWriteProposal{Request: create})
	require.NoError(t, err)
	require.True(t, created.TouchesPrice)
	require.NoError(t, commit(created.ApprovalID, create, true, true))
	require.Equal(t, int64(5), pwiConfigRevision(t, gid))

	// 不涉价的写入（只开关已有的单元格、价格不变）可以不带预览，但仍要二次确认并留历史。
	flip := service.CellWriteRequest{
		Ops:            []service.CellOp{pwiUpsert(gid, "pw-closed", true, service.MatrixPriceInherit, 1)},
		GroupRevisions: map[int64]int64{gid: 5},
	}
	require.Equal(t, service.ReasonPriceWriteConfirm, pwiReason(t, commit(0, flip, false, false)))
	require.NoError(t, commit(0, flip, true, false))
	require.Equal(t, int64(6), pwiConfigRevision(t, gid))
	hist = pwiHistory(t, gid)
	require.Len(t, hist, 3)
	require.True(t, hist[1].Appr.Valid)
	require.False(t, hist[2].Appr.Valid)
	require.Equal(t, []int64{gid, gid, gid}, inv.groups)
}

func TestInterimPriceWriteGate_Integration_ProposeRefusesLegacyGroups(t *testing.T) {
	gid := pwiGroup(t, "legacy", 1)
	gate := service.NewInterimPriceWriteGate(NewPricingWriteStore(integrationDB), service.NewMatrixTxWriter(NewPricingCellWriter(), nil, pwiGuard(nil)), nil, nil)
	_, err := gate.Propose(context.Background(), service.PriceWriteProposal{Request: service.CellWriteRequest{
		Ops: []service.CellOp{pwiExtra(gid, "pw-m", 1.5, 0)}, GroupRevisions: map[int64]int64{gid: 1}, OperatorID: 21,
	}})
	require.Equal(t, service.ReasonCellGroupNotV2, pwiReason(t, err))
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pricing_write_approvals WHERE $1 = ANY(group_ids)`, gid).Scan(&n))
	require.Zero(t, n, "预览被拒绝时不登记审批行")
}

func TestPricingWriteStore_Integration_ApprovalIsConsumedExactlyOnce(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t)
	store := NewPricingWriteStore(integrationDB)
	insert := func(hash string, expires time.Time) int64 {
		id, err := store.InsertApproval(ctx, service.PriceWriteApproval{
			Kind: service.PriceWriteKindCells, PlanHash: hash, Delta: service.PriceDeltaNone, GroupIDs: []int64{gid},
			PreviewedBy: 5, CreatedAt: time.Now(), ExpiresAt: expires,
		})
		require.NoError(t, err)
		return id
	}
	consume := func(id int64, hash, kind string) (*service.PriceWriteApproval, error) {
		var a *service.PriceWriteApproval
		err := store.WithTx(ctx, func(ctx context.Context, tx service.MatrixTx) error {
			var err error
			a, err = store.ConsumeApproval(ctx, tx, id, hash, kind, 6, time.Now())
			return err
		})
		return a, err
	}

	// 指纹或种类不对：不消耗。
	id := insert("hash-a", time.Now().Add(time.Hour))
	_, err := consume(id, "hash-b", service.PriceWriteKindCells)
	require.Equal(t, service.ReasonApprovalMismatch, pwiReason(t, err))
	_, err = consume(id, "hash-a", "other_kind")
	require.Equal(t, service.ReasonApprovalMismatch, pwiReason(t, err))
	require.Equal(t, "previewed", pwiApproval(t, id).Status)

	a, err := consume(id, "hash-a", service.PriceWriteKindCells)
	require.NoError(t, err)
	require.Equal(t, int64(5), a.PreviewedBy)
	require.Equal(t, int64(6), a.ApprovedBy)
	_, err = consume(id, "hash-a", service.PriceWriteKindCells)
	require.Equal(t, service.ReasonApprovalConsumed, pwiReason(t, err))

	_, err = consume(insert("hash-c", time.Now().Add(-time.Minute)), "hash-c", service.PriceWriteKindCells)
	require.Equal(t, service.ReasonApprovalExpired, pwiReason(t, err))

	// 并发消耗同一个预览：恰好一个成功，其余得到「已使用」。
	contested := insert("hash-d", time.Now().Add(time.Hour))
	const workers = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
		reasons   []string
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := consume(contested, "hash-d", service.PriceWriteKindCells)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				return
			}
			var ae *infraerrors.ApplicationError
			if errors.As(err, &ae) {
				reasons = append(reasons, ae.Reason)
			} else {
				reasons = append(reasons, err.Error())
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, successes)
	require.Len(t, reasons, workers-1)
	for _, r := range reasons {
		require.Equal(t, service.ReasonApprovalConsumed, r)
	}

	// 清理：只删从未被消耗的旧预览；已消耗的是审计记录。
	oldPreview := insert("hash-e", time.Now().Add(time.Hour))
	_, err = integrationDB.ExecContext(ctx, `UPDATE pricing_write_approvals SET created_at = NOW() - INTERVAL '3 days' WHERE id = ANY($1)`,
		pq.Array([]int64{oldPreview, id}))
	require.NoError(t, err)
	n, err := store.PurgeStale(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(1))
	var remaining int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pricing_write_approvals WHERE id = ANY($1)`, pq.Array([]int64{oldPreview, id})).Scan(&remaining))
	require.Equal(t, 1, remaining, "过期的预览被清理，已消耗的审批记录保留")
	require.Equal(t, "consumed", pwiApproval(t, id).Status)
}

func TestPricingWriteApprovals_Integration_TableConstraints(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t)
	insert := func(status, delta string, approvedBy any, consumedAt any) error {
		_, err := integrationDB.ExecContext(ctx,
			`INSERT INTO pricing_write_approvals
			   (kind, status, plan_hash, touches_price, price_delta, group_ids, previewed_by, approved_by, expires_at, consumed_at)
			 VALUES ('cell_write', $1, 'h', TRUE, $2, ARRAY[$3::bigint], 1, $4, NOW(), $5)`,
			status, delta, gid, approvedBy, consumedAt)
		return err
	}
	require.NoError(t, insert("previewed", "none", nil, nil))
	requireMatrixPGCode(t, insert("approved", "none", nil, nil), "23514")
	requireMatrixPGCode(t, insert("previewed", "sideways", nil, nil), "23514")
	requireMatrixPGCode(t, insert("consumed", "up", nil, nil), "23514")
	requireMatrixPGCode(t, insert("consumed", "up", int64(2), nil), "23514")
	requireMatrixPGCode(t, insert("previewed", "up", nil, time.Now()), "23514")
	requireMatrixPGCode(t, insert("previewed", "up", int64(2), nil), "23514")
	require.NoError(t, insert("consumed", "up", int64(2), time.Now()))
}
