//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestProbePersistenceSQLiteUsesPortableEntAndOutboxPaths(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	_, err = db.Exec(`
		CREATE TABLE scheduler_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			account_id INTEGER,
			group_id INTEGER,
			payload TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			dedup_key TEXT
		);
		CREATE UNIQUE INDEX idx_scheduler_outbox_pending_dedup_key
			ON scheduler_outbox (dedup_key) WHERE dedup_key IS NOT NULL;
	`)
	require.NoError(t, err)

	proxyRow, err := client.Proxy.Create().
		SetName("probe-proxy").
		SetProtocol("http").
		SetHost("proxy.example").
		SetPort(8080).
		SetUsername("user").
		SetPassword("pass").
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	currentSnapshot := map[string]any{"status": service.UpstreamBillingProbeStatusOK}
	accountRow, err := client.Account.Create().
		SetName("probe-account").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"api_key": "sk-old"}).
		SetExtra(map[string]any{service.UpstreamBillingProbeExtraKey: currentSnapshot}).
		SetProxyID(proxyRow.ID).
		SetConcurrency(1).
		SetPriority(0).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetAutoPauseOnExpired(false).
		Save(ctx)
	require.NoError(t, err)

	proxyID := proxyRow.ID
	account := &service.Account{
		ID: accountRow.ID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-old"}, ProxyID: &proxyID,
		Proxy: &service.Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example", Port: 8080, Username: "user", Password: "pass", Status: service.StatusActive},
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: currentSnapshot},
	}

	extra, err := lockAndMergeAccountProbeExtra(ctx, client, account)
	require.NoError(t, err)
	require.Equal(t, currentSnapshot, extra[service.UpstreamBillingProbeExtraKey])

	repo := &accountRepository{client: client}
	require.NoError(t, updateCredentialsWithProbeEnt(ctx, client, accountRow.ID, map[string]any{"api_key": "sk-new"}))
	updated, err := client.Account.Get(ctx, accountRow.ID)
	require.NoError(t, err)
	require.NotContains(t, updated.Extra, service.UpstreamBillingProbeExtraKey)

	require.NoError(t, updateExtraWithProbeEnt(ctx, client, accountRow.ID, map[string]any{"custom": "value"}, false))
	updated, err = client.Account.Get(ctx, accountRow.ID)
	require.NoError(t, err)
	require.Equal(t, "value", updated.Extra["custom"])

	// Restore the identity and snapshot, then exercise the guarded durable write
	// and the SQLite-compatible scheduler outbox insert.
	_, err = client.Account.UpdateOneID(accountRow.ID).
		SetCredentials(map[string]any{"api_key": "sk-old"}).
		SetExtra(map[string]any{service.UpstreamBillingProbeExtraKey: currentSnapshot}).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, account, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK}))

	var outboxCount int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = ?", accountRow.ID).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	identity, err := lockProbeProxyIdentity(dbent.NewTxContext(ctx, tx), tx, proxyRow.ID)
	require.NoError(t, err)
	require.Equal(t, probeProxyIdentity{protocol: "http", host: "proxy.example", port: 8080, username: "user", password: "pass", status: service.StatusActive}, identity)
	require.NoError(t, tx.Rollback())
}

func TestProbePersistenceSQLiteCoversEntEdgePaths(t *testing.T) {
	db, client := newSQLiteProbePersistenceClient(t)
	ctx := context.Background()

	proxyRow, err := client.Proxy.Create().
		SetName("edge-proxy").
		SetProtocol("http").
		SetHost("proxy.example").
		SetPort(8080).
		SetUsername("user").
		SetPassword("pass").
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	snapshot := map[string]any{"status": service.UpstreamBillingProbeStatusOK}
	proxyID := proxyRow.ID
	accountRow, err := client.Account.Create().
		SetName("edge-account").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"api_key": "sk-edge"}).
		SetExtra(map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey: false,
			service.UpstreamBillingProbeExtraKey:        snapshot,
		}).
		SetProxyID(proxyID).
		SetConcurrency(1).
		SetPriority(0).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetAutoPauseOnExpired(false).
		Save(ctx)
	require.NoError(t, err)

	proxy := &service.Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example", Port: 8080, Username: "user", Password: "pass", Status: service.StatusActive}
	account := &service.Account{
		ID: accountRow.ID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-edge"}, ProxyID: &proxyID, Proxy: proxy,
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: snapshot},
	}

	merged, err := lockAndMergeAccountProbeExtra(ctx, client, account)
	require.NoError(t, err)
	require.Equal(t, false, merged[service.UpstreamBillingProbeEnabledExtraKey])
	require.NotContains(t, merged, service.UpstreamBillingProbeExtraKey)

	identityChanged := *account
	identityChanged.Credentials = map[string]any{"api_key": "sk-other"}
	merged, err = lockAndMergeAccountProbeExtra(ctx, client, &identityChanged)
	require.NoError(t, err)
	require.NotContains(t, merged, service.UpstreamBillingProbeEnabledExtraKey)
	require.NotContains(t, merged, service.UpstreamBillingProbeExtraKey)

	_, err = lockAndMergeAccountProbeExtra(ctx, client, &service.Account{ID: 999, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	require.ErrorIs(t, err, service.ErrAccountNotFound)

	require.True(t, probeAccountIdentityMatches(accountRow, account))
	require.False(t, probeAccountIdentityMatches(nil, account))
	noProxy := *account
	noProxy.ProxyID = nil
	require.False(t, probeAccountIdentityMatches(accountRow, &noProxy))

	require.NoError(t, updateCredentialsWithProbeEnt(ctx, client, accountRow.ID, map[string]any{"api_key": "sk-edge"}))
	updated, err := client.Account.Get(ctx, accountRow.ID)
	require.NoError(t, err)
	require.Contains(t, updated.Extra, service.UpstreamBillingProbeExtraKey)
	require.NoError(t, updateCredentialsWithProbeEnt(ctx, client, accountRow.ID, map[string]any{"api_key": "sk-new"}))
	updated, err = client.Account.Get(ctx, accountRow.ID)
	require.NoError(t, err)
	require.NotContains(t, updated.Extra, service.UpstreamBillingProbeExtraKey)
	require.ErrorIs(t, updateCredentialsWithProbeEnt(ctx, client, 999, map[string]any{"api_key": "missing"}), service.ErrAccountNotFound)

	require.NoError(t, updateExtraWithProbeEnt(ctx, client, accountRow.ID, map[string]any{
		"custom": "value", service.UpstreamBillingProbeExtraKey: snapshot,
	}, true))
	updated, err = client.Account.Get(ctx, accountRow.ID)
	require.NoError(t, err)
	require.Equal(t, "value", updated.Extra["custom"])
	require.NotContains(t, updated.Extra, service.UpstreamBillingProbeExtraKey)
	require.ErrorIs(t, updateExtraWithProbeEnt(ctx, client, 999, map[string]any{"custom": "missing"}, false), service.ErrAccountNotFound)

	// Restore the identity and expected snapshot for the guarded Ent update.
	_, err = client.Account.UpdateOneID(accountRow.ID).
		SetCredentials(map[string]any{"api_key": "sk-edge"}).
		SetExtra(map[string]any{service.UpstreamBillingProbeExtraKey: snapshot}).
		Save(ctx)
	require.NoError(t, err)
	account.Extra = map[string]any{service.UpstreamBillingProbeExtraKey: snapshot}
	repo := &accountRepository{client: client}
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, account, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK}))

	stale := *account
	stale.Extra = map[string]any{service.UpstreamBillingProbeExtraKey: map[string]any{"status": "stale"}}
	require.ErrorIs(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, &stale, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK}), service.ErrUpstreamBillingProbeIdentityChanged)
	missingProxy := *account
	missingProxy.ProxyID = nil
	require.ErrorIs(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, &missingProxy, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK}), service.ErrUpstreamBillingProbeIdentityChanged)

	require.True(t, probeProxyIdentityMatches(ctx, client, account))
	proxyMismatch := *account
	proxyMismatch.Proxy = &service.Proxy{ID: proxyID, Protocol: "https", Host: "proxy.example", Port: 8080, Username: "user", Password: "pass", Status: service.StatusActive}
	require.False(t, probeProxyIdentityMatches(ctx, client, &proxyMismatch))
	proxyMissing := *account
	proxyMissing.ProxyID = ptrInt64(999)
	require.False(t, probeProxyIdentityMatches(ctx, client, &proxyMissing))

	second, err := client.Account.Create().
		SetName("edge-account-two").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"api_key": "sk-two"}).
		SetExtra(map[string]any{"custom": "keep"}).
		SetProxyID(proxyID).
		SetConcurrency(1).
		SetPriority(0).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetAutoPauseOnExpired(false).
		Save(ctx)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	identity, err := lockProbeProxyIdentity(txCtx, tx, proxyID)
	require.NoError(t, err)
	require.Equal(t, probeProxyIdentity{protocol: "http", host: "proxy.example", port: 8080, username: "user", password: "pass", status: service.StatusActive}, identity)
	require.NoError(t, clearProbeSnapshotsForProxy(txCtx, tx, proxyID))
	require.NoError(t, tx.Commit())

	updated, err = client.Account.Get(ctx, accountRow.ID)
	require.NoError(t, err)
	require.NotContains(t, updated.Extra, service.UpstreamBillingProbeExtraKey)
	updated, err = client.Account.Get(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, "keep", updated.Extra["custom"])
	require.ErrorIs(t, func() error {
		tx, txErr := client.Tx(ctx)
		if txErr != nil {
			return txErr
		}
		defer func() { _ = tx.Rollback() }()
		_, lockErr := lockProbeProxyIdentity(dbent.NewTxContext(ctx, tx), tx, 999)
		return lockErr
	}(), service.ErrProxyNotFound)

	accountRepo := &accountRepository{client: client}
	require.NoError(t, accountRepo.UpdateExtra(ctx, accountRow.ID, map[string]any{"custom_two": "value"}))
	updated, err = client.Account.Get(ctx, accountRow.ID)
	require.NoError(t, err)
	require.Equal(t, "value", updated.Extra["custom_two"])
	require.NoError(t, accountRepo.UpdateExtra(ctx, accountRow.ID, map[string]any{service.UpstreamBillingProbeEnabledExtraKey: false}))
	require.NoError(t, accountRepo.UpdateCredentials(ctx, accountRow.ID, map[string]any{"api_key": "sk-final"}))
	require.ErrorIs(t, accountRepo.UpdateCredentials(ctx, 999, map[string]any{"api_key": "missing"}), service.ErrAccountNotFound)

	accountForUpdate := &service.Account{
		ID: accountRow.ID, Name: "edge-updated", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-final"}, Extra: map[string]any{"custom_three": "value"},
		Concurrency: 2, Priority: 1, Status: service.StatusActive, Schedulable: true, AutoPauseOnExpired: false,
	}
	require.NoError(t, accountRepo.Update(ctx, accountForUpdate))

	var outboxCount int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduler_outbox").Scan(&outboxCount))
	require.GreaterOrEqual(t, outboxCount, 2)
}

func newSQLiteProbePersistenceClient(t *testing.T) (*sql.DB, *dbent.Client) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	_, err = db.Exec(`
		CREATE TABLE scheduler_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			account_id INTEGER,
			group_id INTEGER,
			payload TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			dedup_key TEXT
		);
		CREATE UNIQUE INDEX idx_scheduler_outbox_pending_dedup_key
			ON scheduler_outbox (dedup_key) WHERE dedup_key IS NOT NULL;
	`)
	require.NoError(t, err)
	return db, client
}

func ptrInt64(value int64) *int64 { return &value }
