//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepository_ListOAuthRefreshCandidates_Postgres(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	ctx := context.Background()

	oauth := mustCreateAccount(t, client, &service.Account{
		Name:        "oauth-candidate",
		Priority:    10,
		Credentials: map[string]any{"refresh_token": "oauth-refresh-token"},
	})
	setupToken := mustCreateAccount(t, client, &service.Account{
		Name:        "setup-token-candidate",
		Type:        service.AccountTypeSetupToken,
		Priority:    20,
		Credentials: map[string]any{"refresh_token": "setup-token-refresh-token"},
	})
	manualBackoff := mustCreateAccount(t, client, &service.Account{
		Name:        "manual-backoff-candidate",
		Priority:    30,
		Credentials: map[string]any{"refresh_token": "manual-backoff-refresh-token"},
	})
	retryExhausted := mustCreateAccount(t, client, &service.Account{
		Name:        "retry-exhausted-setup-token",
		Type:        service.AccountTypeSetupToken,
		Priority:    40,
		Credentials: map[string]any{"refresh_token": "retry-exhausted-refresh-token"},
	})
	disabled := mustCreateAccount(t, client, &service.Account{
		Name:        "disabled-setup-token",
		Type:        service.AccountTypeSetupToken,
		Status:      service.StatusDisabled,
		Priority:    50,
		Credentials: map[string]any{"refresh_token": "disabled-refresh-token"},
	})
	unsupportedPlatform := mustCreateAccount(t, client, &service.Account{
		Name:        "unsupported-platform-setup-token",
		Platform:    "unsupported",
		Type:        service.AccountTypeSetupToken,
		Priority:    60,
		Credentials: map[string]any{"refresh_token": "unsupported-platform-refresh-token"},
	})
	nonOAuth := mustCreateAccount(t, client, &service.Account{
		Name:        "api-key-with-refresh-token",
		Type:        service.AccountTypeAPIKey,
		Priority:    70,
		Credentials: map[string]any{"refresh_token": "api-key-refresh-token"},
	})
	missingRefreshToken := mustCreateAccount(t, client, &service.Account{
		Name:        "missing-refresh-token",
		Priority:    80,
		Credentials: map[string]any{"access_token": "access-token"},
	})
	emptyRefreshToken := mustCreateAccount(t, client, &service.Account{
		Name:        "empty-refresh-token",
		Priority:    90,
		Credentials: map[string]any{"refresh_token": "  "},
	})
	softDeleted := mustCreateAccount(t, client, &service.Account{
		Name:        "soft-deleted-setup-token",
		Type:        service.AccountTypeSetupToken,
		Priority:    100,
		Credentials: map[string]any{"refresh_token": "soft-deleted-refresh-token"},
	})

	future := time.Now().Add(time.Hour)
	require.NoError(t, client.Account.UpdateOneID(manualBackoff.ID).
		SetTempUnschedulableUntil(future).
		SetTempUnschedulableReason("manual backoff").
		Exec(ctx))
	require.NoError(t, client.Account.UpdateOneID(retryExhausted.ID).
		SetTempUnschedulableUntil(future).
		SetTempUnschedulableReason("token refresh retry exhausted: upstream unavailable").
		Exec(ctx))
	require.NoError(t, client.Account.UpdateOneID(softDeleted.ID).
		SetDeletedAt(time.Now()).
		Exec(ctx))

	accounts, err := repo.ListOAuthRefreshCandidates(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{oauth.ID, setupToken.ID, manualBackoff.ID}, idsOfAccounts(accounts))
	require.NotContains(t, idsOfAccounts(accounts), retryExhausted.ID)
	require.NotContains(t, idsOfAccounts(accounts), disabled.ID)
	require.NotContains(t, idsOfAccounts(accounts), unsupportedPlatform.ID)
	require.NotContains(t, idsOfAccounts(accounts), nonOAuth.ID)
	require.NotContains(t, idsOfAccounts(accounts), missingRefreshToken.ID)
	require.NotContains(t, idsOfAccounts(accounts), emptyRefreshToken.ID)
	require.NotContains(t, idsOfAccounts(accounts), softDeleted.ID)
}
