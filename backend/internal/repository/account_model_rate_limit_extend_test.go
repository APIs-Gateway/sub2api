//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestModelRateLimitResetFromExtra(t *testing.T) {
	const scope = "openai:image_generation"
	for _, tc := range []struct {
		name  string
		extra string
		want  string
	}{
		{name: "missing", extra: `{}`},
		{name: "malformed extra", extra: `{`},
		{name: "invalid", extra: `{"model_rate_limits":{"openai:image_generation":{"rate_limit_reset_at":"tomorrow"}}}`},
		{name: "malformed limit", extra: `{"model_rate_limits":{"openai:image_generation":42}}`},
		{name: "missing reset", extra: `{"model_rate_limits":{"openai:image_generation":{"reason":"old"}}}`},
		{name: "empty reset", extra: `{"model_rate_limits":{"openai:image_generation":{"rate_limit_reset_at":""}}}`},
		{name: "bad sibling ignored", extra: `{"model_rate_limits":{"other":42,"openai:image_generation":{"rate_limit_reset_at":"2026-09-29T01:02:03+08:00"}}}`, want: "2026-09-28T17:02:03Z"},
		{name: "offset", extra: `{"model_rate_limits":{"openai:image_generation":{"rate_limit_reset_at":"2026-09-29T01:02:03+08:00"}}}`, want: "2026-09-28T17:02:03Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := modelRateLimitResetFromExtra([]byte(tc.extra), scope)
			if tc.want == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tc.want, got.UTC().Format(time.RFC3339))
		})
	}
}

func TestExtendModelRateLimitRejectsUnsafeExecution(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(nil, nil, nil)
	require.NoError(t, repo.ExtendModelRateLimit(ctx, 1, "", time.Now()))
	require.ErrorContains(t, repo.ExtendModelRateLimit(ctx, 1, "openai:image_generation", time.Now()), "requires a SQL database")
	require.ErrorContains(t, repo.ExtendModelRateLimit(dbent.NewTxContext(ctx, &dbent.Tx{}), 1, "openai:image_generation", time.Now()), "inside an Ent transaction")
}

func newExtendModelRateLimitMock(t *testing.T) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return newAccountRepositoryWithSQL(nil, db, nil), mock
}

func TestExtendModelRateLimitMissingAccountDoesNotWrite(t *testing.T) {
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(71)).WillReturnRows(sqlmock.NewRows([]string{"extra"}))
	mock.ExpectRollback()

	err := repo.ExtendModelRateLimit(context.Background(), 71, "openai:image_generation", time.Now().Add(5*time.Minute))

	require.ErrorIs(t, err, service.ErrAccountNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtendModelRateLimitDatabaseFailureRollsBack(t *testing.T) {
	writeErr := errors.New("database write failed")
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(72)).WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
	mock.ExpectExec(`UPDATE accounts SET`).WithArgs("openai:image_generation", sqlmock.AnyArg(), int64(72)).WillReturnError(writeErr)
	mock.ExpectRollback()

	err := repo.ExtendModelRateLimit(context.Background(), 72, "openai:image_generation", time.Now().Add(5*time.Minute), "balance")

	require.ErrorIs(t, err, writeErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtendModelRateLimitBeginOrReadFailureDoesNotWrite(t *testing.T) {
	beginErr := errors.New("database unavailable")
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin().WillReturnError(beginErr)
	require.ErrorIs(t, repo.ExtendModelRateLimit(context.Background(), 73, "openai:image_generation", time.Now()), beginErr)
	require.NoError(t, mock.ExpectationsWereMet())

	readErr := errors.New("cannot read account")
	repo, mock = newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(74)).WillReturnError(readErr)
	mock.ExpectRollback()
	require.ErrorIs(t, repo.ExtendModelRateLimit(context.Background(), 74, "openai:image_generation", time.Now()), readErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtendModelRateLimitDeletedDuringWriteDoesNotPublish(t *testing.T) {
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(75)).WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
	mock.ExpectExec(`UPDATE accounts SET`).WithArgs("openai:image_generation", sqlmock.AnyArg(), int64(75)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := repo.ExtendModelRateLimit(context.Background(), 75, "openai:image_generation", time.Now())

	require.ErrorIs(t, err, service.ErrAccountNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtendModelRateLimitAffectedRowsFailureDoesNotPublish(t *testing.T) {
	affectedErr := errors.New("cannot confirm updated row")
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(78)).WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
	mock.ExpectExec(`UPDATE accounts SET`).WithArgs("openai:image_generation", sqlmock.AnyArg(), int64(78)).WillReturnResult(sqlmock.NewErrorResult(affectedErr))
	mock.ExpectRollback()

	err := repo.ExtendModelRateLimit(context.Background(), 78, "openai:image_generation", time.Now())

	require.ErrorIs(t, err, affectedErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtendModelRateLimitOutboxFailureKeepsCommittedCooldown(t *testing.T) {
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(79)).WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
	mock.ExpectExec(`UPDATE accounts SET`).WithArgs("openai:image_generation", sqlmock.AnyArg(), int64(79)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnError(errors.New("outbox unavailable"))

	err := repo.ExtendModelRateLimit(context.Background(), 79, "openai:image_generation", time.Now())

	require.NoError(t, err, "the account cooldown committed before a best-effort outbox publish failure")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtendModelRateLimitCommitFailureDoesNotReportSuccess(t *testing.T) {
	commitErr := errors.New("commit failed")
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(76)).WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{}`)))
	mock.ExpectExec(`UPDATE accounts SET`).WithArgs("openai:image_generation", sqlmock.AnyArg(), int64(76)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(commitErr)

	err := repo.ExtendModelRateLimit(context.Background(), 76, "openai:image_generation", time.Now())

	require.ErrorIs(t, err, commitErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtendModelRateLimitNoOpCommitFailureIsReported(t *testing.T) {
	commitErr := errors.New("commit failed")
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(77)).WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{"model_rate_limits":{"openai:image_generation":{"rate_limit_reset_at":"2099-01-01T00:00:00Z"}}}`)))
	mock.ExpectCommit().WillReturnError(commitErr)

	err := repo.ExtendModelRateLimit(context.Background(), 77, "openai:image_generation", time.Now())

	require.ErrorIs(t, err, commitErr)
	require.NoError(t, mock.ExpectationsWereMet())
}
