//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 每个存储原语在数据库出错、返回坏数据时都把错误交回调用方（调用方据此回滚整个切换事务）。

var errSSBoom = errors.New("boom")

type ssOnlyQueriesExec struct{ service.MatrixExecutor }

func TestStageSwitchStore_EveryPrimitiveRejectsAnExecutorThatCannotRunQueries(t *testing.T) {
	store, _, _ := newStageSwitchMock(t)
	ctx := context.Background()
	exec := ssOnlyQueriesExec{}
	_, err := store.LoadGateFacts(ctx, exec, 7, ssNow)
	require.Error(t, err)
	_, err = store.LoadSnapshot(ctx, exec, 7)
	require.Error(t, err)
	require.Error(t, store.ApplyPlan(ctx, exec, service.GroupApplyPlan{}))
	_, err = store.ArchiveNonDerived(ctx, exec, 7, 1, 0)
	require.Error(t, err)
	_, err = store.SetStage(ctx, exec, 7, service.PricingStageShadow, 1, ssNow)
	require.Error(t, err)
	_, err = store.InsertAudit(ctx, exec, service.StageAuditRecord{GroupID: 7})
	require.Error(t, err)
}

func TestStageSwitchStore_ApplyPlanSkipsAnEmptyPlan(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	require.NoError(t, store.ApplyPlan(context.Background(), db, service.GroupApplyPlan{Skipped: true}))
	require.NoError(t, store.ApplyPlan(context.Background(), db, service.GroupApplyPlan{}))
	require.NoError(t, mock.ExpectationsWereMet(), "nothing was sent to the database")
}

func TestStageSwitchStore_LockConfigErrors(t *testing.T) {
	ctx := context.Background()

	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(`FOR UPDATE OF c`).WillReturnError(errSSBoom)
	_, err := store.LockConfig(ctx, db, 7)
	require.ErrorIs(t, err, errSSBoom)

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(`FOR UPDATE OF c`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(7)))
	mock.ExpectQuery(`FROM group_model_config WHERE group_id = ANY`).WillReturnError(errSSBoom)
	_, err = store.LockConfig(ctx, db, 7)
	require.ErrorIs(t, err, errSSBoom)

	// 锁到了行，却读不到配置（被并发删除）：按「没有配置行」处理。
	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(`FOR UPDATE OF c`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(7)))
	mock.ExpectQuery(`FROM group_model_config WHERE group_id = ANY`).WillReturnRows(sqlmock.NewRows(stageCfgCols))
	_, err = store.LockConfig(ctx, db, 7)
	require.ErrorIs(t, err, service.ErrPricingStageNotDerived)
}

