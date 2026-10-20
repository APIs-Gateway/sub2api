package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationAppliesToDatabase_OpsIngressRejectAggregatesGate(t *testing.T) {
	// The default (Postgres) migration file must apply only on Postgres; the
	// dialect-specific siblings must apply only on their respective drivers.
	require.True(t, migrationAppliesToDatabase(opsIngressRejectAggregatesMigration, migrationDatabasePostgres))
	require.False(t, migrationAppliesToDatabase(opsIngressRejectAggregatesMigration, migrationDatabaseMySQL))
	require.False(t, migrationAppliesToDatabase(opsIngressRejectAggregatesMigration, migrationDatabaseSQLite))

	require.True(t, migrationAppliesToDatabase("183_ops_ingress_reject_aggregates_mysql.sql", migrationDatabaseMySQL))
	require.False(t, migrationAppliesToDatabase("183_ops_ingress_reject_aggregates_mysql.sql", migrationDatabasePostgres))
	require.False(t, migrationAppliesToDatabase("183_ops_ingress_reject_aggregates_mysql.sql", migrationDatabaseSQLite))

	require.True(t, migrationAppliesToDatabase("183_ops_ingress_reject_aggregates_sqlite.sql", migrationDatabaseSQLite))
	require.False(t, migrationAppliesToDatabase("183_ops_ingress_reject_aggregates_sqlite.sql", migrationDatabasePostgres))
	require.False(t, migrationAppliesToDatabase("183_ops_ingress_reject_aggregates_sqlite.sql", migrationDatabaseMySQL))
}
