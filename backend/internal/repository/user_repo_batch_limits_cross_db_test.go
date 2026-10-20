//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestUserRepositoryBatchUpdateLimitsSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", "file:user_batch_limits_cross_db?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(context.Background(), `
CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    concurrency INTEGER NOT NULL,
    rpm_limit INTEGER NOT NULL,
    updated_at TIMESTAMP,
    deleted_at TIMESTAMP NULL
);
INSERT INTO users (id, concurrency, rpm_limit) VALUES (1, 1, 10), (2, 2, 20), (3, 3, 30);
`)
	require.NoError(t, err)

	repo := newUserRepositoryWithSQL(nil, db)
	concurrency := 8
	affected, err := repo.BatchUpdateLimits(context.Background(), []int64{1, 2, 3}, &concurrency, nil)
	require.NoError(t, err)
	require.Equal(t, 3, affected)

	rows, err := db.QueryContext(context.Background(), "SELECT id, concurrency, rpm_limit FROM users ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	for id := int64(1); id <= 3; id++ {
		var rowID int64
		var gotConcurrency, gotRPMLimit int
		require.True(t, rows.Next())
		require.NoError(t, rows.Scan(&rowID, &gotConcurrency, &gotRPMLimit))
		require.Equal(t, id, rowID)
		require.Equal(t, concurrency, gotConcurrency)
		require.Equal(t, int(id*10), gotRPMLimit)
	}
	require.NoError(t, rows.Err())
}

func TestBuildBatchUpdateLimitsQuestionDialectUsesPortablePlaceholders(t *testing.T) {
	concurrency := 5
	query, args := buildBatchUpdateLimitsQuery([]int64{11, 12}, &concurrency, nil, userRepositoryQuestionDialect)
	require.Equal(t, "UPDATE users SET concurrency = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (?, ?) AND deleted_at IS NULL", query)
	require.Equal(t, []any{5, int64(11), int64(12)}, args)
}
