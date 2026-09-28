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
		name string
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
		{name: "ratio query unavailable", setup: func(mock sqlmock.Sqlmock) {
			modelDowngradeExpectGlobalLock(mock)
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
			mock.ExpectQuery("SELECT extra FROM accounts").WillReturnError(failure)
			mock.ExpectRollback()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			tc.setup(mock)
			repo := newAccountRepositoryWithSQL(nil, db, nil)
			applied, err := repo.TryBlockDowngradedModel(context.Background(), 17, "gpt-6-astra", "gpt-6-astra", time.Now().Add(time.Hour), 0.3, false,
				func(context.Context, *service.Account, *int64) bool { return true })
			require.False(t, applied)
			if tc.name == "target disappeared" {
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

func TestModelDowngradeGuardPoolLockErrors(t *testing.T) {
	ctx := context.Background()
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
