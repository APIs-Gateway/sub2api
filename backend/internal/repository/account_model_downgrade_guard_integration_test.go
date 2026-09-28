//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestModelDowngradeGuardKeepsOtherLimitsAndCapsPool(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	makeAccount := func(name string) int64 {
		account, err := client.Account.Create().
			SetName(name).SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).
			SetStatus(service.StatusActive).SetSchedulable(true).
			SetCredentials(map[string]any{}).SetExtra(map[string]any{}).
			SetConcurrency(1).SetPriority(1).Save(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.Account.DeleteOneID(account.ID).Exec(context.Background()) })
		return account.ID
	}
	first := makeAccount("model-downgrade-guard-first")
	second := makeAccount("model-downgrade-guard-second")
	until := time.Now().Add(time.Hour)
	require.NoError(t, repo.SetModelRateLimit(ctx, first, "gpt-6-astra", until, "upstream_429"))

	// A different source's active limit is never overwritten.
	applied, err := repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", until.Add(time.Hour), 1)
	require.NoError(t, err)
	require.False(t, applied)
	got, err := repo.GetByID(ctx, first)
	require.NoError(t, err)
	limits := got.Extra["model_rate_limits"].(map[string]any)
	require.Equal(t, "upstream_429", limits["gpt-6-astra"].(map[string]any)["reason"])

	// Another model on the same account can be quarantined without touching
	// the first key or changing the account's scheduling flag.
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-sol", until, 1)
	require.NoError(t, err)
	require.True(t, applied)
	got, err = repo.GetByID(ctx, first)
	require.NoError(t, err)
	require.True(t, got.Schedulable)
	limits = got.Extra["model_rate_limits"].(map[string]any)
	require.Equal(t, "upstream_429", limits["gpt-6-astra"].(map[string]any)["reason"])
	require.Equal(t, service.ModelDowngradeGuardReason, limits["gpt-6-sol"].(map[string]any)["reason"])
	// Existing guard block is idempotent.
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-sol", until.Add(time.Hour), 1)
	require.NoError(t, err)
	require.False(t, applied)
	// A restrictive ratio must fail closed before a second account is blocked.
	applied, err = repo.TryBlockDowngradedModel(ctx, second, "gpt-6-sol", until, 0.000001)
	require.NoError(t, err)
	require.False(t, applied)
}
