package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationAppliesToDatabase_UserEmailDotStrippedIndexGate(t *testing.T) {
	// The PostgreSQL migration uses CREATE INDEX CONCURRENTLY and therefore must
	// never be offered to drivers whose SQL dialect does not support it. SQLite
	// receives its portable expression-index sibling through the normal suffix
	// selection below.
	require.True(t, migrationAppliesToDatabase(userEmailDotStrippedIndexMigration, migrationDatabasePostgres))
	require.False(t, migrationAppliesToDatabase(userEmailDotStrippedIndexMigration, migrationDatabaseMySQL))
	require.False(t, migrationAppliesToDatabase(userEmailDotStrippedIndexMigration, migrationDatabaseSQLite))

	const sqliteMigration = "191_add_users_email_dot_stripped_index_sqlite.sql"
	require.True(t, migrationAppliesToDatabase(sqliteMigration, migrationDatabaseSQLite))
	require.False(t, migrationAppliesToDatabase(sqliteMigration, migrationDatabasePostgres))
	require.False(t, migrationAppliesToDatabase(sqliteMigration, migrationDatabaseMySQL))
}