func TestStageSwitchStore_LoadGateFactsErrorsAtEveryStep(t *testing.T) {
	ctx := context.Background()
	const (
		cfgSQL    = `FROM group_model_config WHERE group_id = ANY`
		ownerSQL  = `MAX\(c.updated_at\) FROM channels c`
		countSQL  = `FROM pricing_shadow_diffs WHERE group_id = \$1 AND created_at`
		modelsSQL = `SELECT DISTINCT lower\(model\)`
	)
	okCfg := func(m sqlmock.Sqlmock) { m.ExpectQuery(cfgSQL).WillReturnRows(stageCfgRow("shadow")) }
	okOwner := func(m sqlmock.Sqlmock) {
		m.ExpectQuery(ownerSQL).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))
	}
	okCount := func(m sqlmock.Sqlmock) {
		m.ExpectQuery(countSQL).WillReturnRows(sqlmock.NewRows([]string{"t", "e"}).AddRow(int64(0), int64(0)))
	}

	// 配置读取失败。
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(cfgSQL).WillReturnError(errSSBoom)
	_, err := store.LoadGateFacts(ctx, db, 7, ssNow)
	require.ErrorIs(t, err, errSSBoom)

	// 没有配置行：区分分组不存在与分组没有配置。
	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(cfgSQL).WillReturnRows(sqlmock.NewRows(stageCfgCols))
	mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM groups`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(false))
	_, err = store.LoadGateFacts(ctx, db, 7, ssNow)
	require.ErrorIs(t, err, service.ErrGroupNotFound)

	store, db, mock = newStageSwitchMock(t)
	okCfg(mock)
	mock.ExpectQuery(ownerSQL).WillReturnError(errSSBoom)
	_, err = store.LoadGateFacts(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "query owner channel")

	store, db, mock = newStageSwitchMock(t)
	okCfg(mock)
	okOwner(mock)
	mock.ExpectQuery(countSQL).WillReturnError(errSSBoom)
	_, err = store.LoadGateFacts(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "count pricing_shadow_diffs")

	store, db, mock = newStageSwitchMock(t)
	okCfg(mock)
	okOwner(mock)
	okCount(mock)
	mock.ExpectQuery(modelsSQL).WillReturnError(errSSBoom)
	_, err = store.LoadGateFacts(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "query expected models")

	store, db, mock = newStageSwitchMock(t)
	okCfg(mock)
	okOwner(mock)
	okCount(mock)
	mock.ExpectQuery(modelsSQL).WillReturnRows(sqlmock.NewRows([]string{"m"}).AddRow(nil))
	_, err = store.LoadGateFacts(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "scan expected model")

	store, db, mock = newStageSwitchMock(t)
	okCfg(mock)
	okOwner(mock)
	okCount(mock)
	mock.ExpectQuery(modelsSQL).WillReturnRows(sqlmock.NewRows([]string{"m"}).AddRow("gpt-5.4").RowError(0, errSSBoom))
	_, err = store.LoadGateFacts(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "iterate expected models")

	store, db, mock = newStageSwitchMock(t)
	okCfg(mock)
	okOwner(mock)
	okCount(mock)
	mock.ExpectQuery(modelsSQL).WillReturnRows(sqlmock.NewRows([]string{"m"}))
	mock.ExpectQuery(`FROM pricing_replay_evidence`).WillReturnError(errSSBoom)
	_, err = store.LoadGateFacts(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "query pricing_replay_evidence")
}

var ssReplayCols = []string{"id", "group_id", "recorded_at", "window_from", "window_to", "matrix_source", "passed", "binding_stable",
	"rows_in_window", "rows_replayed", "rows_errored", "translation_diffs", "expected_diffs",
	"channel_config_hash", "derive_revision", "matrix_hash", "pricing_data_sha256", "tool_version", "diffs"}

func ssReplayRow(diffs []byte) *sqlmock.Rows {
	return sqlmock.NewRows(ssReplayCols).AddRow(int64(11), int64(7), ssNow, ssNow, ssNow, "derived", true, true,
		int64(1), int64(1), int64(0), int64(0), int64(0), "c", "r", "m", "s", "v", diffs)
}

func TestLatestReplayEvidence_BadRowsAndNullDiffs(t *testing.T) {
	ctx := context.Background()

	_, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(`FROM pricing_replay_evidence`).WillReturnRows(ssReplayRow([]byte(`not json`)))
	_, err := latestReplayEvidence(ctx, db, 7)
	require.ErrorContains(t, err, "decode replay diffs")

	_, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(`FROM pricing_replay_evidence`).WillReturnRows(ssReplayRow([]byte(`null`)))
	ev, err := latestReplayEvidence(ctx, db, 7)
	require.NoError(t, err)
	require.NotNil(t, ev.Diffs, "a null diff list is read as an empty one")
	require.Empty(t, ev.Diffs)
}

func TestStageSwitchStore_LoadSnapshotError(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(`.*`).WillReturnError(errSSBoom)
	_, err := store.LoadSnapshot(context.Background(), db, 7)
	require.ErrorIs(t, err, errSSBoom)
}

func TestStageSwitchStore_FreezeDerivedErrorsAtEveryStep(t *testing.T) {
	ctx := context.Background()
	cellsSQL := `UPDATE model_group_prices SET source = 'legacy_frozen'`
	rulesSQL := `UPDATE cost_accounting_rules SET source = 'legacy_frozen'`

	store, db, mock := newStageSwitchMock(t)
	mock.ExpectExec(cellsSQL).WillReturnError(errSSBoom)
	_, _, err := store.FreezeDerived(ctx, db, 7)
	require.ErrorContains(t, err, "freeze model_group_prices")

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectExec(cellsSQL).WillReturnResult(sqlmock.NewErrorResult(errSSBoom))
	_, _, err = store.FreezeDerived(ctx, db, 7)
	require.ErrorContains(t, err, "rows affected")

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectExec(cellsSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(rulesSQL).WillReturnError(errSSBoom)
	_, _, err = store.FreezeDerived(ctx, db, 7)
	require.ErrorContains(t, err, "freeze cost_accounting_rules")

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectExec(cellsSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(rulesSQL).WillReturnResult(sqlmock.NewErrorResult(errSSBoom))
	_, _, err = store.FreezeDerived(ctx, db, 7)
	require.ErrorContains(t, err, "rows affected")
}

func TestStageSwitchStore_ArchiveNonDerivedErrorsAtEveryStep(t *testing.T) {
	ctx := context.Background()
	const (
		cellsSQL   = `FROM model_group_prices WHERE group_id = ANY`
		historySQL = `INSERT INTO model_group_price_history`
		deleteSQL  = `DELETE FROM model_group_prices`
		rulesSQL   = `FROM cost_accounting_rules WHERE scope_group_id = ANY`
		priceSQL   = `FROM cost_accounting_rule_prices`
		delRuleSQL = `DELETE FROM cost_accounting_rules`
	)
	manualCell := func() *sqlmock.Rows {
		return sqlmock.NewRows(stageCellCols).
			AddRow(int64(3), int64(7), "edited", false, 0, true, "extra", 2.0, nil, nil, nil, "manual", int64(2), ssNow)
	}
	manualRule := func() *sqlmock.Rows {
		return sqlmock.NewRows(stageRuleCols).AddRow(int64(6), int64(7), "manual", nil, nil, "m", "{}", "{1}", 1, true)
	}

	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(cellsSQL).WillReturnError(errSSBoom)
	_, err := store.ArchiveNonDerived(ctx, db, 7, 1, 0)
	require.ErrorIs(t, err, errSSBoom)

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(cellsSQL).WillReturnRows(manualCell())
	mock.ExpectExec(historySQL).WillReturnError(errSSBoom)
	_, err = store.ArchiveNonDerived(ctx, db, 7, 1, 0)
	require.ErrorContains(t, err, "archive cell history")

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(cellsSQL).WillReturnRows(sqlmock.NewRows(stageCellCols))
	mock.ExpectExec(deleteSQL).WillReturnError(errSSBoom)
	_, err = store.ArchiveNonDerived(ctx, db, 7, 1, 0)
	require.ErrorContains(t, err, "delete non-derived cells")

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(cellsSQL).WillReturnRows(sqlmock.NewRows(stageCellCols))
	mock.ExpectExec(deleteSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(rulesSQL).WillReturnError(errSSBoom)
	_, err = store.ArchiveNonDerived(ctx, db, 7, 1, 0)
	require.ErrorIs(t, err, errSSBoom)

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(cellsSQL).WillReturnRows(sqlmock.NewRows(stageCellCols))
	mock.ExpectExec(deleteSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(rulesSQL).WillReturnRows(manualRule())
	mock.ExpectQuery(priceSQL).WillReturnRows(sqlmock.NewRows([]string{"rule_id", "platform", "models", "price"}))
	mock.ExpectExec(delRuleSQL).WillReturnError(errSSBoom)
	_, err = store.ArchiveNonDerived(ctx, db, 7, 1, 0)
	require.ErrorContains(t, err, "delete non-derived cost rules")
}

func TestStageSwitchStore_InsertAuditErrorAndEmptyEvidence(t *testing.T) {
	ctx := context.Background()
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(`INSERT INTO pricing_stage_audit`).WillReturnError(errSSBoom)
	_, err := store.InsertAudit(ctx, db, service.StageAuditRecord{GroupID: 7, From: service.PricingStageShadow, To: service.PricingStageV2})
	require.ErrorContains(t, err, "insert pricing_stage_audit")

	// 没有证据时写 {}。
	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(`INSERT INTO pricing_stage_audit`).WithArgs(int64(7), "shadow", "v2", "advance", int64(0), false, sqlmock.AnyArg(),
		"none", int64(0), int64(0), "{}").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	id, err := store.InsertAudit(ctx, db, service.StageAuditRecord{GroupID: 7, From: service.PricingStageShadow, To: service.PricingStageV2,
		Kind: "advance", PriceDelta: service.PriceDeltaNone})
	require.NoError(t, err)
	require.EqualValues(t, 9, id)
}

func TestStageSwitchStore_RecentUsageModelsErrors(t *testing.T) {
	ctx := context.Background()
	const usageSQL = `FROM usage_logs WHERE group_id`

	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(usageSQL).WillReturnError(errSSBoom)
	_, err := store.RecentUsageModels(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "query recent usage models")

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(usageSQL).WillReturnRows(sqlmock.NewRows([]string{"m", "n"}).AddRow(nil, int64(1)))
	_, err = store.RecentUsageModels(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "scan recent usage model")

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(usageSQL).WillReturnRows(sqlmock.NewRows([]string{"m", "n"}).AddRow("gpt-5.4", int64(1)).RowError(0, errSSBoom))
	_, err = store.RecentUsageModels(ctx, db, 7, ssNow)
	require.ErrorContains(t, err, "iterate recent usage models")
}

func TestStageSwitchStore_ListAuditErrors(t *testing.T) {
	ctx := context.Background()
	const auditSQL = `FROM pricing_stage_audit WHERE group_id`
	cols := []string{"id", "created_at", "group_id", "from_stage", "to_stage", "kind", "operator_id", "interactive", "approval_id",
		"price_delta", "rev_before", "rev_after", "evidence"}

	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(auditSQL).WillReturnError(errSSBoom)
	_, err := store.ListAudit(ctx, db, 7, 10)
	require.ErrorContains(t, err, "query pricing_stage_audit")

	// 列类型对不上：扫描失败。
	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(auditSQL).WillReturnRows(sqlmock.NewRows(cols).AddRow("not-a-number", ssNow, int64(7), "shadow", "v2", "advance",
		int64(1), true, int64(0), "none", int64(1), int64(2), []byte(`{}`)))
	_, err = store.ListAudit(ctx, db, 7, 10)
	require.ErrorContains(t, err, "scan pricing_stage_audit")

	store, db, mock = newStageSwitchMock(t)
	mock.ExpectQuery(auditSQL).WillReturnRows(sqlmock.NewRows(cols).AddRow(int64(1), ssNow, int64(7), "shadow", "v2", "advance",
		int64(1), true, int64(0), "none", int64(1), int64(2), []byte(`{}`)).RowError(0, errSSBoom))
	_, err = store.ListAudit(ctx, db, 7, 10)
	require.ErrorContains(t, err, "iterate pricing_stage_audit")
}

func TestReplayEvidenceStore_BeginAndCommitFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewPricingReplayEvidenceStore(db)
	row := service.ReplayEvidence{GroupID: 7, RecordedAt: ssNow, WindowFrom: ssNow.Add(-time.Hour), WindowTo: ssNow, MatrixSource: "derived",
		Diffs: []service.PricingReplayDiffCount{}}

	mock.ExpectBegin().WillReturnError(errSSBoom)
	require.ErrorContains(t, store.RecordReplayEvidence(context.Background(), []service.ReplayEvidence{row}), "begin tx")

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO pricing_replay_evidence`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(errSSBoom)
	require.ErrorContains(t, store.RecordReplayEvidence(context.Background(), []service.ReplayEvidence{row}), "commit")
}
