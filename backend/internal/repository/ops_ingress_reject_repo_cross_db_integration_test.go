//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite"
)

func TestOpsIngressRejectRepositorySQLiteActualUpsertAndFilter(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))
	require.NoError(t, err)
	exerciseOpsIngressRejectRepositoryAcrossDialects(t, context.Background(), db, migrationDatabaseSQLite)
}

func TestOpsIngressRejectRepositoryPostgreSQLActualUpsertAndFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("ops_ingress_reject"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	exerciseOpsIngressRejectRepositoryAcrossDialects(t, ctx, db, migrationDatabasePostgres)
}

func TestOpsIngressRejectRepositoryMySQL57ActualUpsertAndFilter(t *testing.T) {
	if runtime.GOARCH != "amd64" && os.Getenv("OPS_INGRESS_REJECT_TEST_MYSQL57_EMULATED") != "1" {
		t.Skip("mysql:5.7.44 only publishes an amd64 image; run this test on native amd64 or set OPS_INGRESS_REJECT_TEST_MYSQL57_EMULATED=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := mysql.Run(ctx, "mysql:5.7.44",
		mysql.WithDatabase("ops_ingress_reject"),
		mysql.WithUsername("ops"),
		mysql.WithPassword("ops"),
		testcontainers.WithImagePlatform("linux/amd64"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })

	dsn, err := container.ConnectionString(ctx, "parseTime=true")
	require.NoError(t, err)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	exerciseOpsIngressRejectRepositoryAcrossDialects(t, ctx, db, migrationDatabaseMySQL)
}

func exerciseOpsIngressRejectRepositoryAcrossDialects(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dialect migrationDatabaseDialect,
) {
	t.Helper()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.PingContext(ctx))
	createOpsIngressRejectAggregateTable(t, ctx, db, dialect)

	repo := &opsRepository{db: db}
	bucket := time.Now().UTC().Truncate(time.Minute)
	userID := int64(101)
	first := &service.OpsIngressRejectAggregate{
		BucketStart: bucket, RejectReason: "invalid_api_key", RouteFamily: "responses", Protocol: "http",
		ClientIP: "203.0.113.0/24", UserID: &userID, RequestCount: 3,
		FirstSeen: bucket, LastSeen: bucket.Add(10 * time.Second),
	}
	second := &service.OpsIngressRejectAggregate{
		BucketStart: bucket, RejectReason: "invalid_api_key", RouteFamily: "responses", Protocol: "http",
		ClientIP: "203.0.113.0/24", UserID: &userID, RequestCount: 2,
		FirstSeen: bucket.Add(-5 * time.Second), LastSeen: bucket.Add(20 * time.Second),
	}
	other := &service.OpsIngressRejectAggregate{
		BucketStart: bucket, RejectReason: "ip_restricted", RouteFamily: "messages", Protocol: "http",
		ClientIP: "198.51.100.0/24", RequestCount: 1, FirstSeen: bucket, LastSeen: bucket,
	}

	require.NoError(t, repo.BatchUpsertIngressRejects(ctx, []*service.OpsIngressRejectAggregate{first}))
	require.NoError(t, repo.BatchUpsertIngressRejects(ctx, []*service.OpsIngressRejectAggregate{second, other}))

	filtered, err := repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{
		RejectReason: "invalid_api_key", UserID: &userID, PageSize: 50,
	})
	require.NoError(t, err)
	require.Equal(t, 1, filtered.Total)
	require.Len(t, filtered.Items, 1)
	require.Equal(t, int64(5), filtered.Items[0].RequestCount)
	require.WithinDuration(t, bucket.Add(-5*time.Second), filtered.Items[0].FirstSeen, time.Second)
	require.WithinDuration(t, bucket.Add(20*time.Second), filtered.Items[0].LastSeen, time.Second)
	require.Equal(t, userID, *filtered.Items[0].UserID)
	require.Nil(t, filtered.Items[0].APIKeyID)

	all, err := repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{PageSize: 50})
	require.NoError(t, err)
	require.Equal(t, 2, all.Total)
}

func createOpsIngressRejectAggregateTable(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dialect migrationDatabaseDialect,
) {
	t.Helper()
	statement := `CREATE TABLE ops_ingress_reject_aggregates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bucket_start TIMESTAMP NOT NULL,
		reject_reason VARCHAR(64) NOT NULL,
		route_family VARCHAR(64) NOT NULL,
		protocol VARCHAR(32) NOT NULL,
		client_ip VARCHAR(64) NOT NULL DEFAULT '',
		user_id BIGINT NOT NULL DEFAULT 0,
		api_key_id BIGINT NOT NULL DEFAULT 0,
		request_count BIGINT NOT NULL DEFAULT 0,
		first_seen TIMESTAMP NOT NULL,
		last_seen TIMESTAMP NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)
	)`
	switch dialect {
	case migrationDatabasePostgres:
		statement = `CREATE TABLE ops_ingress_reject_aggregates (
			id BIGSERIAL PRIMARY KEY,
			bucket_start TIMESTAMPTZ NOT NULL,
			reject_reason VARCHAR(64) NOT NULL,
			route_family VARCHAR(64) NOT NULL,
			protocol VARCHAR(32) NOT NULL,
			client_ip VARCHAR(64) NOT NULL DEFAULT '',
			user_id BIGINT NOT NULL DEFAULT 0,
			api_key_id BIGINT NOT NULL DEFAULT 0,
			request_count BIGINT NOT NULL DEFAULT 0,
			first_seen TIMESTAMPTZ NOT NULL,
			last_seen TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)
		)`
	case migrationDatabaseMySQL:
		statement = `CREATE TABLE ops_ingress_reject_aggregates (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			bucket_start TIMESTAMP NOT NULL,
			reject_reason VARCHAR(64) NOT NULL,
			route_family VARCHAR(64) NOT NULL,
			protocol VARCHAR(32) NOT NULL,
			client_ip VARCHAR(64) NOT NULL DEFAULT '',
			user_id BIGINT NOT NULL DEFAULT 0,
			api_key_id BIGINT NOT NULL DEFAULT 0,
			request_count BIGINT NOT NULL DEFAULT 0,
			first_seen TIMESTAMP NOT NULL,
			last_seen TIMESTAMP NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY ops_ingress_reject_dimensions (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)
		)`
	}
	_, err := db.ExecContext(ctx, statement)
	require.NoError(t, err)
}
