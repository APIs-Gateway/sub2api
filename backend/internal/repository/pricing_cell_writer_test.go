package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-1：单元格写入器（sqlmock，覆盖语句形状、顺序与每个错误分支；真实 PostgreSQL 的行为见集成测试）。

// pwxRow 一行 sqlmock 数据（sqlmock 的 AddRow 要的是 []driver.Value，不是 []any）。
type pwxRow = []driver.Value

// 同一个写入器既跑在 *sql.Tx 里，也跑在 ent 事务里（ent 生成时开了 sql/execquery，*ent.Tx 带 ExecContext / QueryContext）。
var (
	_ service.MatrixExecutor = (*sql.DB)(nil)
	_ service.MatrixExecutor = (*sql.Tx)(nil)
	_ service.MatrixExecutor = (*dbent.Tx)(nil)
)

var pwxStateCols = []string{"group_id", "pricing_stage", "revision"}

var pwxTS = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

func pwxTx(t *testing.T) (*sql.Tx, sqlmock.Sqlmock) {
	t.Helper()
	db, mock := newSQLMock(t)
	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)
	return tx, mock
}

func pwxFloat(v float64) *float64 { return &v }

func pwxOp(group int64, key string, open bool, mode service.MatrixPriceMode, baseline int64) service.CellOp {
	return service.CellOp{GroupID: group, ModelKey: key, Kind: service.CellOpUpsert, Open: open, PriceMode: mode, BaselineRevision: baseline}
}

func pwxDelete(group int64, key string, baseline int64) service.CellOp {
	return service.CellOp{GroupID: group, ModelKey: key, Kind: service.CellOpDelete, BaselineRevision: baseline}
}

func pwxRequest(groupRevs map[int64]int64, ops ...service.CellOp) service.CellWriteRequest {
	return service.CellWriteRequest{Ops: ops, GroupRevisions: groupRevs, OperatorID: 7, ApprovalID: 8}
}

func pwxExpectStates(mock sqlmock.Sqlmock, lock bool, rows ...pwxRow) {
	r := sqlmock.NewRows(pwxStateCols)
	for _, row := range rows {
		r.AddRow(row...)
	}
	pattern := `FROM group_model_config c\s+JOIN groups g ON g.id = c.group_id AND g.deleted_at IS NULL\s+WHERE c.group_id = ANY\(\$1\) ORDER BY c.group_id`
	if lock {
		pattern += ` FOR UPDATE OF c`
	} else {
		pattern += `$`
	}
	mock.ExpectQuery(pattern).WillReturnRows(r)
}

func pwxCellRow(id, group int64, key string, rev int64, open bool, mode string, extra any, source string) pwxRow {
	return pwxRow{id, group, key, false, 0, open, mode, extra, nil, nil, nil, source, rev, pwxTS}
}

func pwxExpectCells(mock sqlmock.Sqlmock, rows ...pwxRow) {
	r := sqlmock.NewRows(matrixCellCols)
	for _, row := range rows {
		r.AddRow(row...)
	}
	mock.ExpectQuery(`FROM model_group_prices\s+WHERE is_pattern = FALSE AND \(group_id, model_key\) IN\s+\(SELECT t.group_id, t.model_key FROM unnest\(\$1::bigint\[\], \$2::text\[\]\)`).
		WillReturnRows(r)
}

