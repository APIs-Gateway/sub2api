//go:build unit

package repository

import (
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
	"testing"
)

func TestBillingInflightMigrationAndCapabilityDialectGate(t *testing.T) {
	const migration = "198_billing_inflight_leases.sql"
	require.True(t, migrationAppliesToDatabase(migration, migrationDatabasePostgres))
	for _, tc := range []struct {
		driver, dsn string
		dialect     migrationDatabaseDialect
	}{
		{"mysql", "user:pass@tcp(127.0.0.1:1)/db", migrationDatabaseMySQL},
		{"sqlite", "file:inflight_gate?mode=memory&cache=shared", migrationDatabaseSQLite},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			db, err := sql.Open(tc.driver, tc.dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			require.False(t, migrationAppliesToDatabase(migration, tc.dialect))
			require.False(t, (&usageBillingRepository{db: db}).BillingInflightAvailable(), "non-PG does not attempt unsupported reservation SQL")
		})
	}
}
