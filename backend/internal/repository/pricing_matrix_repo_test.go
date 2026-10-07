package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var (
	matrixConfigCols = []string{"group_id", "access_mode", "billing_model_source", "model_mapping", "features", "cost_mode",
		"pricing_stage", "stage_changed_at", "stage_changed_by", "revision", "updated_at"}
	matrixCellCols = []string{"id", "group_id", "model_key", "is_pattern", "pattern_order", "open", "price_mode", "extra_multiplier",
		"custom_price", "effective_from", "effective_to", "source", "revision", "updated_at"}
	matrixRuleCols  = []string{"id", "scope_group_id", "source", "source_channel_id", "source_ordinal", "name", "group_ids", "account_ids", "sort_order", "enabled"}
	matrixPriceCols = []string{"rule_id", "platform", "models", "price"}
)

func matrixPtrFloat(v float64) *float64 { return &v }

// expectEmptySnapshotReads 一个分组没有任何现状的三次读取（配置、单元格、成本核算规则）。
func expectEmptySnapshotReads(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`FROM group_model_config WHERE group_id = ANY`).WillReturnRows(sqlmock.NewRows(matrixConfigCols))
	mock.ExpectQuery(`FROM model_group_prices WHERE group_id = ANY`).WillReturnRows(sqlmock.NewRows(matrixCellCols))
	mock.ExpectQuery(`FROM cost_accounting_rules WHERE scope_group_id = ANY`).WillReturnRows(sqlmock.NewRows(matrixRuleCols))
}

