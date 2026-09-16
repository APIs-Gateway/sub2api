//go:build unit

package repository

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestUsageLogUpstreamModelMismatchMigration_SQLite(t *testing.T) {
	db, err := sql.Open("sqlite", "file:usage_log_upstream_model_mismatch_migration?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)

	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "190_usage_log_upstream_model_mismatch.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)

	for _, column := range []string{"upstream_model_mismatch", "upstream_response_model"} {
		var found string
		require.NoError(t, db.QueryRow("SELECT name FROM pragma_table_info('usage_logs') WHERE name = ?", column).Scan(&found))
		require.Equal(t, column, found)
	}

	_, err = db.Exec(`INSERT INTO usage_logs (id) VALUES (1)`)
	require.NoError(t, err)

	var mismatch bool
	var responseModel sql.NullString
	require.NoError(t, db.QueryRow(`SELECT upstream_model_mismatch, upstream_response_model FROM usage_logs WHERE id = 1`).Scan(&mismatch, &responseModel))
	require.False(t, mismatch, "upstream_model_mismatch should default to FALSE")
	require.False(t, responseModel.Valid, "upstream_response_model should default to NULL")
}
