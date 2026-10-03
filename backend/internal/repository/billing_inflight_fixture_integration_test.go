//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	redisclient "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// These fixtures need committed rows and independent transactions to exercise
// real HTTP/WS billing and user/card lock ordering. Give each testing.T its own
// database instead of leaking committed rows into the existing shared suites.
// The template contains only migrations, never a snapshot of shared test data.
var inflightDatabases = struct {
	sync.Mutex
	template string
	fixtures map[*testing.T]*inflightDatabaseFixture
}{fixtures: make(map[*testing.T]*inflightDatabaseFixture)}

type inflightDatabaseFixture struct {
	db     *sql.DB
	client *dbent.Client
}

func inflightDatabaseDSN(name string) (string, error) {
	u, err := url.Parse(integrationPostgresDSN)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}

func inflightTestDatabase(t *testing.T) *inflightDatabaseFixture {
	t.Helper()
	inflightDatabases.Lock()
	defer inflightDatabases.Unlock()
	if fixture := inflightDatabases.fixtures[t]; fixture != nil {
		return fixture
	}
	ctx := context.Background()
	if inflightDatabases.template == "" {
		name := "inflight_template_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		_, err := integrationDB.ExecContext(ctx, `CREATE DATABASE "`+name+`"`)
		require.NoError(t, err)
		inflightDatabases.template = name
		dsn, err := inflightDatabaseDSN(name)
		require.NoError(t, err)
		db, err := sql.Open("postgres", dsn)
		require.NoError(t, err)
		err = ApplyMigrations(ctx, db)
		closeErr := db.Close()
		require.NoError(t, err, "migrate empty isolated fixture template")
		require.NoError(t, closeErr)
	}
	name := "inflight_fixture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := integrationDB.ExecContext(ctx, `CREATE DATABASE "`+name+`" TEMPLATE "`+inflightDatabases.template+`"`)
	require.NoError(t, err)
	fixture := &inflightDatabaseFixture{}
	inflightDatabases.fixtures[t] = fixture
	// Register before any fixture services, handlers, connections, or worker
	// pools. Their LIFO cleanup must stop asynchronous work before this runs.
	t.Cleanup(func() {
		if fixture.client != nil {
			if err := fixture.client.Close(); err != nil {
				t.Errorf("close isolated fixture client: %v", err)
			}
		} else if fixture.db != nil {
			if err := fixture.db.Close(); err != nil {
				t.Errorf("close isolated fixture database: %v", err)
			}
		}
		_, err := integrationDB.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`)
		require.NoError(t, err, "drop only this fixture's isolated database")
		var remaining int
		require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM pg_database WHERE datname=$1`, name).Scan(&remaining))
		require.Zero(t, remaining, "isolated fixture database must not remain after cleanup")
		inflightDatabases.Lock()
		delete(inflightDatabases.fixtures, t)
		inflightDatabases.Unlock()
	})
	dsn, err := inflightDatabaseDSN(name)
	require.NoError(t, err)
	fixture.db, err = sql.Open("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, fixture.db.PingContext(ctx))
	fixture.client = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, fixture.db)))
	return fixture
}

func inflightTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return inflightTestDatabase(t).db
}

func inflightTestEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	return inflightTestDatabase(t).client
}

func cleanupInflightDatabaseTemplate() {
	if inflightDatabases.template == "" {
		return
	}
	if len(inflightDatabases.fixtures) != 0 {
		panic("isolated inflight fixture database cleanup did not complete")
	}
	if _, err := integrationDB.Exec(`DROP DATABASE "` + inflightDatabases.template + `" WITH (FORCE)`); err != nil {
		panic(fmt.Sprintf("drop isolated inflight template: %v", err))
	}
}

func TestBillingInflightPostgres_FixtureDatabasesAreIsolated(t *testing.T) {
	_, user, _ := inflightFixture(t, 1)
	var parentName, childName string
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT current_database()`).Scan(&parentName))
	t.Run("independent_committed_rows", func(t *testing.T) {
		db := inflightTestDB(t)
		require.NoError(t, db.QueryRow(`SELECT current_database()`).Scan(&childName))
		require.NotEqual(t, parentName, childName)
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM users`).Scan(&n))
		require.Zero(t, n, "clone contains migrations, not another fixture's committed users")
		require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM users WHERE email=$1`, user.Email).Scan(&n))
		require.Zero(t, n, "real billing fixtures must not write users into shared suites")
		inflightFixture(t, 2)
	})
	var n int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM pg_database WHERE datname=$1`, childName).Scan(&n))
	require.Zero(t, n, "child fixture cleanup drops its database before the parent continues")
}

// FlushDB cannot respect the usual Redis key-prefix hook. This one loss test
// therefore owns an entire Redis container and never flushes shared Redis.
func inflightIsolatedRedis(t *testing.T) *redisclient.Client {
	t.Helper()
	ctx := context.Background()
	container, err := tcredis.Run(ctx, redisImageTag)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	rdb := redisclient.NewClient(&redisclient.Options{Addr: fmt.Sprintf("%s:%d", host, port.Int())})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, rdb.Ping(ctx).Err())
	return rdb
}

func makeInflightPaymentService(t *testing.T) *service.PaymentService {
	t.Helper()
	client := inflightTestEntClient(t)
	groups := NewGroupRepository(client, inflightTestDB(t))
	subs := service.NewSubscriptionService(groups, NewUserSubscriptionRepository(client), nil, nil, nil, client, nil, nil)
	return service.NewPaymentService(client, nil, nil, nil, subs, service.NewPaymentConfigService(client, nil, nil), nil, groups, nil)
}