// expectDeriveAdvisoryLock 事务里 lock_timeout 之后、行锁之前的那条咨询锁。
func expectDeriveAdvisoryLock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(pricingDeriveAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestPricingMatrixRepo_GetGroupMeta(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)

	empty, err := repo.GetGroupMeta(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, empty, "没有分组时不查询")

	mock.ExpectQuery(`SELECT id, platform, deleted_at IS NOT NULL FROM groups WHERE id = ANY`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "deleted"}).
			AddRow(int64(1), "openai", false).
			AddRow(int64(2), "anthropic", true))
	got, err := repo.GetGroupMeta(context.Background(), []int64{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, map[int64]service.DeriveGroup{
		1: {ID: 1, Platform: "openai"},
		2: {ID: 2, Platform: "anthropic", Deleted: true},
	}, got, "不存在的分组不出现在结果里；软删除的带标记")

	mock.ExpectQuery(`FROM groups`).WillReturnError(errors.New("boom"))
	_, err = repo.GetGroupMeta(context.Background(), []int64{1})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingMatrixRepo_ListDerivedRuleGroupIDs(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)

	mock.ExpectQuery(`SELECT DISTINCT scope_group_id FROM cost_accounting_rules\s+WHERE source = 'legacy_derived' AND source_channel_id = \$1`).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"scope_group_id"}).AddRow(int64(10)).AddRow(int64(11)))
	got, err := repo.ListDerivedRuleGroupIDs(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, []int64{10, 11}, got)

	mock.ExpectQuery(`FROM cost_accounting_rules`).WillReturnError(errors.New("boom"))
	_, err = repo.ListDerivedRuleGroupIDs(context.Background(), 5)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingMatrixRepo_ListDerivedRuleChannels(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)

	mock.ExpectQuery(`SELECT DISTINCT source_channel_id, scope_group_id FROM cost_accounting_rules\s+WHERE source = 'legacy_derived' AND source_channel_id IS NOT NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"source_channel_id", "scope_group_id"}).
			AddRow(int64(5), int64(10)).AddRow(int64(5), int64(11)).AddRow(int64(6), int64(20)))
	got, err := repo.ListDerivedRuleChannels(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[int64][]int64{5: {10, 11}, 6: {20}}, got)

	mock.ExpectQuery(`FROM cost_accounting_rules`).WillReturnError(errors.New("boom"))
	_, err = repo.ListDerivedRuleChannels(context.Background())
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingMatrixRepo_LoadGroupSnapshots_DecodesAllTables(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)
	ts := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	changedAt := ts.Add(-time.Hour)

	mock.ExpectQuery(`FROM group_model_config WHERE group_id = ANY`).WillReturnRows(sqlmock.NewRows(matrixConfigCols).
		AddRow(int64(10), "allowlist", "requested", []byte(`[{"src":"a*","dst":"b"}]`), []byte(`{"bedrock_cc_compat":true}`), "follow_billing",
			"shadow", changedAt, int64(3), int64(4), ts).
		AddRow(int64(11), "open", nil, []byte(`[]`), []byte(`{}`), "account_rate", "legacy", nil, nil, int64(1), ts))
	mock.ExpectQuery(`FROM model_group_prices WHERE group_id = ANY`).WillReturnRows(sqlmock.NewRows(matrixCellCols).
		AddRow(int64(100), int64(10), "gpt-5.5", false, 0, true, "custom", nil, []byte(`{"billing_mode":"token","input_price":0.000001}`), nil, nil, "legacy_derived", int64(2), ts).
		AddRow(int64(101), int64(10), "gpt-5", true, 1, false, "extra", 1.25, nil, ts, ts.Add(time.Hour), "manual", int64(1), ts))
	mock.ExpectQuery(`FROM cost_accounting_rules WHERE scope_group_id = ANY`).WillReturnRows(sqlmock.NewRows(matrixRuleCols).
		AddRow(int64(200), int64(10), "legacy_derived", int64(7), int64(1), "rule", "{1,2}", "{}", 5, true).
		AddRow(int64(201), int64(10), "manual", nil, nil, "mine", "{}", "{9}", 0, false))
	mock.ExpectQuery(`FROM cost_accounting_rule_prices\s+WHERE rule_id = ANY`).WillReturnRows(sqlmock.NewRows(matrixPriceCols).
		AddRow(int64(200), "openai", []byte(`["gpt-5.5","gpt-5*"]`), []byte(`{"billing_mode":"per_request","per_request_price":0.5}`)))

	snaps, err := repo.LoadGroupSnapshots(context.Background(), []int64{10, 11, 12})
	require.NoError(t, err)
	require.Len(t, snaps, 3)

	cfg := snaps[10].Config
	require.NotNil(t, cfg)
	require.Equal(t, service.MatrixAccessAllowlist, cfg.AccessMode)
	require.Equal(t, "requested", *cfg.BillingModelSource)
	require.Equal(t, []service.MatrixMappingEntry{{Src: "a*", Dst: "b"}}, cfg.ModelMapping)
	require.Equal(t, map[string]any{"bedrock_cc_compat": true}, cfg.Features)
	require.Equal(t, service.MatrixCostFollowBilling, cfg.CostMode)
	require.Equal(t, service.PricingStageShadow, cfg.PricingStage)
	require.Equal(t, changedAt, *cfg.StageChangedAt)
	require.Equal(t, int64(3), *cfg.StageChangedBy)
	require.Equal(t, int64(4), cfg.Revision)

	cfg11 := snaps[11].Config
	require.Nil(t, cfg11.BillingModelSource, "NULL 表示无渠道")
	require.Nil(t, cfg11.StageChangedAt)
	require.Equal(t, []service.MatrixMappingEntry{}, cfg11.ModelMapping)
	require.Equal(t, map[string]any{}, cfg11.Features)

	require.Nil(t, snaps[12].Config, "没有行 = 默认值")
	require.Empty(t, snaps[12].Cells)

	require.Len(t, snaps[10].Cells, 2)
	derived := snaps[10].Cells[0]
	require.Equal(t, service.MatrixPriceCustom, derived.PriceMode)
	require.Equal(t, service.MatrixSourceLegacyDerived, derived.Source)
	require.Equal(t, service.BillingModeToken, derived.CustomPrice.BillingMode)
	require.Equal(t, matrixPtrFloat(0.000001), derived.CustomPrice.InputPrice)
	manual := snaps[10].Cells[1]
	require.True(t, manual.IsPattern)
	require.False(t, manual.Open)
	require.Equal(t, matrixPtrFloat(1.25), manual.ExtraMultiplier)
	require.Nil(t, manual.CustomPrice)
	require.Equal(t, ts, *manual.EffectiveFrom)
	require.Equal(t, ts.Add(time.Hour), *manual.EffectiveTo)

	require.Len(t, snaps[10].Rules, 2)
	rule := snaps[10].Rules[0]
	require.Equal(t, int64(7), rule.SourceChannelID)
	require.Equal(t, 1, rule.SourceOrdinal)
	require.Equal(t, []int64{1, 2}, rule.GroupIDs)
	require.Equal(t, []int64{}, rule.AccountIDs)
	require.Len(t, rule.Prices, 1)
	require.Equal(t, []string{"gpt-5.5", "gpt-5*"}, rule.Prices[0].Models)
	require.Equal(t, service.BillingModePerRequest, rule.Prices[0].Price.BillingMode)
	manualRule := snaps[10].Rules[1]
	require.Equal(t, service.MatrixSourceManual, manualRule.Source)
	require.Zero(t, manualRule.SourceChannelID)
	require.Zero(t, manualRule.SourceOrdinal)
	require.False(t, manualRule.Enabled)
	require.Equal(t, []service.MatrixCostRulePrice{}, manualRule.Prices)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingMatrixRepo_LoadGroupSnapshots_NoGroupsAndErrors(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)

	snaps, err := repo.LoadGroupSnapshots(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, snaps)

	mock.ExpectQuery(`FROM group_model_config`).WillReturnRows(sqlmock.NewRows(matrixConfigCols).
		AddRow(int64(10), "open", nil, []byte(`not json`), []byte(`{}`), "account_rate", "legacy", nil, nil, int64(1), time.Now()))
	_, err = repo.LoadGroupSnapshots(context.Background(), []int64{10})
	require.ErrorContains(t, err, "decode model_mapping")

	mock.ExpectQuery(`FROM group_model_config`).WillReturnRows(sqlmock.NewRows(matrixConfigCols))
	mock.ExpectQuery(`FROM model_group_prices`).WillReturnError(errors.New("boom"))
	_, err = repo.LoadGroupSnapshots(context.Background(), []int64{10})
	require.ErrorContains(t, err, "model_group_prices")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// ApplyPlans：加锁、读快照、计划、执行，同一个事务
// ---------------------------------------------------------------------------

func TestPricingMatrixRepo_ApplyPlans_LocksThenReadsThenWritesInOneTx(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)
	bms := "channel_mapped"
	price := service.MatrixCustomPrice{BillingMode: service.BillingModeToken, InputPrice: matrixPtrFloat(0.000002)}

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout = '5s'`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectDeriveAdvisoryLock(mock)
	mock.ExpectQuery(`SELECT group_id FROM group_model_config WHERE group_id = ANY\(\$1\) ORDER BY group_id FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	expectEmptySnapshotReads(mock)
	mock.ExpectExec(`INSERT INTO group_model_config .* ON CONFLICT \(group_id\) DO UPDATE SET.*WHERE group_model_config.pricing_stage <> 'v2'`).
		WithArgs(int64(10), "allowlist", bms, `[{"src":"a*","dst":"b"}]`, `{"bedrock_cc_compat":true}`, "follow_billing").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO model_group_prices .* ON CONFLICT \(group_id, model_key, is_pattern\) DO NOTHING`).
		WithArgs(int64(10), "gpt-5.5", false, 0, true, "custom", nil, `{"billing_mode":"token","input_price":0.000002}`, "legacy_derived").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO model_group_prices`).
		WithArgs(int64(10), "gpt-5", true, 0, true, "inherit", nil, nil, "legacy_derived").
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectQuery(`INSERT INTO cost_accounting_rules .* RETURNING id`).
		WithArgs("rule", int64(10), "legacy_derived", int64(7), 1, sqlmock.AnyArg(), sqlmock.AnyArg(), 5, true).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(900)))
	mock.ExpectExec(`INSERT INTO cost_accounting_rule_prices`).
		WithArgs(int64(900), "openai", `["gpt-5.5"]`, `{"billing_mode":"token","input_price":0.000002}`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.ApplyPlans(context.Background(), []int64{10}, func(snaps map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
		require.Contains(t, snaps, int64(10), "计划在拿到快照之后才生成")
		return []service.GroupApplyPlan{{
			GroupID: 10,
			ConfigWrite: &service.MatrixGroupConfig{
				AccessMode: service.MatrixAccessAllowlist, BillingModelSource: &bms,
				ModelMapping: []service.MatrixMappingEntry{{Src: "a*", Dst: "b"}},
				Features:     map[string]any{"bedrock_cc_compat": true},
				CostMode:     service.MatrixCostFollowBilling,
			},
			CellInserts: []service.MatrixCell{
				{ModelKey: "gpt-5.5", Open: true, PriceMode: service.MatrixPriceCustom, CustomPrice: &price, Source: service.MatrixSourceLegacyDerived},
				{ModelKey: "gpt-5", IsPattern: true, Open: true, PriceMode: service.MatrixPriceInherit, Source: service.MatrixSourceLegacyDerived},
			},
			RuleInserts: []service.MatrixCostRule{{
				Name: "rule", SourceChannelID: 7, SourceOrdinal: 1, SortOrder: 5, Enabled: true,
				Prices: []service.MatrixCostRulePrice{{Platform: "openai", Models: []string{"gpt-5.5"}, Price: price}},
			}},
		}}, nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingMatrixRepo_ApplyPlans_UpdatesAndDeletesOnlyDerivedRows(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectDeriveAdvisoryLock(mock)
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	expectEmptySnapshotReads(mock)
	mock.ExpectExec(`DELETE FROM model_group_prices WHERE id = ANY\(\$1\) AND source = 'legacy_derived'`).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE model_group_prices\s+SET pattern_order = \$2, open = \$3, price_mode = \$4, extra_multiplier = \$5, custom_price = \$6::jsonb,\s+revision = revision \+ 1, updated_at = NOW\(\)\s+WHERE id = \$1 AND source = 'legacy_derived'`).
		WithArgs(int64(5), 2, true, "inherit", nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM cost_accounting_rules WHERE id = ANY\(\$1\) AND source = 'legacy_derived'`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.ApplyPlans(context.Background(), []int64{10}, func(map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
		return []service.GroupApplyPlan{{
			GroupID:     10,
			CellDeletes: []int64{3, 4},
			CellUpdates: []service.StoredMatrixCell{{ID: 5, MatrixCell: service.MatrixCell{ModelKey: "x", PatternOrder: 2, Open: true, PriceMode: service.MatrixPriceInherit, Source: service.MatrixSourceLegacyDerived}}},
			RuleDeletes: []int64{8},
		}}, nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 行在并发中被切到 v2：upsert 影响 0 行，该分组的其余写入一律不执行。
func TestPricingMatrixRepo_ApplyPlans_V2RowStopsTheWholeGroup(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectDeriveAdvisoryLock(mock)
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	expectEmptySnapshotReads(mock)
	mock.ExpectExec(`INSERT INTO group_model_config`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit() // 其后没有任何单元格、成本核算规则语句

	err := repo.ApplyPlans(context.Background(), []int64{10}, func(map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
		return []service.GroupApplyPlan{{
			GroupID:     10,
			ConfigWrite: &service.MatrixGroupConfig{AccessMode: service.MatrixAccessOpen, CostMode: service.MatrixCostAccountRate, ModelMapping: []service.MatrixMappingEntry{}, Features: map[string]any{}},
			CellInserts: []service.MatrixCell{{ModelKey: "x", Open: true, PriceMode: service.MatrixPriceInherit}},
			RuleDeletes: []int64{1},
		}}, nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingMatrixRepo_ApplyPlans_SkippedAndEmptyPlansWriteNothing(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingMatrixRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectDeriveAdvisoryLock(mock)
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	expectEmptySnapshotReads(mock)
	mock.ExpectCommit()

	err := repo.ApplyPlans(context.Background(), []int64{10, 11}, func(map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
		return []service.GroupApplyPlan{
			{GroupID: 10, Skipped: true, SkipReason: "pricing_stage_v2", CellInserts: []service.MatrixCell{{ModelKey: "ignored"}}},
			{GroupID: 11},
		}, nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingMatrixRepo_ApplyPlans_FailuresRollBack(t *testing.T) {
	t.Run("加锁超时", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		expectDeriveAdvisoryLock(mock)
		mock.ExpectQuery(`FOR UPDATE`).WillReturnError(errors.New("canceling statement due to lock timeout"))
		mock.ExpectRollback()
		err := NewPricingMatrixRepository(db).ApplyPlans(context.Background(), []int64{10}, func(map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
			t.Fatal("加锁失败后不应生成计划")
			return nil, nil
		})
		require.ErrorContains(t, err, "lock group_model_config rows")
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("咨询锁失败时不碰行锁", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pricingDeriveAdvisoryLockKey).WillReturnError(errors.New("canceling statement due to lock timeout"))
		mock.ExpectRollback()
		err := NewPricingMatrixRepository(db).ApplyPlans(context.Background(), []int64{10}, func(map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
			t.Fatal("拿不到咨询锁不应生成计划")
			return nil, nil
		})
		require.ErrorContains(t, err, "advisory lock")
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("计划生成失败", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		expectDeriveAdvisoryLock(mock)
		mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
		expectEmptySnapshotReads(mock)
		mock.ExpectRollback()
		boom := errors.New("plan failed")
		err := NewPricingMatrixRepository(db).ApplyPlans(context.Background(), []int64{10}, func(map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
			return nil, boom
		})
		require.ErrorIs(t, err, boom)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("写入失败整个事务回滚", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		expectDeriveAdvisoryLock(mock)
		mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
		expectEmptySnapshotReads(mock)
		mock.ExpectExec(`INSERT INTO model_group_prices`).WillReturnError(errors.New("check constraint violated"))
		mock.ExpectRollback()
		err := NewPricingMatrixRepository(db).ApplyPlans(context.Background(), []int64{10}, func(map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
			return []service.GroupApplyPlan{{GroupID: 10, CellInserts: []service.MatrixCell{{ModelKey: "x", Open: true, PriceMode: service.MatrixPriceInherit}}}}, nil
		})
		require.ErrorContains(t, err, "apply plan of group 10")
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("开启事务失败", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin().WillReturnError(errors.New("no connection"))
		err := NewPricingMatrixRepository(db).ApplyPlans(context.Background(), []int64{10}, nil)
		require.ErrorContains(t, err, "begin tx")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestMatrixCustomPriceParam(t *testing.T) {
	got, err := matrixCustomPriceParam(nil)
	require.NoError(t, err)
	require.Nil(t, got, "没有 custom_price 时写 NULL")

	got, err = matrixCustomPriceParam(&service.MatrixCustomPrice{BillingMode: service.BillingModeImage})
	require.NoError(t, err)
	require.Equal(t, `{"billing_mode":"image"}`, got, "空 custom 的 JSON 只带 billing_mode")
}

// 派生出的成本核算规则没有来源渠道/顺序（manual 才可能）时写 NULL。
func TestInsertMatrixCostRule_NullOrigin(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectQuery(`INSERT INTO cost_accounting_rules`).
		WithArgs("m", int64(10), "legacy_derived", nil, nil, sqlmock.AnyArg(), sqlmock.AnyArg(), 0, false).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	require.NoError(t, insertMatrixCostRule(context.Background(), db, 10, service.MatrixCostRule{Name: "m"}))
	require.NoError(t, mock.ExpectationsWereMet())
}
