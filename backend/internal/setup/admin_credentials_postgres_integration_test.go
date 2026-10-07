//go:build integration

package setup

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/crypto/bcrypt"
)

// The existing creation API opens its own PostgreSQL connection. No mock or
// new private helper can substitute for its persisted credential contract.
func TestSetupAdminBootstrapPostgres_CredentialsAndExistingUsers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("setup_admin"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	portNumber, err := strconv.Atoi(port.Port())
	require.NoError(t, err)
	database := DatabaseConfig{Host: host, Port: portNumber, User: "postgres", Password: "postgres", DBName: "setup_admin", SSLMode: "disable"}
	db, err := sql.Open("postgres", buildPostgresDSN(&database, database.DBName))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.PingContext(ctx))
	_, err = db.ExecContext(ctx, `CREATE TABLE users (
		id BIGSERIAL PRIMARY KEY, email TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
		role TEXT NOT NULL, balance DOUBLE PRECISION NOT NULL, concurrency INTEGER NOT NULL,
		status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
	)`)
	require.NoError(t, err)
	reset := func(t *testing.T) {
		t.Helper()
		_, err := db.ExecContext(ctx, `TRUNCATE users RESTART IDENTITY`)
		require.NoError(t, err)
	}
	for _, tc := range []struct {
		name     string
		email    string
		password string
	}{
		{"short_password", "owner@example.com", "1234567"},
		{"oversize_ascii", "owner@example.com", strings.Repeat("a", 73)},
		{"oversize_utf8", "owner@example.com", strings.Repeat("界", 25)},
		{"invalid_email", "not-email", "valid-password"},
		{"unloginable_short_domain", "a@b", "valid-password"},
		{"display_name_email", "Name <a@b.com>", "valid-password"},
	} {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			reset(t)
			cfg := &SetupConfig{Database: database, Admin: AdminConfig{Email: tc.email, Password: tc.password}}
			created, _, err := createAdminUser(cfg)
			require.Error(t, err)
			require.False(t, created)
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count))
			require.Zero(t, count, "rejected credentials cannot leave a persisted account")
		})
	}
	for _, tc := range []struct {
		name     string
		mode     string
		email    string
		password string
	}{
		{"generated_standard", "standard", "", ""},
		{"generated_simple", "simple", "", ""},
		{"manual_minimum", "standard", "owner@example.com", "12345678"},
		{"manual_ascii_limit", "simple", "owner@example.com", strings.Repeat("a", 72)},
		{"manual_utf8_limit", "standard", "owner@example.com", strings.Repeat("界", 24)},
		{"manual_trimmed_email", "standard", " owner@example.com ", " valid-password "},
	} {
		t.Run("create/"+tc.name, func(t *testing.T) {
			reset(t)
			t.Setenv("RUN_MODE", tc.mode)
			cfg := &SetupConfig{Database: database, Admin: AdminConfig{Email: tc.email, Password: tc.password}}
			created, reason, err := createAdminUser(cfg)
			require.NoError(t, err)
			require.True(t, created)
			require.Equal(t, adminBootstrapReasonEmptyDatabase, reason)
			var email, hash, role, status string
			var balance float64
			var concurrency, count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT email,password_hash,role,status,balance,concurrency FROM users`).Scan(&email, &hash, &role, &status, &balance, &concurrency))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count))
			require.Equal(t, 1, count)
			require.Equal(t, cfg.Admin.Email, email)
			require.Equal(t, service.RoleAdmin, role)
			require.Equal(t, service.StatusActive, status)
			require.Zero(t, balance)
			require.Equal(t, setupDefaultAdminConcurrency(), concurrency)
			require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(cfg.Admin.Password)))
			require.NoError(t, binding.Validator.ValidateStruct(&struct {
				Email string `binding:"required,email"`
			}{Email: email}))
			if tc.email == "" {
				require.Regexp(t, `^admin-[0-9a-f]{12}@sub2api\.local$`, email)
				require.Len(t, cfg.Admin.Password, 32)
			} else {
				require.Equal(t, strings.TrimSpace(tc.email), email)
				require.Equal(t, tc.password, cfg.Admin.Password)
			}
		})
	}
	for _, role := range []string{service.RoleAdmin, service.RoleUser} {
		t.Run("existing/"+role, func(t *testing.T) {
			reset(t)
			_, err := db.ExecContext(ctx, `INSERT INTO users (email,password_hash,role,balance,concurrency,status,created_at,updated_at) VALUES ('existing@example.com','unchanged-hash',$1,17,9,'active',NOW(),NOW())`, role)
			require.NoError(t, err)
			cfg := &SetupConfig{Database: database, Admin: AdminConfig{Email: "invalid", Password: "123"}}
			before := cfg.Admin
			created, reason, err := createAdminUser(cfg)
			require.NoError(t, err, "invalid old environment credentials must not block an existing deployment")
			require.False(t, created)
			want := adminBootstrapReasonUsersExistWithoutAdmin
			if role == service.RoleAdmin {
				want = adminBootstrapReasonAdminExists
			}
			require.Equal(t, want, reason)
			require.Equal(t, before, cfg.Admin)
			var email, hash, gotRole string
			var balance float64
			var concurrency, count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT email,password_hash,role,balance,concurrency FROM users`).Scan(&email, &hash, &gotRole, &balance, &concurrency))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count))
			require.Equal(t, 1, count)
			require.Equal(t, "existing@example.com", email)
			require.Equal(t, "unchanged-hash", hash)
			require.Equal(t, role, gotRole)
			require.Equal(t, float64(17), balance)
			require.Equal(t, 9, concurrency)
		})
	}
	t.Run("concurrent_random_bootstraps_create_one_admin", func(t *testing.T) {
		reset(t)
		// Delay actual inserts without changing their values or return status.
		// The fixed path waits on bootstrap + fixture locks; the old unlocked
		// path waits with both inserts after both real count queries saw zero.
		_, err := db.ExecContext(ctx, `CREATE FUNCTION hold_fixture_admin_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock_shared(78931667::bigint); RETURN NEW; END $$;
		CREATE TRIGGER hold_fixture_admin_insert BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION hold_fixture_admin_insert()`)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := db.ExecContext(context.Background(), `DROP TRIGGER hold_fixture_admin_insert ON users; DROP FUNCTION hold_fixture_admin_insert()`)
			require.NoError(t, err)
		})
		locker, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = locker.Rollback() }()
		_, err = locker.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78931667::bigint)")
		require.NoError(t, err)
		type result struct {
			created bool
			reason  string
			err     error
			admin   AdminConfig
		}
		outcomes := make(chan result, 2)
		for i := 0; i < 2; i++ {
			go func() {
				cfg := &SetupConfig{Database: database}
				created, reason, err := createAdminUser(cfg)
				outcomes <- result{created, reason, err, cfg.Admin}
			}()
		}
		require.Eventually(t, func() bool {
			var waiting int
			err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&waiting)
			return err == nil && waiting == 2
		}, 2*time.Second, 10*time.Millisecond, "both independent bootstrap connections must reach the actual database barrier")
		require.NoError(t, locker.Commit())
		createdCount := 0
		var winning AdminConfig
		for i := 0; i < 2; i++ {
			select {
			case got := <-outcomes:
				require.NoError(t, got.err)
				if got.created {
					createdCount++
					winning = got.admin
					require.Equal(t, adminBootstrapReasonEmptyDatabase, got.reason)
				} else {
					require.Equal(t, adminBootstrapReasonAdminExists, got.reason)
					require.Equal(t, AdminConfig{}, got.admin, "loser must skip before generating credentials")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("bootstrap did not finish after the fixture lock was released")
			}
		}
		require.Equal(t, 1, createdCount, "random emails must not turn the count/insert race into multiple administrators")
		var count int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count))
		require.Equal(t, 1, count)
		var email, hash string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT email,password_hash FROM users`).Scan(&email, &hash))
		require.Equal(t, winning.Email, email)
		require.Regexp(t, `^admin-[0-9a-f]{12}@sub2api\.local$`, email)
		require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(winning.Password)))
	})
	t.Run("blocked_bootstrap_times_out_without_creating_and_can_retry", func(t *testing.T) {
		reset(t)
		locker, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = locker.Rollback() }()
		_, err = locker.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1, $2)", int32(0x53554232), int32(0x41444d4e))
		require.NoError(t, err)
		cfg := &SetupConfig{Database: database}
		started := time.Now()
		created, _, err := createAdminUser(cfg)
		require.Error(t, err)
		require.False(t, created)
		require.Less(t, time.Since(started), 8*time.Second, "existing five-second creation context must bound lock waiting")
		require.Equal(t, AdminConfig{}, cfg.Admin, "waiting installer must not generate credentials before owning the lock")
		var count int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count))
		require.Zero(t, count)
		require.NoError(t, locker.Commit())
		created, reason, err := createAdminUser(cfg)
		require.NoError(t, err, "interrupted bootstrap must release its transaction and remain retryable")
		require.True(t, created)
		require.Equal(t, adminBootstrapReasonEmptyDatabase, reason)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count))
		require.Equal(t, 1, count)
	})
}
