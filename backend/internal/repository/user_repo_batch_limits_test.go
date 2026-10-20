//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestUserRepositoryBatchUpdateLimitsBuildsPartialUpdate(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	repo := newUserRepositoryWithSQL(nil, db)
	userIDs := []int64{11, 12}
	concurrency := 8
	rpmLimit := 0
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users SET concurrency = $1, rpm_limit = $2, updated_at = CURRENT_TIMESTAMP WHERE id IN ($3, $4) AND deleted_at IS NULL")).
		WithArgs(concurrency, rpmLimit, userIDs[0], userIDs[1]).
		WillReturnResult(sqlmock.NewResult(0, 2))

	affected, err := repo.BatchUpdateLimits(context.Background(), userIDs, &concurrency, &rpmLimit)

	require.NoError(t, err)
	require.Equal(t, 2, affected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepositoryBatchUpdateLimitsLeavesUnselectedFieldUntouched(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	repo := newUserRepositoryWithSQL(nil, db)
	userIDs := []int64{13}
	rpmLimit := 60
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users SET rpm_limit = $1, updated_at = CURRENT_TIMESTAMP WHERE id IN ($2) AND deleted_at IS NULL")).
		WithArgs(rpmLimit, userIDs[0]).
		WillReturnResult(sqlmock.NewResult(0, 1))

	affected, err := repo.BatchUpdateLimits(context.Background(), userIDs, nil, &rpmLimit)

	require.NoError(t, err)
	require.Equal(t, 1, affected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepositoryBatchUpdateLimitsSkipsEmptyRequest(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	repo := newUserRepositoryWithSQL(nil, db)
	affected, err := repo.BatchUpdateLimits(context.Background(), nil, nil, nil)

	require.NoError(t, err)
	require.Zero(t, affected)
	require.NoError(t, mock.ExpectationsWereMet())
}
