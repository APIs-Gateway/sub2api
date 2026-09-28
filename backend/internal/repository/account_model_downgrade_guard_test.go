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

// These failures must leave the account untouched; mismatch failover is
// independent from this optional cross-request quarantine.
func TestModelDowngradeGuardTransactionFailsClosed(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name  string
		ratio float64
		setup func(sqlmock.Sqlmock)
	}{
		{name: "begin", setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectBegin().WillReturnError(failure)
		}},
		{name: "advisory lock", setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectBegin()
			mock.ExpectExec("pg_advisory_xact_lock").WillReturnError(failure)
			mock.ExpectRollback()
		}},
		{name: "global pool lock", setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectBegin()
			mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT a.id FROM accounts a").WillReturnError(failure)
			mock.ExpectRollback()
		}},
		{name: "target disappeared", setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectGlobalLock(mock)
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}))
			mock.ExpectRollback()
		}},
		{name: "target state malformed", setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectGlobalLock(mock)
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`[]`)))
			mock.ExpectRollback()
		}},
		{name: "target query unavailable", setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectGlobalLock(mock)
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnError(failure)
			mock.ExpectRollback()
		}},
		{name: "ratio query unavailable", setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectGlobalLock(mock)
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnError(failure)
			mock.ExpectRollback()
		}},
		{name: "ratio payload malformed", setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectGlobalLock(mock)
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow("not-json-bytes"))
			mock.ExpectRollback()
		}},
		{name: "ratio row iteration failed", setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectGlobalLock(mock)
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)).RowError(0, failure))
			mock.ExpectRollback()
		}},
		{name: "group membership query unavailable", ratio: 1, setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectRatioPass(mock)
			mock.ExpectQuery("SELECT group_id FROM account_groups").WillReturnError(failure)
			mock.ExpectRollback()
		}},
		{name: "group membership malformed", ratio: 1, setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectRatioPass(mock)
			mock.ExpectQuery("SELECT group_id FROM account_groups").WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow("not-a-group-id"))
			mock.ExpectRollback()
		}},
		{name: "group membership iteration failed", ratio: 1, setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectRatioPass(mock)
			mock.ExpectQuery("SELECT group_id FROM account_groups").WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(10)).RowError(0, failure))
			mock.ExpectRollback()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			tc.setup(mock)
			repo := newAccountRepositoryWithSQL(nil, db, nil)
			ratio := tc.ratio
			if ratio == 0 {
				ratio = 0.3
			}
			applied, err := repo.TryBlockDowngradedModel(context.Background(), 17, "gpt-6-astra", "gpt-6-astra", time.Now().Add(time.Hour), ratio, false,
				func(context.Context, *service.Account, *int64) bool { return true })
			require.False(t, applied)
			if tc.name == "target disappeared" || tc.name == "ratio payload malformed" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func modelDowngradeExpectGlobalLock(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT a.id FROM accounts a").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
}

func modelDowngradeExpectRatioPass(mock sqlmock.Sqlmock) {
	modelDowngradeExpectGlobalLock(mock)
	mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
	mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
}

func TestModelDowngradeGuardPoolLockErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("query", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT id").WillReturnError(errors.New("pool query failed"))
		mock.ExpectRollback()
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		require.Error(t, lockModelDowngradePool(ctx, tx, "SELECT id"))
		require.NoError(t, tx.Rollback())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
	}{
		{name: "scan", rows: sqlmock.NewRows([]string{"id"}).AddRow("not-an-id")},
		{name: "row iteration", rows: sqlmock.NewRows([]string{"id"}).AddRow(int64(17)).RowError(0, errors.New("row failed"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT id").WillReturnRows(tc.rows)
			mock.ExpectRollback()
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			require.Error(t, lockModelDowngradePool(ctx, tx, "SELECT id"))
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestModelDowngradeSelectiveClearDatabaseFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		setup func(sqlmock.Sqlmock)
	}{
		{name: "write failed", setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectExec("UPDATE accounts SET extra").WillReturnError(errors.New("database unavailable"))
		}},
		{name: "account missing", setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectExec("UPDATE accounts SET extra").WillReturnResult(sqlmock.NewResult(0, 0))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			tc.setup(mock)
			repo := newAccountRepositoryWithSQL(nil, db, nil)
			require.Error(t, repo.ClearModelRateLimitsExceptDowngrade(context.Background(), 17))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
