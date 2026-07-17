package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestUpdateUpstreamBillingProbeSnapshotRequiresSameIdentityAndSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		affected int64
		wantErr  error
	}{
		{name: "same identity and snapshot", affected: 1},
		{name: "identity or snapshot changed", affected: 0, wantErr: service.ErrUpstreamBillingProbeIdentityChanged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })

			mock.ExpectBegin()
			tx, err := client.Tx(context.Background())
			require.NoError(t, err)
			mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR SHARE")).
				WithArgs(int64(9)).
				WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
					AddRow("http", "127.0.0.1", 3128, "user", "pass", service.StatusActive))
			mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+regexp.QuoteMeta("WHERE id = $2")+`.*`+regexp.QuoteMeta("AND platform = $3")+`.*`+regexp.QuoteMeta("AND type = $4")+`.*`+regexp.QuoteMeta("AND credentials = $5::jsonb")+`.*`+regexp.QuoteMeta("AND proxy_id IS NOT DISTINCT FROM $6")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_probe', 'null'::jsonb) = $7::jsonb")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_probe_enabled', 'null'::jsonb) = $8::jsonb")).
				WithArgs(sqlmock.AnyArg(), int64(17), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test","base_url":"http://127.0.0.1:8080"}`, int64(9), `{"status":"stale"}`, "null").
				WillReturnResult(sqlmock.NewResult(0, tt.affected))
			if tt.affected > 0 {
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
					WithArgs(service.SchedulerOutboxEventAccountChanged, int64(17), nil, nil, sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(1, 1))
			}

			repo := newAccountRepositoryWithSQL(client, &recordingSQLExecutor{err: errors.New("must use transaction client")}, nil)
			proxyID := int64(9)
			account := &service.Account{
				ID:          17,
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://127.0.0.1:8080"},
				ProxyID:     &proxyID,
				Proxy: &service.Proxy{
					ID: proxyID, Protocol: "http", Host: "127.0.0.1", Port: 3128,
					Username: "user", Password: "pass", Status: service.StatusActive,
				},
				Extra: map[string]any{service.UpstreamBillingProbeExtraKey: map[string]any{"status": "stale"}},
			}
			err = repo.UpdateUpstreamBillingProbeSnapshot(dbent.NewTxContext(context.Background(), tx), account, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUpdateUpstreamBillingProbeSnapshotCommitsSnapshotAndOutboxAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+regexp.QuoteMeta("AND credentials = $5::jsonb")+`.*`+regexp.QuoteMeta("AND proxy_id IS NOT DISTINCT FROM $6")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_probe', 'null'::jsonb) = $7::jsonb")).
		WithArgs(sqlmock.AnyArg(), int64(17), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test"}`, nil, "null", "null").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(17), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(client, db, nil)
	err = repo.UpdateUpstreamBillingProbeSnapshot(context.Background(), &service.Account{
		ID: 17, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateUpstreamBillingProbeSnapshotRejectsChangedProxyIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR SHARE")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "new.example", 3128, "user", "pass", service.StatusActive))

	proxyID := int64(9)
	repo := newAccountRepositoryWithSQL(client, db, nil)
	err = repo.UpdateUpstreamBillingProbeSnapshot(dbent.NewTxContext(context.Background(), tx), &service.Account{
		ID: 17, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"}, ProxyID: &proxyID,
		Proxy: &service.Proxy{ID: proxyID, Protocol: "http", Host: "old.example", Port: 3128, Username: "user", Password: "pass", Status: service.StatusActive},
	}, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK})
	require.ErrorIs(t, err, service.ErrUpstreamBillingProbeIdentityChanged)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateUpstreamBillingProbeSnapshotRollsBackWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+regexp.QuoteMeta("AND proxy_id IS NOT DISTINCT FROM $6")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_probe', 'null'::jsonb) = $7::jsonb")).
		WithArgs(sqlmock.AnyArg(), int64(18), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test"}`, nil, "null", "null").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	repo := newAccountRepositoryWithSQL(client, db, nil)
	err = repo.UpdateUpstreamBillingProbeSnapshot(context.Background(), &service.Account{
		ID: 18, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK})
	require.EqualError(t, err, "outbox failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProbePersistenceHelpers(t *testing.T) {
	proxyID := int64(7)
	require.Equal(t, probeProxyIdentity{}, probeProxyIdentityFromService(nil))
	require.Equal(t, probeProxyIdentity{protocol: "http", host: "example", port: 8080, username: "u", password: "p", status: service.StatusActive}, probeProxyIdentityFromService(&service.Proxy{
		ID: proxyID, Protocol: "http", Host: "example", Port: 8080, Username: "u", Password: "p", Status: service.StatusActive,
	}))
	require.True(t, upstreamBillingProbeExplicitlyDisabled(map[string]any{service.UpstreamBillingProbeEnabledExtraKey: false}))
	require.False(t, upstreamBillingProbeExplicitlyDisabled(map[string]any{service.UpstreamBillingProbeEnabledExtraKey: "false"}))
	require.True(t, upstreamBillingProbeSnapshotClearRequested(map[string]any{service.UpstreamBillingProbeExtraKey: nil}))
	require.False(t, upstreamBillingProbeSnapshotClearRequested(map[string]any{service.UpstreamBillingProbeExtraKey: "null"}))
}

func TestProxyProbePersistenceHelpersInvalidateOnlyRealSnapshots(t *testing.T) {
	db, mock := newSQLMock(t)

	mock.ExpectQuery(`(?s)UPDATE accounts.*platform = 'openai'.*type = 'apikey'.*extra \? 'upstream_billing_probe'.*extra -> 'upstream_billing_probe' <> 'null'::jsonb.*RETURNING id`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)).AddRow(int64(12)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountBulkChanged, nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	require.NoError(t, clearProbeSnapshotsForProxy(context.Background(), db, 7))
	require.NoError(t, mock.ExpectationsWereMet())

	mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
		WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	require.NoError(t, clearProbeSnapshotsForProxy(context.Background(), db, 8))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLockProbeProxyIdentity(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectQuery(`(?s)SELECT protocol, host, port.*FROM proxies.*FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "proxy.example", 8080, "user", "pass", service.StatusActive))
	identity, err := lockProbeProxyIdentity(context.Background(), db, 7)
	require.NoError(t, err)
	require.Equal(t, probeProxyIdentity{protocol: "http", host: "proxy.example", port: 8080, username: "user", password: "pass", status: service.StatusActive}, identity)
	require.NoError(t, mock.ExpectationsWereMet())

	mock.ExpectQuery(`(?s)SELECT protocol, host, port.*FROM proxies.*FOR UPDATE`).
		WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}))
	_, err = lockProbeProxyIdentity(context.Background(), db, 8)
	require.ErrorIs(t, err, service.ErrProxyNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLockAndMergeAccountProbeExtraUsesCurrentDatabaseSnapshot(t *testing.T) {
	db, mock := newSQLMock(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectQuery(`(?s)SELECT.*FROM accounts.*FOR NO KEY UPDATE`).
		WithArgs(int64(27), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test"}`, nil).
		WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "enabled", "snapshot"}).
			AddRow(true, []byte(`true`), []byte(`{"status":"ok"}`)))
	got, err := lockAndMergeAccountProbeExtra(context.Background(), client, &service.Account{
		ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey: false,
			service.UpstreamBillingProbeExtraKey:        map[string]any{"status": "stale"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, true, got[service.UpstreamBillingProbeEnabledExtraKey])
	require.Equal(t, map[string]any{"status": "ok"}, got[service.UpstreamBillingProbeExtraKey])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateExtraExplicitProbeDisableRemovesSnapshotAtomically(t *testing.T) {
	db, mock := newSQLMock(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE accounts SET extra = CASE.*- 'upstream_billing_probe'`).
		WithArgs(`{"upstream_billing_probe_enabled":false}`, int64(27), true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(27), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newAccountRepositoryWithSQL(client, db, nil)
	require.NoError(t, repo.UpdateExtra(context.Background(), 27, map[string]any{service.UpstreamBillingProbeEnabledExtraKey: false}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkUpdateNilProbeRemovesKey(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	_, err := repo.BulkUpdate(context.Background(), []int64{27}, service.AccountBulkUpdate{
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: nil},
	})
	require.NoError(t, err)
	require.NotEmpty(t, exec.execQueries)
	require.Contains(t, normalizeSQLWhitespace(exec.execQueries[0]), "- 'upstream_billing_probe'")
}

func TestUpdateCredentialsAtomicallyClearsProbeForIdentityChange(t *testing.T) {
	db, mock := newSQLMock(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE accounts.*credentials IS DISTINCT FROM \$1::jsonb.*- 'upstream_billing_probe'`).
		WithArgs(`{"api_key":"sk-new"}`, int64(27)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(27), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newAccountRepositoryWithSQL(client, db, nil)
	require.NoError(t, repo.UpdateCredentials(context.Background(), 27, map[string]any{"api_key": "sk-new"}))
	require.NoError(t, mock.ExpectationsWereMet())
}
