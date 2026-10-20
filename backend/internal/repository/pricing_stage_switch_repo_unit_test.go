//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var ssNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func newStageSwitchMock(t *testing.T) (*pricingStageSwitchStore, *sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store, ok := NewPricingStageSwitchStore(db).(*pricingStageSwitchStore)
	require.True(t, ok)
	return store, db, mock
}

var (
	stageCfgCols = []string{"group_id", "access_mode", "billing_model_source", "model_mapping", "features", "cost_mode",
		"pricing_stage", "stage_changed_at", "stage_changed_by", "revision", "updated_at"}
	stageCellCols = []string{"id", "group_id", "model_key", "is_pattern", "pattern_order", "open", "price_mode", "extra_multiplier",
		"custom_price", "effective_from", "effective_to", "source", "revision", "updated_at"}
	stageRuleCols = []string{"id", "scope_group_id", "source", "source_channel_id", "source_ordinal", "name", "group_ids", "account_ids", "sort_order", "enabled"}
)

func stageCfgRow(stage string) *sqlmock.Rows {
	return sqlmock.NewRows(stageCfgCols).AddRow(int64(7), "open", nil, []byte(`[]`), []byte(`{}`), "account_rate",
		stage, ssNow.Add(-time.Hour), int64(1), int64(5), ssNow.Add(-2*time.Hour))
}

func TestStageSwitchStore_LockConfig(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)

	mock.ExpectQuery(`FROM group_model_config c JOIN groups g .*FOR UPDATE OF c`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(7)))
	mock.ExpectQuery(`FROM group_model_config WHERE group_id = ANY`).WillReturnRows(stageCfgRow("shadow"))
	cfg, err := store.LockConfig(context.Background(), tx, 7)
	require.NoError(t, err)
	require.Equal(t, service.PricingStageShadow, cfg.PricingStage)
	require.EqualValues(t, 5, cfg.Revision)

	// 分组不存在或已软删除。
	mock.ExpectQuery(`FOR UPDATE OF c`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM groups`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(false))
	_, err = store.LockConfig(context.Background(), tx, 8)
	require.ErrorIs(t, err, service.ErrGroupNotFound)

	// 分组存在但还没有配置行。
	mock.ExpectQuery(`FOR UPDATE OF c`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM groups`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(true))
	_, err = store.LockConfig(context.Background(), tx, 9)
	require.ErrorIs(t, err, service.ErrPricingStageNotDerived)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStageSwitchStore_ExecutorMustSupportSingleRowQueries(t *testing.T) {
	store, _, _ := newStageSwitchMock(t)
	type onlyQueries struct{ service.MatrixExecutor }
	_, err := store.LockConfig(context.Background(), onlyQueries{}, 7)
	require.Error(t, err)
}

