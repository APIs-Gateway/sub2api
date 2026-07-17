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

func TestUsageLogImageInputTokensMigration_SQLite(t *testing.T) {
	db, err := sql.Open("sqlite", "file:usage_log_image_input_tokens_migration?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)

	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "178_usage_log_image_input_tokens.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)

	for _, column := range []string{"image_input_tokens", "image_input_cost"} {
		var found string
		require.NoError(t, db.QueryRow("SELECT name FROM pragma_table_info('usage_logs') WHERE name = ?", column).Scan(&found))
		require.Equal(t, column, found)
	}
}
