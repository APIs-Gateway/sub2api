package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-1：过渡审批存储（sqlmock；原子消耗与并发行为见集成测试）。

var pwsNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestPricingWriteStore_ReaderIsTheDB(t *testing.T) {
	db, _ := newSQLMock(t)
	require.Same(t, db, NewPricingWriteStore(db).Reader())
}

func TestPricingWriteStore_WithTx(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	t.Run("commit", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout = '5s'`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE t`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		err := NewPricingWriteStore(db).WithTx(ctx, func(ctx context.Context, tx service.MatrixExecutor) error {
			_, err := tx.ExecContext(ctx, `UPDATE t SET x = 1`)
			return err
		})
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("begin fails", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin().WillReturnError(boom)
		err := NewPricingWriteStore(db).WithTx(ctx, func(context.Context, service.MatrixExecutor) error {
			t.Fatal("事务没开起来，不应运行回调")
			return nil
		})
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "begin tx")
	})
	t.Run("lock_timeout fails", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnError(boom)
		mock.ExpectRollback()
		err := NewPricingWriteStore(db).WithTx(ctx, func(context.Context, service.MatrixExecutor) error {
			t.Fatal("设置锁超时失败后不应运行回调")
			return nil
		})
		require.ErrorContains(t, err, "set lock_timeout")
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("callback error rolls back", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		err := NewPricingWriteStore(db).WithTx(ctx, func(context.Context, service.MatrixExecutor) error { return boom })
		require.Same(t, boom, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("commit fails", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit().WillReturnError(boom)
		err := NewPricingWriteStore(db).WithTx(ctx, func(context.Context, service.MatrixExecutor) error { return nil })
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "commit")
	})
	t.Run("panic rolls back and propagates", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		require.PanicsWithValue(t, "kaboom", func() {
			_ = NewPricingWriteStore(db).WithTx(ctx, func(context.Context, service.MatrixExecutor) error { panic("kaboom") })
		})
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestPricingWriteStore_InsertApproval(t *testing.T) {
	ctx := context.Background()
	a := service.PriceWriteApproval{
		Kind: service.PriceWriteKindCells, PlanHash: "h", TouchesPrice: true, Delta: service.PriceDeltaUp,
		GroupIDs: []int64{1, 2}, Summary: []byte(`[{"a":1}]`), PreviewedBy: 7, CreatedAt: pwsNow, ExpiresAt: pwsNow.Add(time.Minute),
	}

	db, mock := newSQLMock(t)
	mock.ExpectQuery(`INSERT INTO pricing_write_approvals\s+\(kind, plan_hash, touches_price, price_delta, group_ids, summary, previewed_by, created_at, expires_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6::jsonb, \$7, \$8, \$9\) RETURNING id`).
		WithArgs("cell_write", "h", true, "up", sqlmock.AnyArg(), `[{"a":1}]`, int64(7), pwsNow, pwsNow.Add(time.Minute)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))
	id, err := NewPricingWriteStore(db).InsertApproval(ctx, a)
	require.NoError(t, err)
	require.Equal(t, int64(42), id)

	// 没有摘要时写空对象。
	a.Summary = nil
	mock.ExpectQuery(`INSERT INTO pricing_write_approvals`).
		WithArgs("cell_write", "h", true, "up", sqlmock.AnyArg(), `{}`, int64(7), pwsNow, pwsNow.Add(time.Minute)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(43)))
	id, err = NewPricingWriteStore(db).InsertApproval(ctx, a)
	require.NoError(t, err)
	require.Equal(t, int64(43), id)

	mock.ExpectQuery(`INSERT INTO pricing_write_approvals`).WillReturnError(errors.New("boom"))
	_, err = NewPricingWriteStore(db).InsertApproval(ctx, a)
	require.ErrorContains(t, err, "insert pricing_write_approvals")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingWriteStore_ConsumeApproval(t *testing.T) {
	ctx := context.Background()
	reasonOf := func(t *testing.T, err error) string {
		t.Helper()
		var ae *infraerrors.ApplicationError
		require.True(t, errors.As(err, &ae), "expected an application error, got %v", err)
		return ae.Reason
	}
	updateSQL := `UPDATE pricing_write_approvals\s+SET status = 'consumed', approved_by = \$2, consumed_at = \$3\s+WHERE id = \$1 AND status = 'previewed' AND plan_hash = \$4 AND kind = \$5 AND expires_at > \$3\s+RETURNING touches_price, price_delta, previewed_by`
	selectSQL := `SELECT status, plan_hash, kind, expires_at FROM pricing_write_approvals WHERE id = \$1`
	consume := func(t *testing.T) (service.PriceWriteStore, sqlmock.Sqlmock, service.MatrixExecutor) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		tx, err := db.Begin()
		require.NoError(t, err)
		return NewPricingWriteStore(db), mock, tx
	}

	t.Run("consumed", func(t *testing.T) {
		store, mock, tx := consume(t)
		mock.ExpectQuery(updateSQL).WithArgs(int64(5), int64(9), pwsNow, "h", "cell_write").
			WillReturnRows(sqlmock.NewRows([]string{"touches_price", "price_delta", "previewed_by"}).AddRow(true, "unknown", int64(7)))
		a, err := store.ConsumeApproval(ctx, tx, 5, "h", "cell_write", 9, pwsNow)
		require.NoError(t, err)
		require.Equal(t, &service.PriceWriteApproval{
			ID: 5, Kind: "cell_write", PlanHash: "h", TouchesPrice: true, Delta: service.PriceDeltaUnknown, PreviewedBy: 7, ApprovedBy: 9,
		}, a)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("update fails", func(t *testing.T) {
		store, mock, tx := consume(t)
		mock.ExpectQuery(updateSQL).WillReturnError(errors.New("boom"))
		_, err := store.ConsumeApproval(ctx, tx, 5, "h", "cell_write", 9, pwsNow)
		require.ErrorContains(t, err, "consume pricing_write_approvals")
	})
	t.Run("scan fails", func(t *testing.T) {
		store, mock, tx := consume(t)
		mock.ExpectQuery(updateSQL).WillReturnRows(sqlmock.NewRows([]string{"touches_price", "price_delta", "previewed_by"}).AddRow("maybe", "up", int64(7)))
		_, err := store.ConsumeApproval(ctx, tx, 5, "h", "cell_write", 9, pwsNow)
		require.ErrorContains(t, err, "scan consumed approval")
	})
	t.Run("iterate fails", func(t *testing.T) {
		store, mock, tx := consume(t)
		mock.ExpectQuery(updateSQL).WillReturnRows(sqlmock.NewRows([]string{"touches_price", "price_delta", "previewed_by"}).
			AddRow(true, "up", int64(7)).RowError(0, errors.New("boom")))
		_, err := store.ConsumeApproval(ctx, tx, 5, "h", "cell_write", 9, pwsNow)
		require.ErrorContains(t, err, "consume pricing_write_approvals")
	})

	selectCols := []string{"status", "plan_hash", "kind", "expires_at"}
	rejected := []struct {
		name   string
		rows   *sqlmock.Rows
		reason string
	}{
		{"not found", sqlmock.NewRows(selectCols), service.ReasonApprovalNotFound},
		{"already consumed", sqlmock.NewRows(selectCols).AddRow("consumed", "h", "cell_write", pwsNow.Add(time.Hour)), service.ReasonApprovalConsumed},
		{"expired", sqlmock.NewRows(selectCols).AddRow("previewed", "h", "cell_write", pwsNow.Add(-time.Second)), service.ReasonApprovalExpired},
		{"plan differs", sqlmock.NewRows(selectCols).AddRow("previewed", "other", "cell_write", pwsNow.Add(time.Hour)), service.ReasonApprovalMismatch},
	}
	for _, tc := range rejected {
		t.Run("rejected: "+tc.name, func(t *testing.T) {
			store, mock, tx := consume(t)
			mock.ExpectQuery(updateSQL).WillReturnRows(sqlmock.NewRows([]string{"touches_price", "price_delta", "previewed_by"}))
			mock.ExpectQuery(selectSQL).WithArgs(int64(5)).WillReturnRows(tc.rows)
			_, err := store.ConsumeApproval(ctx, tx, 5, "h", "cell_write", 9, pwsNow)
			require.Equal(t, tc.reason, reasonOf(t, err))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
	t.Run("rejected: classification query fails", func(t *testing.T) {
		store, mock, tx := consume(t)
		mock.ExpectQuery(updateSQL).WillReturnRows(sqlmock.NewRows([]string{"touches_price", "price_delta", "previewed_by"}))
		mock.ExpectQuery(selectSQL).WillReturnError(errors.New("boom"))
		_, err := store.ConsumeApproval(ctx, tx, 5, "h", "cell_write", 9, pwsNow)
		require.ErrorContains(t, err, "query pricing_write_approvals")
	})
	t.Run("rejected: classification scan fails", func(t *testing.T) {
		store, mock, tx := consume(t)
		mock.ExpectQuery(updateSQL).WillReturnRows(sqlmock.NewRows([]string{"touches_price", "price_delta", "previewed_by"}))
		mock.ExpectQuery(selectSQL).WillReturnRows(sqlmock.NewRows(selectCols).AddRow("previewed", "h", "cell_write", "not-a-time"))
		_, err := store.ConsumeApproval(ctx, tx, 5, "h", "cell_write", 9, pwsNow)
		require.ErrorContains(t, err, "scan pricing_write_approvals")
	})
	t.Run("rejected: classification iterate fails", func(t *testing.T) {
		store, mock, tx := consume(t)
		mock.ExpectQuery(updateSQL).WillReturnRows(sqlmock.NewRows([]string{"touches_price", "price_delta", "previewed_by"}))
		mock.ExpectQuery(selectSQL).WillReturnRows(sqlmock.NewRows(selectCols).
			AddRow("previewed", "h", "cell_write", pwsNow).RowError(0, errors.New("boom")))
		_, err := store.ConsumeApproval(ctx, tx, 5, "h", "cell_write", 9, pwsNow)
		require.ErrorContains(t, err, "query pricing_write_approvals")
	})
}

func TestPricingWriteStore_PurgeStale(t *testing.T) {
	ctx := context.Background()
	purge := `DELETE FROM pricing_write_approvals WHERE status = 'previewed' AND created_at < \$1`

	db, mock := newSQLMock(t)
	store := NewPricingWriteStore(db)
	mock.ExpectExec(purge).WithArgs(pwsNow).WillReturnResult(sqlmock.NewResult(0, 3))
	n, err := store.PurgeStale(ctx, pwsNow)
	require.NoError(t, err)
	require.Equal(t, int64(3), n)

	mock.ExpectExec(purge).WillReturnError(errors.New("boom"))
	_, err = store.PurgeStale(ctx, pwsNow)
	require.ErrorContains(t, err, "purge pricing_write_approvals")

	mock.ExpectExec(purge).WillReturnResult(sqlmock.NewErrorResult(errors.New("no rows affected")))
	_, err = store.PurgeStale(ctx, pwsNow)
	require.ErrorContains(t, err, "rows affected")
	require.NoError(t, mock.ExpectationsWereMet())
}
