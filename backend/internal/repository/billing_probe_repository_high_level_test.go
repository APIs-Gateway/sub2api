package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestUpdateWithProbeCoversAccountMutationBranches(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	rateMultiplier := 1.25
	loadFactor := 2
	proxyID := int64(9)
	lastUsedAt := now.Add(-time.Minute)
	expiresAt := now.Add(time.Hour)
	rateLimitedAt := now.Add(-2 * time.Minute)
	rateLimitResetAt := now.Add(time.Minute)
	overloadUntil := now.Add(2 * time.Minute)
	sessionStart := now.Add(-3 * time.Minute)
	sessionEnd := now.Add(3 * time.Minute)
	note := "note"

	tests := []struct {
		name    string
		account *service.Account
		proxyID any
	}{
		{
			name: "all optional fields set",
			account: &service.Account{
				ID: 27, Name: "full", Notes: &note,
				Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra:       map[string]any{"custom": "value"},
				ProxyID:     &proxyID, RateMultiplier: &rateMultiplier, LoadFactor: &loadFactor,
				Concurrency: 3, Priority: 4, Status: service.StatusActive, Schedulable: true,
				LastUsedAt: &lastUsedAt, ExpiresAt: &expiresAt, RateLimitedAt: &rateLimitedAt,
				RateLimitResetAt: &rateLimitResetAt, OverloadUntil: &overloadUntil,
				SessionWindowStart: &sessionStart, SessionWindowEnd: &sessionEnd,
				SessionWindowStatus: "active",
			},
			proxyID: proxyID,
		},
		{
			name: "optional fields cleared and error status disables scheduling",
			account: &service.Account{
				ID: 28, Name: "sparse", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"},
				Status: service.StatusError, Schedulable: true,
			},
			proxyID: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })

			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT.*FROM accounts.*FOR NO KEY UPDATE`).
				WithArgs(tc.account.ID, tc.account.Platform, tc.account.Type, `{"api_key":"sk-test"}`, tc.proxyID).
				WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "enabled", "snapshot"}).
					AddRow(true, []byte(`true`), []byte(`{"status":"ok"}`)))
			mock.ExpectExec(`(?s)UPDATE "accounts" SET`).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`(?s)SELECT .* FROM "accounts" WHERE "id" = \$1`).
				WithArgs(tc.account.ID).
				WillReturnRows(updatedProbeAccountRows(tc.account.ID, `{"custom":"value"}`))
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
				WithArgs(service.SchedulerOutboxEventAccountChanged, tc.account.ID, nil, nil, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()

			repo := newAccountRepositoryWithSQL(client, db, nil)
			require.NoError(t, repo.Update(context.Background(), tc.account))
			require.False(t, tc.account.UpdatedAt.IsZero())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUpdateWithProbeReturnsPersistenceAndOutboxErrors(t *testing.T) {
	t.Run("nil account is a no-op", func(t *testing.T) {
		repo := &accountRepository{}
		require.NoError(t, repo.Update(context.Background(), nil))
	})

	t.Run("lock query error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		t.Cleanup(func() { _ = client.Close() })
		mock.ExpectBegin()
		mock.ExpectQuery(`(?s)SELECT.*FROM accounts.*FOR NO KEY UPDATE`).
			WithArgs(int64(29), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test"}`, nil).
			WillReturnError(errors.New("lock failed"))
		mock.ExpectRollback()
		repo := newAccountRepositoryWithSQL(client, db, nil)
		err := repo.Update(context.Background(), &service.Account{
			ID: 29, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-test"},
		})
		require.EqualError(t, err, "lock failed")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("outbox error rolls back account update", func(t *testing.T) {
		db, mock := newSQLMock(t)
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		t.Cleanup(func() { _ = client.Close() })
		mock.ExpectBegin()
		mock.ExpectQuery(`(?s)SELECT.*FROM accounts.*FOR NO KEY UPDATE`).
			WithArgs(int64(30), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test"}`, nil).
			WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "enabled", "snapshot"}).
				AddRow(true, []byte(`true`), []byte(`{"status":"ok"}`)))
		mock.ExpectExec(`(?s)UPDATE "accounts" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`(?s)SELECT .* FROM "accounts" WHERE "id" = \$1`).
			WithArgs(int64(30)).
			WillReturnRows(updatedProbeAccountRows(30, `{"upstream_billing_probe":{"status":"ok"}}`))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnError(errors.New("outbox failed"))
		mock.ExpectRollback()
		repo := newAccountRepositoryWithSQL(client, db, nil)
		err := repo.Update(context.Background(), &service.Account{
			ID: 30, Name: "account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-test"},
		})
		require.EqualError(t, err, "outbox failed")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestProxyUpdateInvalidatesProbeSnapshotsAtomically(t *testing.T) {
	t.Run("identity change clears snapshots", func(t *testing.T) {
		db, mock := newSQLMock(t)
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		t.Cleanup(func() { _ = client.Close() })
		mock.ExpectBegin()
		mock.ExpectQuery(`(?s)SELECT protocol, host, port.*FOR UPDATE`).
			WithArgs(int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
				AddRow("http", "old.example", 8080, "user", "pass", service.StatusActive))
		mock.ExpectExec(`(?s)UPDATE "proxies" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE "proxies" SET "backup_proxy_id" = NULL`).
			WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 0))
		expectProxyReloadRow(mock, 9, "new.example", "user", "pass")
		mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
			WithArgs(int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()

		repo := newProxyRepositoryWithSQL(client, db)
		err := repo.Update(context.Background(), &service.Proxy{
			ID: 9, Name: "proxy", Protocol: "http", Host: "new.example", Port: 8080,
			Username: "user", Password: "pass", Status: service.StatusActive,
		})
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("same identity skips invalidation", func(t *testing.T) {
		db, mock := newSQLMock(t)
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		t.Cleanup(func() { _ = client.Close() })
		mock.ExpectBegin()
		mock.ExpectQuery(`(?s)SELECT protocol, host, port.*FOR UPDATE`).
			WithArgs(int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
				AddRow("http", "same.example", 8080, "", "", service.StatusActive))
		mock.ExpectExec(`(?s)UPDATE "proxies" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE "proxies" SET "backup_proxy_id" = NULL`).
			WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 0))
		expectProxyReloadRow(mock, 9, "same.example", "", "")
		mock.ExpectCommit()

		repo := newProxyRepositoryWithSQL(client, db)
		err := repo.Update(context.Background(), &service.Proxy{
			ID: 9, Name: "renamed", Protocol: "http", Host: "same.example", Port: 8080,
			Status: service.StatusActive,
		})
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestProxyUpdateRollsBackWhenProbeOutboxFails(t *testing.T) {
	db, mock := newSQLMock(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT protocol, host, port.*FOR UPDATE`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "old.example", 8080, "", "", service.StatusActive))
	mock.ExpectExec(`(?s)UPDATE "proxies" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "proxies" SET "backup_proxy_id" = NULL`).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 0))
	expectProxyReloadRow(mock, 9, "new.example", "", "")
	mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	repo := newProxyRepositoryWithSQL(client, db)
	err := repo.Update(context.Background(), &service.Proxy{
		ID: 9, Name: "proxy", Protocol: "http", Host: "new.example", Port: 8080,
		Status: service.StatusActive,
	})
	require.EqualError(t, err, "outbox failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkUpdateProbeClearUsesTransactionAndOutbox(t *testing.T) {
	db, mock := newSQLMock(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE accounts SET .*extra = .* - 'upstream_billing_probe'`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(client, db, nil)
	rows, err := repo.BulkUpdate(context.Background(), []int64{27}, service.AccountBulkUpdate{
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: nil},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSweepExpiredProxyClearsProbeForBothFallbackModes(t *testing.T) {
	t.Run("without fallback clears snapshots", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectExec(`UPDATE proxies SET status=\$1`).
			WithArgs(service.StatusExpired, int64(9)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
			WithArgs(int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
			WillReturnResult(sqlmock.NewResult(1, 1))

		repo := &proxyRepository{}
		_, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, db, 9, nil, false)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("fallback target updates accounts and clears snapshots", func(t *testing.T) {
		exec := &recordingSQLExecutor{result: rowsAffectedResult(2)}
		target := int64(11)
		repo := &proxyRepository{}
		changed, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, exec, 9, &target, true)
		require.NoError(t, err)
		require.EqualValues(t, 2, changed)
		require.Len(t, exec.execQueries, 2)
		require.Contains(t, exec.execQueries[1], "proxy_id=$2")
		require.Contains(t, exec.execQueries[1], "- 'upstream_billing_probe'")
	})
}

func TestBillingProbePersistenceErrorEdges(t *testing.T) {
	t.Run("proxy identity query and scan errors", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(`(?s)SELECT protocol, host, port.*FOR UPDATE`).
			WithArgs(int64(9)).WillReturnError(errors.New("query failed"))
		_, err := lockProbeProxyIdentity(context.Background(), nil, db, 9)
		require.EqualError(t, err, "query failed")

		mock.ExpectQuery(`(?s)SELECT protocol, host, port.*FOR UPDATE`).
			WithArgs(int64(10)).
			WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
				AddRow("http", "proxy", "not-a-port", "", "", service.StatusActive))
		_, err = lockProbeProxyIdentity(context.Background(), nil, db, 10)
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("proxy identity row error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(`(?s)SELECT protocol, host, port.*FOR UPDATE`).
			WithArgs(int64(11)).
			WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
				RowError(0, errors.New("row failed")))
		_, err := lockProbeProxyIdentity(context.Background(), nil, db, 11)
		require.ErrorIs(t, err, service.ErrProxyNotFound)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("snapshot clear query scan and row errors", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
			WithArgs(int64(9)).WillReturnError(errors.New("clear query failed"))
		require.EqualError(t, clearProbeSnapshotsForProxy(context.Background(), nil, db, 9), "clear query failed")

		mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
			WithArgs(int64(10)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("not-an-id"))
		require.Error(t, clearProbeSnapshotsForProxy(context.Background(), nil, db, 10))

		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("account probe row and scan errors", func(t *testing.T) {
		db, mock := newSQLMock(t)
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		t.Cleanup(func() { _ = client.Close() })
		args := []driver.Value{int64(27), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test"}`, nil}
		mock.ExpectQuery(`(?s)SELECT.*FROM accounts.*FOR NO KEY UPDATE`).
			WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "enabled", "snapshot"}))
		_, err := lockAndMergeAccountProbeExtra(context.Background(), client, &service.Account{
			ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-test"},
		})
		require.ErrorIs(t, err, service.ErrAccountNotFound)

		mock.ExpectQuery(`(?s)SELECT.*FROM accounts.*FOR NO KEY UPDATE`).
			WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "enabled", "snapshot"}).
			AddRow("not-a-bool", []byte(`true`), []byte(`{"status":"ok"}`)))
		_, err = lockAndMergeAccountProbeExtra(context.Background(), client, &service.Account{
			ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-test"},
		})
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("nil snapshot and zero affected updates", func(t *testing.T) {
		repo := &accountRepository{}
		require.ErrorIs(t, repo.UpdateUpstreamBillingProbeSnapshot(context.Background(), nil, nil), service.ErrAccountNilInput)

		db, mock := newSQLMock(t)
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		t.Cleanup(func() { _ = client.Close() })
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE accounts`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		repo = newAccountRepositoryWithSQL(client, db, nil)
		require.ErrorIs(t, repo.UpdateCredentials(context.Background(), 27, map[string]any{"api_key": "sk-new"}), service.ErrAccountNotFound)

		mock.ExpectExec(`UPDATE accounts`).
			WithArgs(sqlmock.AnyArg(), int64(28), false).
			WillReturnResult(sqlmock.NewResult(0, 0))
		require.ErrorIs(t, repo.UpdateExtra(context.Background(), 28, map[string]any{service.UpstreamBillingProbeEnabledExtraKey: true}), service.ErrAccountNotFound)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("proxy expiry snapshot clear error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectExec(`UPDATE proxies SET status=\$1`).
			WithArgs(service.StatusExpired, int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).WillReturnError(errors.New("clear failed"))
		repo := &proxyRepository{}
		_, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, db, 9, nil, false)
		require.EqualError(t, err, "clear failed")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func updatedProbeAccountRows(id int64, extra string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(dbaccount.Columns).AddRow(
		id, now, now, nil, "test", nil, service.PlatformOpenAI, service.AccountTypeAPIKey,
		[]byte(`{"api_key":"sk-test"}`), []byte(extra), nil, nil, 1, nil, 1, 1.0,
		service.StatusActive, nil, nil, nil, false, true, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func expectProxyReloadRow(mock sqlmock.Sqlmock, id int64, host, username, password string) {
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)SELECT .* FROM "proxies" WHERE "id" = \$1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "created_at", "updated_at", "deleted_at", "name", "protocol", "host", "port",
			"username", "password", "status", "expires_at", "fallback_mode", "backup_proxy_id", "expiry_warn_days",
		}).AddRow(id, now, now, nil, "proxy", "http", host, 8080, username, password,
			service.StatusActive, nil, service.FallbackModeNone, nil, 0))
}
