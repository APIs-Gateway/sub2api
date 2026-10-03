//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	_ "modernc.org/sqlite"
)

func TestBillingInflightPostgres_MigrationRepeatAndNonPostgresSkip(t *testing.T) {
	ctx := context.Background()
	const name = "198_billing_inflight_leases.sql"
	data, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	source := fstest.MapFS{name: &fstest.MapFile{Data: data}}
	t.Run("postgres_existing_schema_repeat", func(t *testing.T) {
		require.NoError(t, applyMigrationsFS(ctx, integrationDB, source))
		require.NoError(t, applyMigrationsFS(ctx, integrationDB, source))
		var n int
		require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM schema_migrations WHERE filename=$1`, name).Scan(&n))
		require.Equal(t, 1, n)
		require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='billing_inflight_leases'`).Scan(&n))
		require.Equal(t, 1, n)
	})
	t.Run("sqlite_only_new_migration", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(1)
		require.NoError(t, applyMigrationsFS(ctx, db, source))
		require.NoError(t, applyMigrationsFS(ctx, db, source))
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='billing_inflight_leases'`).Scan(&n))
		require.Zero(t, n)
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE filename=?`, name).Scan(&n))
		require.Zero(t, n)
		require.False(t, (&usageBillingRepository{db: db}).BillingInflightAvailable())
	})
	t.Run("mysql84_only_new_migration", func(t *testing.T) {
		container, err := tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithDatabase("inflight_gate"), tcmysql.WithUsername("root"), tcmysql.WithPassword("inflight"))
		require.NoError(t, err)
		defer func() { _ = container.Terminate(ctx) }()
		dsn, err := container.ConnectionString(ctx)
		require.NoError(t, err)
		db, err := sql.Open("mysql", dsn)
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		require.NoError(t, applyMigrationsFS(ctx, db, source))
		require.NoError(t, applyMigrationsFS(ctx, db, source))
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='billing_inflight_leases'`).Scan(&n))
		require.Zero(t, n)
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE filename=?`, name).Scan(&n))
		require.Zero(t, n)
		require.False(t, (&usageBillingRepository{db: db}).BillingInflightAvailable())
	})
}
