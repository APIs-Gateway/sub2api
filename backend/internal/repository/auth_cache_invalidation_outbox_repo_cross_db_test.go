//go:build unit

package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestAuthCacheInvalidationOutboxDialectUsesPortablePlaceholders(t *testing.T) {
	require.Equal(t, "$1", authCacheInvalidationOutboxPlaceholder(authCacheInvalidationOutboxPostgres, 1))
	require.Equal(t, "?", authCacheInvalidationOutboxPlaceholder(authCacheInvalidationOutboxSQLite, 1))

	db, err := sql.Open("sqlite", fmt.Sprintf("file:auth_cache_outbox_dialect_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.Equal(t, authCacheInvalidationOutboxSQLite, authCacheInvalidationOutboxDialectForDB(db))

	mysqlDB, err := sql.Open("mysql", "user:pass@tcp(127.0.0.1:1)/db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mysqlDB.Close() })
	require.Equal(t, authCacheInvalidationOutboxMySQL, authCacheInvalidationOutboxDialectForDB(mysqlDB))
}

func TestAuthCacheInvalidationOutboxRepositoryMySQLClaimUsesPortableLocking(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := &authCacheInvalidationOutboxRepository{db: db, dialect: authCacheInvalidationOutboxMySQL}
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	mock.ExpectBegin()
	mock.ExpectQuery("(?s)LIMIT \\? FOR UPDATE").
		WithArgs(sqlmock.AnyArg(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "cache_key", "attempts", "delivery_stage", "created_at"}).
			AddRow(int64(9), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 0, 0, createdAt))
	mock.ExpectExec("(?s)UPDATE auth_cache_invalidation_outbox.*claimed_at = CURRENT_TIMESTAMP").
		WithArgs("mysql-worker", int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	events, err := repo.Claim(context.Background(), "mysql-worker", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, int64(9), events[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

type recordingMigrationExecutor struct {
	queries []string
	errAt   int
}

func (e *recordingMigrationExecutor) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	e.queries = append(e.queries, query)
	if e.errAt > 0 && len(e.queries) == e.errAt {
		return nil, fmt.Errorf("migration executor failure")
	}
	return sqlmock.NewResult(0, 0), nil
}

func TestAuthCacheInvalidationMigrationDialectHelpers(t *testing.T) {
	require.True(t, migrationAppliesToDatabase("184_auth_cache_invalidation_outbox.sql", migrationDatabasePostgres))
	require.False(t, migrationAppliesToDatabase("184_auth_cache_invalidation_outbox.sql", migrationDatabaseMySQL))
	require.True(t, migrationAppliesToDatabase("184_auth_cache_invalidation_outbox_mysql.sql", migrationDatabaseMySQL))
	require.False(t, migrationAppliesToDatabase("184_auth_cache_invalidation_outbox_mysql.sql", migrationDatabaseSQLite))
	require.True(t, migrationAppliesToDatabase("184_auth_cache_invalidation_outbox_sqlite.sql", migrationDatabaseSQLite))
	require.Contains(t, schemaMigrationsTableDDLForDialect(migrationDatabaseMySQL), "VARCHAR(255)")
	require.Contains(t, schemaMigrationsTableDDLForDialect(migrationDatabaseSQLite), "CURRENT_TIMESTAMP")
	require.Equal(t, "INSERT INTO schema_migrations (filename, checksum) VALUES (?, ?)", recordMigrationQuery(migrationDatabaseMySQL))

	executor := &recordingMigrationExecutor{}
	require.NoError(t, execMigrationContent(context.Background(), executor, "-- comment\nCREATE TABLE t (id INT); DROP TABLE t;", migrationDatabaseMySQL))
	require.Len(t, executor.queries, 2)

	executor = &recordingMigrationExecutor{errAt: 2}
	require.Error(t, execMigrationContent(context.Background(), executor, "CREATE TABLE t (id INT); DROP TABLE t;", migrationDatabaseMySQL))
}

func TestAuthCacheInvalidationOutboxRepositorySQLiteClaimAndLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:auth_cache_outbox_lifecycle_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(context.Background(), `
CREATE TABLE auth_cache_invalidation_outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    cache_key TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivery_stage INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    claimed_at TIMESTAMP NULL,
    claimed_by TEXT
);
INSERT INTO auth_cache_invalidation_outbox (cache_key) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
`)
	require.NoError(t, err)

	repo := NewAuthCacheInvalidationOutboxRepository(db).(*authCacheInvalidationOutboxRepository)
	events, err := repo.Claim(context.Background(), "sqlite-worker-1", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "sqlite-worker-1", readClaimedBy(t, db, events[0].ID))

	require.NoError(t, repo.ScheduleSecondPass(context.Background(), events[0].ID, "sqlite-worker-1", time.Now().UTC().Add(-time.Second)))
	secondPass, err := repo.Claim(context.Background(), "sqlite-worker-2", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, secondPass, 1)
	require.Equal(t, 1, secondPass[0].Stage)
	require.NoError(t, repo.DeleteClaimed(context.Background(), secondPass[0].ID, "sqlite-worker-2"))

	stats, err := repo.Stats(context.Background())
	require.NoError(t, err)
	require.Zero(t, stats.Pending)
}

func readClaimedBy(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var claimedBy string
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT claimed_by FROM auth_cache_invalidation_outbox WHERE id = ?", id).Scan(&claimedBy))
	return claimedBy
}

func TestAuthCacheInvalidationSQLiteMigrationUsesDurableHashedTriggers(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:auth_cache_outbox_migration_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(context.Background(), `
CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    status TEXT,
    role TEXT,
    deleted_at TIMESTAMP
);
CREATE TABLE groups (
    id INTEGER PRIMARY KEY,
    status TEXT,
    is_exclusive INTEGER NOT NULL DEFAULT 0,
    deleted_at TIMESTAMP
);
CREATE TABLE api_keys (
    id INTEGER PRIMARY KEY,
    user_id INTEGER,
    key TEXT,
    group_id INTEGER,
    status TEXT,
    deleted_at TIMESTAMP,
    ip_whitelist TEXT,
    ip_blacklist TEXT,
    expires_at TIMESTAMP
);
CREATE TABLE user_allowed_groups (user_id INTEGER, group_id INTEGER);
`)
	require.NoError(t, err)

	migration, err := migrations.FS.ReadFile("184_auth_cache_invalidation_outbox_sqlite.sql")
	require.NoError(t, err)
	fsys := fstest.MapFS{
		"184_auth_cache_invalidation_outbox_sqlite.sql": &fstest.MapFile{Data: migration},
	}
	require.NoError(t, applyMigrationsFS(context.Background(), db, fsys))
	require.NoError(t, applyMigrationsFS(context.Background(), db, fsys))

	const rawKey = "sk-sqlite-migration"
	_, err = db.ExecContext(context.Background(), `
INSERT INTO users (id, status, role) VALUES (1, 'active', 'user');
INSERT INTO groups (id, status, is_exclusive) VALUES (1, 'active', 1);
INSERT INTO api_keys (id, user_id, key, group_id, status) VALUES (1, 1, ?, 1, 'active');
`, rawKey)
	require.NoError(t, err)

	_, err = db.ExecContext(context.Background(), "UPDATE api_keys SET status = 'disabled' WHERE id = 1")
	require.NoError(t, err)
	wantDigest := sha256.Sum256([]byte(rawKey))
	wantCacheKey := hex.EncodeToString(wantDigest[:])
	var gotCacheKey string
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT cache_key FROM auth_cache_invalidation_outbox ORDER BY id LIMIT 1").Scan(&gotCacheKey))
	require.Equal(t, wantCacheKey, gotCacheKey)

	_, err = db.ExecContext(context.Background(), "DELETE FROM auth_cache_invalidation_outbox")
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), "UPDATE users SET status = 'disabled' WHERE id = 1")
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM auth_cache_invalidation_outbox").Scan(&count))
	require.Equal(t, 1, count)
}
