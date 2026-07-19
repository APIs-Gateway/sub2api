package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type accountIDsPayloadMatcher struct {
	want []int64
}

func (m accountIDsPayloadMatcher) Match(value driver.Value) bool {
	raw, ok := value.([]byte)
	if !ok {
		return false
	}
	var payload struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	return reflect.DeepEqual(m.want, payload.AccountIDs)
}

func TestAutoPauseExpiredAccountsEnqueuesAffectedAccounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM accounts`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)).AddRow(int64(29)))
	mock.ExpectExec(`(?s)UPDATE accounts.*id IN.*expires_at <= \$3`).
		WithArgs(int64(11), int64(29), now).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)")).
		WithArgs(service.SchedulerOutboxEventAccountBulkChanged, nil, nil, accountIDsPayloadMatcher{want: []int64{11, 29}}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	updated, err := repo.AutoPauseExpiredAccounts(context.Background(), now)

	require.NoError(t, err)
	require.EqualValues(t, 2, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutoPauseExpiredAccountsSkipsOutboxWithoutChanges(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM accounts`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	updated, err := repo.AutoPauseExpiredAccounts(context.Background(), now)

	require.NoError(t, err)
	require.Zero(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutoPauseExpiredAccountsSkipsOutboxWhenCandidateWasAlreadyPaused(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM accounts`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)))
	mock.ExpectExec(`(?s)UPDATE accounts.*id IN.*expires_at <= \$2`).
		WithArgs(int64(11), now).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	updated, err := repo.AutoPauseExpiredAccounts(context.Background(), now)

	require.NoError(t, err)
	require.Zero(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutoPauseExpiredAccountsReturnsQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	queryErr := errors.New("query failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM accounts`).
		WithArgs(now).
		WillReturnError(queryErr)
	mock.ExpectRollback()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	updated, err := repo.AutoPauseExpiredAccounts(context.Background(), now)

	require.ErrorIs(t, err, queryErr)
	require.Zero(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutoPauseExpiredAccountsReturnsScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM accounts`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("not-an-id"))
	mock.ExpectRollback()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	updated, err := repo.AutoPauseExpiredAccounts(context.Background(), now)

	require.Error(t, err)
	require.Zero(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutoPauseExpiredAccountsReturnsRowsError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	rowsErr := errors.New("rows failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM accounts`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)).RowError(0, rowsErr))
	mock.ExpectRollback()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	updated, err := repo.AutoPauseExpiredAccounts(context.Background(), now)

	require.ErrorIs(t, err, rowsErr)
	require.Zero(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutoPauseExpiredAccountsLogsOutboxErrorAndReturnsCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	outboxErr := errors.New("outbox failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM accounts`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)))
	mock.ExpectExec(`(?s)UPDATE accounts.*id IN.*expires_at <= \$2`).
		WithArgs(int64(11), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)")).
		WithArgs(service.SchedulerOutboxEventAccountBulkChanged, nil, nil, accountIDsPayloadMatcher{want: []int64{11}}).
		WillReturnError(outboxErr)
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	updated, err := repo.AutoPauseExpiredAccounts(context.Background(), now)

	require.NoError(t, err)
	require.EqualValues(t, 1, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}
