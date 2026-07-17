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