func TestPricingCellWriter_ApplyTx_WritesCellsHistoryAndBumpsRevision(t *testing.T) {
	tx, mock := pwxTx(t)

	pwxExpectStates(mock, true, pwxRow{int64(1), "v2", int64(3)}, pwxRow{int64(2), "v2", int64(1)})
	pwxExpectCells(mock,
		pwxCellRow(11, 1, "gone-cell", 6, true, "inherit", nil, "manual"),
		pwxCellRow(12, 1, "price-cell", 5, true, "inherit", nil, "legacy_frozen"),
		pwxCellRow(13, 1, "same-cell", 2, true, "inherit", nil, "manual"),
	)
	// 规范化后按 (group_id, model_key) 排序：gone-cell、new-cell、price-cell、same-cell（noop）、other-cell。
	mock.ExpectExec(`DELETE FROM model_group_prices WHERE id = \$1`).WithArgs(int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO model_group_price_history .*VALUES \(\$1, \$2, FALSE, \$3, \$4::jsonb, \$5::jsonb, \$6, \$7, \$8\)`).
		WithArgs(int64(1), "gone-cell", "delete", sqlmock.AnyArg(), nil, int64(7), nil, int64(8)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO model_group_prices\s+\(group_id, model_key, is_pattern, pattern_order, open, price_mode, extra_multiplier, custom_price, source\)\s+VALUES \(\$1, \$2, FALSE, 0, \$3, \$4, \$5, \$6::jsonb, \$7\)`).
		WithArgs(int64(1), "new-cell", true, "inherit", nil, nil, "manual").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO model_group_price_history`).
		WithArgs(int64(1), "new-cell", "create", nil, sqlmock.AnyArg(), int64(7), nil, int64(8)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE model_group_prices\s+SET open = \$2, price_mode = \$3, extra_multiplier = \$4, custom_price = \$5::jsonb, source = \$6,\s+revision = revision \+ 1, updated_at = NOW\(\)\s+WHERE id = \$1`).
		WithArgs(int64(12), true, "extra", 1.5, nil, "manual").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO model_group_price_history`).
		WithArgs(int64(1), "price-cell", "update", sqlmock.AnyArg(), sqlmock.AnyArg(), int64(7), nil, int64(8)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO model_group_prices`).
		WithArgs(int64(2), "other-cell", false, "inherit", nil, nil, "manual").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO model_group_price_history`).
		WithArgs(int64(2), "other-cell", "create", nil, sqlmock.AnyArg(), int64(7), nil, int64(8)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE group_model_config SET revision = revision \+ 1, updated_at = NOW\(\) WHERE group_id = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))

	priceCell := pwxOp(1, "price-cell", true, service.MatrixPriceExtra, 5)
	priceCell.ExtraMultiplier = pwxFloat(1.5)
	res, err := NewPricingCellWriter().ApplyTx(context.Background(), tx, pwxRequest(
		map[int64]int64{1: 3, 2: 1},
		pwxOp(2, "other-cell", false, service.MatrixPriceInherit, 0),
		priceCell,
		pwxOp(1, "same-cell", true, service.MatrixPriceInherit, 2),
		pwxOp(1, "NEW-cell", true, service.MatrixPriceInherit, 0),
		pwxDelete(1, "gone-cell", 6),
	))
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, res.ChangedGroupIDs)
	require.True(t, res.TouchesPrice)
	require.Len(t, res.Planned, 5)
	actions := map[string]service.CellWriteAction{}
	for _, p := range res.Planned {
		actions[p.Op.ModelKey] = p.Action
	}
	require.Equal(t, map[string]service.CellWriteAction{
		"gone-cell": service.CellWriteDelete, "new-cell": service.CellWriteCreate, "price-cell": service.CellWriteUpdate,
		"same-cell": service.CellWriteNoop, "other-cell": service.CellWriteCreate,
	}, actions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingCellWriter_ApplyTx_AllNoopWritesNothing(t *testing.T) {
	tx, mock := pwxTx(t)
	pwxExpectStates(mock, true, pwxRow{int64(1), "v2", int64(3)})
	pwxExpectCells(mock, pwxCellRow(13, 1, "same-cell", 2, true, "inherit", nil, "manual"))

	res, err := NewPricingCellWriter().ApplyTx(context.Background(), tx, pwxRequest(map[int64]int64{1: 3},
		pwxOp(1, "same-cell", true, service.MatrixPriceInherit, 2), pwxDelete(1, "never-existed", 0)))
	require.NoError(t, err)
	require.Empty(t, res.ChangedGroupIDs, "没有变化就不写历史、不动分组 revision")
	require.False(t, res.TouchesPrice)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingCellWriter_PlanTx_ReadsWithoutLockingOrWriting(t *testing.T) {
	db, mock := newSQLMock(t)
	pwxExpectStates(mock, false, pwxRow{int64(1), "v2", int64(3)})
	pwxExpectCells(mock)

	planned, err := NewPricingCellWriter().PlanTx(context.Background(), db, pwxRequest(map[int64]int64{1: 3},
		pwxOp(1, "new-cell", true, service.MatrixPriceInherit, 0)))
	require.NoError(t, err)
	require.Len(t, planned, 1)
	require.Equal(t, service.CellWriteCreate, planned[0].Action)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingCellWriter_RejectsBeforeAnyWrite(t *testing.T) {
	reasonOf := func(t *testing.T, err error) string {
		t.Helper()
		var ae *infraerrors.ApplicationError
		require.True(t, errors.As(err, &ae), "expected an application error, got %v", err)
		return ae.Reason
	}
	op := pwxOp(1, "m", true, service.MatrixPriceInherit, 0)

	t.Run("invalid request never reaches the database", func(t *testing.T) {
		tx, mock := pwxTx(t)
		_, err := NewPricingCellWriter().ApplyTx(context.Background(), tx, pwxRequest(map[int64]int64{1: 1}))
		require.Equal(t, service.ReasonCellOpsEmpty, reasonOf(t, err))
		require.NoError(t, mock.ExpectationsWereMet())
	})
	for name, states := range map[string][]pwxRow{
		"no config row (legacy default)": {},
		"legacy":                         {{int64(1), "legacy", int64(3)}},
		"shadow":                         {{int64(1), "shadow", int64(3)}},
	} {
		t.Run(name, func(t *testing.T) {
			tx, mock := pwxTx(t)
			pwxExpectStates(mock, true, states...)
			_, err := NewPricingCellWriter().ApplyTx(context.Background(), tx, pwxRequest(map[int64]int64{1: 3}, op))
			require.Equal(t, service.ReasonCellGroupNotV2, reasonOf(t, err))
			require.NoError(t, mock.ExpectationsWereMet(), "没有读单元格，也没有写任何东西")
		})
	}
	t.Run("group revision moved", func(t *testing.T) {
		tx, mock := pwxTx(t)
		pwxExpectStates(mock, true, pwxRow{int64(1), "v2", int64(4)})
		_, err := NewPricingCellWriter().ApplyTx(context.Background(), tx, pwxRequest(map[int64]int64{1: 3}, op))
		require.Equal(t, service.ReasonPriceBaselineChanged, reasonOf(t, err))
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("cell revision moved", func(t *testing.T) {
		tx, mock := pwxTx(t)
		pwxExpectStates(mock, true, pwxRow{int64(1), "v2", int64(3)})
		pwxExpectCells(mock, pwxCellRow(11, 1, "m", 9, true, "inherit", nil, "manual"))
		_, err := NewPricingCellWriter().ApplyTx(context.Background(), tx, pwxRequest(map[int64]int64{1: 3}, op))
		require.Equal(t, service.ReasonPriceBaselineChanged, reasonOf(t, err))
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestPricingCellWriter_ReadFailures(t *testing.T) {
	writer := NewPricingCellWriter()
	req := pwxRequest(map[int64]int64{1: 3}, pwxOp(1, "m", true, service.MatrixPriceInherit, 0))
	boom := errors.New("boom")

	cases := []struct {
		name   string
		expect func(mock sqlmock.Sqlmock)
		want   string
	}{
		{"states query", func(m sqlmock.Sqlmock) { m.ExpectQuery(`FROM group_model_config c`).WillReturnError(boom) }, "query group_model_config states"},
		{"states scan", func(m sqlmock.Sqlmock) {
			m.ExpectQuery(`FROM group_model_config c`).WillReturnRows(sqlmock.NewRows(pwxStateCols).AddRow(int64(1), "v2", "not-a-number"))
		}, "scan group_model_config state"},
		{"states iterate", func(m sqlmock.Sqlmock) {
			m.ExpectQuery(`FROM group_model_config c`).WillReturnRows(sqlmock.NewRows(pwxStateCols).AddRow(int64(1), "v2", int64(3)).RowError(0, boom))
		}, "iterate group_model_config states"},
		{"cells query", func(m sqlmock.Sqlmock) {
			pwxExpectStates(m, true, pwxRow{int64(1), "v2", int64(3)})
			m.ExpectQuery(`FROM model_group_prices`).WillReturnError(boom)
		}, "query model_group_prices for write"},
		{"cells scan", func(m sqlmock.Sqlmock) {
			pwxExpectStates(m, true, pwxRow{int64(1), "v2", int64(3)})
			m.ExpectQuery(`FROM model_group_prices`).WillReturnRows(sqlmock.NewRows(matrixCellCols).
				AddRow("x", int64(1), "m", false, 0, true, "inherit", nil, nil, nil, nil, "manual", int64(1), pwxTS))
		}, "scan model_group_prices"},
		{"cells decode", func(m sqlmock.Sqlmock) {
			pwxExpectStates(m, true, pwxRow{int64(1), "v2", int64(3)})
			m.ExpectQuery(`FROM model_group_prices`).WillReturnRows(sqlmock.NewRows(matrixCellCols).
				AddRow(int64(5), int64(1), "m", false, 0, true, "custom", nil, []byte("{not json"), nil, nil, "manual", int64(1), pwxTS))
		}, "decode custom_price of cell 5"},
		{"cells iterate", func(m sqlmock.Sqlmock) {
			pwxExpectStates(m, true, pwxRow{int64(1), "v2", int64(3)})
			m.ExpectQuery(`FROM model_group_prices`).WillReturnRows(sqlmock.NewRows(matrixCellCols).
				AddRow(int64(5), int64(1), "m", false, 0, true, "inherit", nil, nil, nil, nil, "manual", int64(1), pwxTS).RowError(0, boom))
		}, "iterate model_group_prices"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, mock := pwxTx(t)
			tc.expect(mock)
			_, err := writer.ApplyTx(context.Background(), tx, req)
			require.ErrorContains(t, err, tc.want)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPricingCellWriter_WriteFailures(t *testing.T) {
	writer := NewPricingCellWriter()
	boom := errors.New("boom")
	history := `INSERT INTO model_group_price_history`

	create := pwxRequest(map[int64]int64{1: 3}, pwxOp(1, "m", true, service.MatrixPriceInherit, 0))
	update := pwxRequest(map[int64]int64{1: 3}, pwxOp(1, "m", false, service.MatrixPriceInherit, 4))
	remove := pwxRequest(map[int64]int64{1: 3}, pwxDelete(1, "m", 4))
	existing := pwxCellRow(11, 1, "m", 4, true, "inherit", nil, "manual")

	cases := []struct {
		name   string
		req    service.CellWriteRequest
		cells  []pwxRow
		expect func(mock sqlmock.Sqlmock)
		want   string
	}{
		{"insert", create, nil, func(m sqlmock.Sqlmock) { m.ExpectExec(`INSERT INTO model_group_prices`).WillReturnError(boom) }, `insert cell "m"`},
		{"update", update, []pwxRow{existing}, func(m sqlmock.Sqlmock) { m.ExpectExec(`UPDATE model_group_prices`).WillReturnError(boom) }, "update cell 11"},
		{"delete", remove, []pwxRow{existing}, func(m sqlmock.Sqlmock) { m.ExpectExec(`DELETE FROM model_group_prices`).WillReturnError(boom) }, "delete cell 11"},
		{"history", create, nil, func(m sqlmock.Sqlmock) {
			m.ExpectExec(`INSERT INTO model_group_prices`).WillReturnResult(sqlmock.NewResult(1, 1))
			m.ExpectExec(history).WillReturnError(boom)
		}, "insert cell history"},
		{"bump", create, nil, func(m sqlmock.Sqlmock) {
			m.ExpectExec(`INSERT INTO model_group_prices`).WillReturnResult(sqlmock.NewResult(1, 1))
			m.ExpectExec(history).WillReturnResult(sqlmock.NewResult(1, 1))
			m.ExpectExec(`UPDATE group_model_config SET revision`).WillReturnError(boom)
		}, "bump group_model_config revision"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, mock := pwxTx(t)
			pwxExpectStates(mock, true, pwxRow{int64(1), "v2", int64(3)})
			pwxExpectCells(mock, tc.cells...)
			tc.expect(mock)
			_, err := writer.ApplyTx(context.Background(), tx, tc.req)
			require.ErrorIs(t, err, boom)
			require.ErrorContains(t, err, tc.want)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPricingCellWriter_UnserializableCustomPriceIsAnErrorNotAWrite(t *testing.T) {
	writer := NewPricingCellWriter()
	nan := &service.MatrixCustomPrice{InputPrice: pwxFloat(math.NaN())}
	op := pwxOp(1, "m", true, service.MatrixPriceCustom, 0)
	op.CustomPrice = nan

	t.Run("create", func(t *testing.T) {
		tx, mock := pwxTx(t)
		pwxExpectStates(mock, true, pwxRow{int64(1), "v2", int64(3)})
		pwxExpectCells(mock)
		_, err := writer.ApplyTx(context.Background(), tx, pwxRequest(map[int64]int64{1: 3}, op))
		require.ErrorContains(t, err, "marshal custom_price")
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("update", func(t *testing.T) {
		tx, mock := pwxTx(t)
		pwxExpectStates(mock, true, pwxRow{int64(1), "v2", int64(3)})
		pwxExpectCells(mock, pwxCellRow(11, 1, "m", 4, true, "inherit", nil, "manual"))
		update := op
		update.BaselineRevision = 4
		_, err := writer.ApplyTx(context.Background(), tx, pwxRequest(map[int64]int64{1: 3}, update))
		require.ErrorContains(t, err, "marshal custom_price")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