func TestStageSwitchStore_LoadGateFacts(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	changed := ssNow.Add(-100 * time.Hour)
	owner := ssNow.Add(-10 * time.Hour)

	mock.ExpectQuery(`FROM group_model_config WHERE group_id = ANY`).WillReturnRows(
		sqlmock.NewRows(stageCfgCols).AddRow(int64(7), "open", nil, []byte(`[]`), []byte(`{}`), "account_rate",
			"shadow", changed, int64(1), int64(5), changed))
	mock.ExpectQuery(`MAX\(c.updated_at\) FROM channels c JOIN channel_groups cg`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(owner))
	// 统计窗口的起点：观察起点（渠道最近一次保存）与 13 天保留期里较晚的那个。
	mock.ExpectQuery(`COUNT\(\*\) FILTER \(WHERE class = 'translation'\).*FROM pricing_shadow_diffs`).
		WithArgs(int64(7), owner).
		WillReturnRows(sqlmock.NewRows([]string{"t", "e"}).AddRow(int64(0), int64(12)))
	mock.ExpectQuery(`SELECT DISTINCT lower\(model\) FROM pricing_shadow_diffs`).
		WithArgs(int64(7), owner).
		WillReturnRows(sqlmock.NewRows([]string{"m"}).AddRow("gpt-5.4"))
	mock.ExpectQuery(`FROM pricing_replay_evidence WHERE group_id = \$1 ORDER BY recorded_at DESC, id DESC LIMIT 1`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "recorded_at", "window_from", "window_to", "matrix_source", "passed", "binding_stable",
			"rows_in_window", "rows_replayed", "rows_errored", "translation_diffs", "expected_diffs",
			"channel_config_hash", "derive_revision", "matrix_hash", "pricing_data_sha256", "tool_version", "diffs"}).
			AddRow(int64(11), int64(7), ssNow.Add(-time.Hour), ssNow.Add(-31*24*time.Hour), ssNow.Add(-2*time.Hour), "derived", true, true,
				int64(10), int64(10), int64(0), int64(0), int64(3), "chan", "rev", "mh", "sha", "v1",
				[]byte(`[{"kind":"cost","class":"expected","reason":"unpriced_zero_equivalent","count":3}]`)))

	facts, err := store.LoadGateFacts(context.Background(), db, 7, ssNow)
	require.NoError(t, err)
	require.Equal(t, service.PricingStageShadow, facts.Config.Stage)
	require.EqualValues(t, 5, facts.Config.Revision)
	require.Equal(t, owner, *facts.OwnerChannelUpdatedAt)
	require.Equal(t, owner, facts.Shadow.WindowFrom)
	require.EqualValues(t, 12, facts.Shadow.ExpectedDiffs)
	require.Equal(t, []string{"gpt-5.4"}, facts.Shadow.ExpectedModels)
	require.NotNil(t, facts.Replay)
	require.EqualValues(t, 11, facts.Replay.ID)
	require.Equal(t, "rev", facts.Replay.DeriveRevision)
	require.Len(t, facts.Replay.Diffs, 1)
	require.EqualValues(t, 3, facts.Replay.Diffs[0].Count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStageSwitchStore_LoadGateFactsWindowIsCappedByShadowRetention(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	long := ssNow.Add(-40 * 24 * time.Hour)
	mock.ExpectQuery(`FROM group_model_config WHERE group_id = ANY`).WillReturnRows(
		sqlmock.NewRows(stageCfgCols).AddRow(int64(7), "open", nil, []byte(`[]`), []byte(`{}`), "account_rate",
			"shadow", long, int64(1), int64(5), long))
	mock.ExpectQuery(`MAX\(c.updated_at\)`).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))
	retention := ssNow.Add(-service.PricingGateShadowRetention)
	mock.ExpectQuery(`FROM pricing_shadow_diffs`).WithArgs(int64(7), retention).
		WillReturnRows(sqlmock.NewRows([]string{"t", "e"}).AddRow(int64(0), int64(0)))
	mock.ExpectQuery(`SELECT DISTINCT lower\(model\)`).WithArgs(int64(7), ssNow.Add(-service.PricingGateExpectedTrafficWindow)).
		WillReturnRows(sqlmock.NewRows([]string{"m"}))
	mock.ExpectQuery(`FROM pricing_replay_evidence`).WillReturnError(sql.ErrNoRows)

	facts, err := store.LoadGateFacts(context.Background(), db, 7, ssNow)
	require.NoError(t, err)
	require.Nil(t, facts.OwnerChannelUpdatedAt)
	require.Equal(t, retention, facts.Shadow.WindowFrom)
	require.Nil(t, facts.Replay, "no replay recorded yet")
	require.Empty(t, facts.Shadow.ExpectedModels)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStageSwitchStore_FreezeDerivedOnlyTouchesDerivedRows(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectExec(`UPDATE model_group_prices SET source = 'legacy_frozen' WHERE group_id = \$1 AND source = 'legacy_derived'`).
		WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec(`UPDATE cost_accounting_rules SET source = 'legacy_frozen' WHERE scope_group_id = \$1 AND source = 'legacy_derived'`).
		WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 2))
	cells, rules, err := store.FreezeDerived(context.Background(), db, 7)
	require.NoError(t, err)
	require.EqualValues(t, 4, cells)
	require.EqualValues(t, 2, rules)

	mock.ExpectExec(`UPDATE model_group_prices`).WillReturnError(errors.New("boom"))
	_, _, err = store.FreezeDerived(context.Background(), db, 7)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStageSwitchStore_ArchiveNonDerived(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(`FROM model_group_prices WHERE group_id = ANY`).WillReturnRows(sqlmock.NewRows(stageCellCols).
		AddRow(int64(1), int64(7), "gpt-5.4", false, 0, true, "inherit", nil, nil, nil, nil, "legacy_derived", int64(1), ssNow).
		AddRow(int64(2), int64(7), "gpt-5.5", false, 0, true, "inherit", nil, nil, nil, nil, "legacy_frozen", int64(1), ssNow).
		AddRow(int64(3), int64(7), "edited", false, 0, true, "extra", 2.0, nil, nil, nil, "manual", int64(2), ssNow))
	mock.ExpectExec(`INSERT INTO model_group_price_history .* 'archive'`).
		WithArgs(int64(7), "gpt-5.5", false, sqlmock.AnyArg(), int64(42), int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO model_group_price_history .* 'archive'`).
		WithArgs(int64(7), "edited", false, sqlmock.AnyArg(), int64(42), int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM model_group_prices WHERE group_id = \$1 AND source <> 'legacy_derived'`).
		WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(`FROM cost_accounting_rules WHERE scope_group_id = ANY`).WillReturnRows(sqlmock.NewRows(stageRuleCols).
		AddRow(int64(5), int64(7), "legacy_derived", int64(3), int64(0), "a", "{}", "{}", 0, true).
		AddRow(int64(6), int64(7), "manual", nil, nil, "m", "{}", "{1}", 1, true))
	mock.ExpectQuery(`FROM cost_accounting_rule_prices`).WillReturnRows(sqlmock.NewRows([]string{"rule_id", "platform", "models", "price"}))
	mock.ExpectExec(`DELETE FROM cost_accounting_rules WHERE scope_group_id = \$1 AND source <> 'legacy_derived'`).
		WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))

	got, err := store.ArchiveNonDerived(context.Background(), db, 7, 42, 9)
	require.NoError(t, err)
	require.EqualValues(t, 2, got.Cells)
	require.EqualValues(t, 1, got.Rules)
	require.Contains(t, string(got.RulesJSON), `"manual"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStageSwitchStore_ArchiveNothingDeletesNothingExtra(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(`FROM model_group_prices WHERE group_id = ANY`).WillReturnRows(sqlmock.NewRows(stageCellCols))
	mock.ExpectExec(`DELETE FROM model_group_prices`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM cost_accounting_rules WHERE scope_group_id = ANY`).WillReturnRows(sqlmock.NewRows(stageRuleCols))
	got, err := store.ArchiveNonDerived(context.Background(), db, 7, 42, 0)
	require.NoError(t, err)
	require.Zero(t, got.Cells)
	require.Zero(t, got.Rules)
	require.JSONEq(t, `[]`, string(got.RulesJSON))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStageSwitchStore_SetStageBumpsTheRevision(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(`UPDATE group_model_config\s+SET pricing_stage = \$2, stage_changed_at = \$3, stage_changed_by = \$4, revision = revision \+ 1.*RETURNING revision`).
		WithArgs(int64(7), "v2", ssNow, int64(42)).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(6)))
	rev, err := store.SetStage(context.Background(), db, 7, service.PricingStageV2, 42, ssNow)
	require.NoError(t, err)
	require.EqualValues(t, 6, rev)

	mock.ExpectQuery(`UPDATE group_model_config`).WillReturnError(sql.ErrNoRows)
	_, err = store.SetStage(context.Background(), db, 7, service.PricingStageV2, 42, ssNow)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStageSwitchStore_InsertAndListAudit(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	mock.ExpectQuery(`INSERT INTO pricing_stage_audit .* RETURNING id`).
		WithArgs(int64(7), "shadow", "v2", "advance", int64(42), true, int64(9), "unknown", int64(5), int64(6), `{"gate":{}}`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(77)))
	id, err := store.InsertAudit(context.Background(), db, service.StageAuditRecord{
		GroupID: 7, From: service.PricingStageShadow, To: service.PricingStageV2, Kind: service.StageKindAdvance,
		Actor:      service.PriceWriteActorFromAuthMethod(42, service.AuditAuthMethodJWT),
		ApprovalID: 9, PriceDelta: service.PriceDeltaUnknown, RevisionBefore: 5, RevisionAfter: 6, Evidence: []byte(`{"gate":{}}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 77, id)

	// 没有凭证（回拨）写 NULL，没有证据写空对象。
	mock.ExpectQuery(`INSERT INTO pricing_stage_audit`).
		WithArgs(int64(7), "v2", "legacy", "rollback", int64(42), false, nil, "none", int64(6), int64(7), `{}`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(78)))
	_, err = store.InsertAudit(context.Background(), db, service.StageAuditRecord{
		GroupID: 7, From: service.PricingStageV2, To: service.PricingStageLegacy, Kind: service.StageKindRollback,
		Actor: service.PriceWriteActorFromAuthMethod(42, "admin_token"), PriceDelta: service.PriceDeltaNone, RevisionBefore: 6, RevisionAfter: 7,
	})
	require.NoError(t, err)

	mock.ExpectQuery(`FROM pricing_stage_audit WHERE group_id = \$1 ORDER BY created_at DESC, id DESC LIMIT \$2`).WithArgs(int64(7), 5).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "group_id", "from_stage", "to_stage", "kind", "operator_id", "interactive", "approval_id",
			"price_delta", "config_revision_before", "config_revision_after", "evidence"}).
			AddRow(int64(78), ssNow, int64(7), "v2", "legacy", "rollback", int64(42), false, int64(0), "none", int64(6), int64(7), []byte(`{}`)))
	items, err := store.ListAudit(context.Background(), db, 7, 5)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, service.PricingStageV2, items[0].From)
	require.Equal(t, service.PricingStageLegacy, items[0].To)
	require.False(t, items[0].Interactive)
	require.Zero(t, items[0].ApprovalID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStageSwitchStore_RecentUsageModels(t *testing.T) {
	store, db, mock := newStageSwitchMock(t)
	since := ssNow.Add(-7 * 24 * time.Hour)
	mock.ExpectQuery(`FROM usage_logs WHERE group_id = \$1 AND created_at >= \$2 GROUP BY 1`).WithArgs(int64(7), since).
		WillReturnRows(sqlmock.NewRows([]string{"m", "n"}).AddRow("gpt-5.4", int64(10)).AddRow("", int64(2)))
	got, err := store.RecentUsageModels(context.Background(), db, 7, since)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"gpt-5.4": 10}, got, "rows without a model name are skipped")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReplayEvidenceStore_RecordsEveryGroupInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewPricingReplayEvidenceStore(db)
	require.NoError(t, store.RecordReplayEvidence(context.Background(), nil), "nothing to record")

	row := service.ReplayEvidence{GroupID: 7, RecordedAt: ssNow, WindowFrom: ssNow.Add(-30 * 24 * time.Hour), WindowTo: ssNow, MatrixSource: "derived",
		Passed: true, BindingStable: true, ChannelConfigHash: "c", DeriveRevision: "r", Diffs: []service.PricingReplayDiffCount{}}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO pricing_replay_evidence`).WithArgs(int64(7), ssNow, sqlmock.AnyArg(), sqlmock.AnyArg(), "derived", true, true,
		int64(0), int64(0), int64(0), int64(0), int64(0), "c", "r", "", "", "", `[]`).WillReturnResult(sqlmock.NewResult(1, 1))
	row2 := row
	row2.GroupID = 8
	mock.ExpectExec(`INSERT INTO pricing_replay_evidence`).WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	require.Error(t, store.RecordReplayEvidence(context.Background(), []service.ReplayEvidence{row, row2}))

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO pricing_replay_evidence`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	require.NoError(t, store.RecordReplayEvidence(context.Background(), []service.ReplayEvidence{row}))
	require.NoError(t, mock.ExpectationsWereMet())
}
