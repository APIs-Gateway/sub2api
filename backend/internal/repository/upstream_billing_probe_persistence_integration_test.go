//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountUpdatePreservesProbeAndIdentityChangesClearIt(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "probe-persistence-update", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-old"},
		Extra:       map[string]any{service.UpstreamBillingProbeEnabledExtraKey: true},
	})

	loaded, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, loaded, &service.UpstreamBillingProbeSnapshot{
		Status: service.UpstreamBillingProbeStatusOK, LastAttemptAt: time.Now().UTC(),
	}))

	loaded.Name = "ordinary-edit"
	require.NoError(t, repo.Update(ctx, loaded))
	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Contains(t, got.Extra, service.UpstreamBillingProbeExtraKey)

	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{service.UpstreamBillingProbeEnabledExtraKey: false}))
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
	require.Equal(t, false, got.Extra[service.UpstreamBillingProbeEnabledExtraKey])

	got.Credentials["api_key"] = "sk-new"
	require.NoError(t, repo.Update(ctx, got))
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
}

func TestBulkAndCredentialUpdatesClearProbeSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	newAccount := func(name string) *service.Account {
		return mustCreateAccount(t, tx.Client(), &service.Account{
			Name: name, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-old"},
			Extra: map[string]any{
				service.UpstreamBillingProbeEnabledExtraKey: true,
				service.UpstreamBillingProbeExtraKey:        map[string]any{"status": service.UpstreamBillingProbeStatusOK},
			},
		})
	}

	bulkAccount := newAccount("probe-bulk-clear")
	_, err := repo.BulkUpdate(ctx, []int64{bulkAccount.ID}, service.AccountBulkUpdate{
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: nil},
	})
	require.NoError(t, err)
	got, err := repo.GetByID(ctx, bulkAccount.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)

	credentialAccount := newAccount("probe-credentials-clear")
	require.NoError(t, repo.UpdateCredentials(ctx, credentialAccount.ID, map[string]any{"api_key": "sk-new"}))
	got, err = repo.GetByID(ctx, credentialAccount.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
}

func TestProxyIdentityUpdateInvalidatesProbeAndRejectsStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	accountRepo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	proxyRepo := newProxyRepositoryWithSQL(tx.Client(), tx)
	proxy := mustCreateProxy(t, tx.Client(), &service.Proxy{
		Name: "probe-proxy", Protocol: "http", Host: "old.example", Port: 8080,
		Username: "old-user", Password: "old-pass", Status: service.StatusActive,
	})
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "proxy-probe-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"}, ProxyID: &proxy.ID,
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey: true,
			service.UpstreamBillingProbeExtraKey:        map[string]any{"status": service.UpstreamBillingProbeStatusOK},
		},
	})

	inFlight, err := accountRepo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	proxyToUpdate, err := proxyRepo.GetByID(ctx, proxy.ID)
	require.NoError(t, err)
	proxyToUpdate.Host = "new.example"
	require.NoError(t, proxyRepo.Update(ctx, proxyToUpdate))

	got, err := accountRepo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
	require.ErrorIs(t, accountRepo.UpdateUpstreamBillingProbeSnapshot(ctx, inFlight, &service.UpstreamBillingProbeSnapshot{
		Status: service.UpstreamBillingProbeStatusOK,
	}), service.ErrUpstreamBillingProbeIdentityChanged)

	var payloadJSON []byte
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT payload FROM scheduler_outbox
		WHERE event_type = $1 ORDER BY id DESC LIMIT 1
	`, []any{service.SchedulerOutboxEventAccountBulkChanged}, &payloadJSON))
	var payload struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))
	require.Equal(t, []int64{account.ID}, payload.AccountIDs)
}

func TestSweepExpiredProxyWithoutFallbackInvalidatesOnlyRealSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	proxyRepo := newProxyRepositoryWithSQL(tx.Client(), tx)
	accountRepo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	past := time.Now().Add(-time.Hour)
	proxy := mustCreateProxy(t, tx.Client(), &service.Proxy{
		Name: "expired-probe-proxy", Protocol: "http", Host: "127.0.0.1", Port: 8080,
		Status: service.StatusActive, ExpiresAt: &past, FallbackMode: service.FallbackModeNone,
	})
	withSnapshot := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "expired-with-snapshot", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"}, ProxyID: &proxy.ID,
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: map[string]any{"status": service.UpstreamBillingProbeStatusOK}},
	})
	withoutSnapshot := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "expired-without-snapshot", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test-2"}, ProxyID: &proxy.ID,
	})

	changed, err := proxyRepo.SweepExpiredProxies(ctx, time.Now())
	require.NoError(t, err)
	require.Zero(t, changed)
	got, err := accountRepo.GetByID(ctx, withSnapshot.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
	got, err = accountRepo.GetByID(ctx, withoutSnapshot.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)

	var payloadJSON []byte
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT payload FROM scheduler_outbox
		WHERE event_type = $1 ORDER BY id DESC LIMIT 1
	`, []any{service.SchedulerOutboxEventAccountBulkChanged}, &payloadJSON))
	var payload struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))
	require.Equal(t, []int64{withSnapshot.ID}, payload.AccountIDs)
}
