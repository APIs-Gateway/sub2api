//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func modelDowngradeTestCandidateFilter(ctx context.Context, candidate *service.Account, _ *int64) bool {
	return candidate.IsModelSupported("gpt-6-astra") && candidate.IsSchedulableForModelWithContext(ctx, "gpt-6-astra")
}

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
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
			_ = client.Account.DeleteOneID(account.ID).Exec(context.Background())
		})
		return account.ID
	}
	first := makeAccount("model-downgrade-guard-first")
	second := makeAccount("model-downgrade-guard-second")
	until := time.Now().Add(time.Hour)
	require.NoError(t, repo.SetModelRateLimit(ctx, first, "gpt-6-astra", until, "upstream_429"))

	// A different source's active limit is never overwritten.
	applied, err := repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until.Add(time.Hour), 1, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.False(t, applied)
	got, err := repo.GetByID(ctx, first)
	require.NoError(t, err)
	limits := got.Extra["model_rate_limits"].(map[string]any)
	require.Equal(t, "upstream_429", limits["gpt-6-astra"].(map[string]any)["reason"])

	// Another model on the same account can be quarantined without touching
	// the first key or changing the account's scheduling flag.
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-sol", "gpt-6-sol", until, 1, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.True(t, applied)
	got, err = repo.GetByID(ctx, first)
	require.NoError(t, err)
	require.True(t, got.Schedulable)
	limits = got.Extra["model_rate_limits"].(map[string]any)
	require.Equal(t, "upstream_429", limits["gpt-6-astra"].(map[string]any)["reason"])
	require.Equal(t, service.ModelDowngradeGuardReason, limits["gpt-6-sol"].(map[string]any)["reason"])
	// Existing guard block is idempotent.
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-sol", "gpt-6-sol", until.Add(time.Hour), 1, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.False(t, applied)
	// A restrictive ratio must fail closed before a second account is blocked.
	applied, err = repo.TryBlockDowngradedModel(ctx, second, "gpt-6-sol", "gpt-6-sol", until, 0.000001, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.False(t, applied)

	// A successful scheduled test of another model may clear ordinary limits,
	// but the explicit downgrade quarantine must keep its original expiry.
	require.NoError(t, repo.ClearModelRateLimitsExceptDowngrade(ctx, first))
	got, err = repo.GetByID(ctx, first)
	require.NoError(t, err)
	limits = got.Extra["model_rate_limits"].(map[string]any)
	require.NotContains(t, limits, "gpt-6-astra")
	require.Equal(t, service.ModelDowngradeGuardReason, limits["gpt-6-sol"].(map[string]any)["reason"])
	require.Equal(t, until.UTC().Format(time.RFC3339), limits["gpt-6-sol"].(map[string]any)["rate_limit_reset_at"])
	require.NoError(t, repo.ClearModelRateLimits(ctx, first))
	got, err = repo.GetByID(ctx, first)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, "model_rate_limits", "explicit cleanup may remove the guard")
	require.NoError(t, repo.SetModelRateLimit(ctx, first, "gpt-6-sol", time.Now().Add(-time.Minute), service.ModelDowngradeGuardReason))
	require.NoError(t, repo.ClearModelRateLimitsExceptDowngrade(ctx, first))
	got, err = repo.GetByID(ctx, first)
	require.NoError(t, err)
	require.Empty(t, got.Extra["model_rate_limits"], "expired guard can be cleaned up")
	// The account already consumes one global guard slot for another model;
	// moving to a second guarded model must not consume an extra ratio slot.
	require.NoError(t, repo.SetModelRateLimit(ctx, first, "other-model", time.Now().Add(time.Hour), service.ModelDowngradeGuardReason))
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until, 0.000001, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, repo.ClearModelRateLimits(ctx, first))
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = $1::jsonb WHERE id = $2`,
		`{"model_rate_limits":{"gpt-6-astra":{"reason":"upstream_429","rate_limit_reset_at":"bad timestamp"}}}`, first)
	require.NoError(t, err)
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until, 1, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.False(t, applied, "unknown target limit must not be overwritten")
}

func TestModelDowngradeGuardPreservesGroupModelCandidateWithMalformedOtherLimit(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	group, err := client.Group.Create().SetName("model-downgrade-pool").
		SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Group.DeleteOneID(group.ID).Exec(context.Background()) })
	makeAccount := func(name string, grouped bool, mapping map[string]any) int64 {
		credentials := map[string]any{}
		if mapping != nil {
			credentials["model_mapping"] = mapping
		}
		account, err := client.Account.Create().SetName(name).
			SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).
			SetStatus(service.StatusActive).SetSchedulable(true).
			SetCredentials(credentials).SetExtra(map[string]any{}).
			SetConcurrency(1).SetPriority(1).Save(ctx)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
			_ = client.Account.DeleteOneID(account.ID).Exec(context.Background())
		})
		if grouped {
			_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(group.ID).Save(ctx)
			require.NoError(t, err)
		}
		return account.ID
	}
	first := makeAccount("downgrade-pool-first", true, nil)
	second := makeAccount("downgrade-pool-second", true, nil)
	// Eight other accounts raise the global denominator; they cannot serve
	// this group. The second group account must still remain available.
	for i := 0; i < 8; i++ {
		makeAccount("downgrade-pool-unrelated-"+string(rune('a'+i)), false, nil)
	}
	other := makeAccount("downgrade-pool-malformed-other", false, nil)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = $1::jsonb WHERE id = $2`,
		`{"model_rate_limits":{"unrelated":{"reason":"upstream_429","rate_limit_reset_at":"bad timestamp"}}}`, other)
	require.NoError(t, err)
	malformedGuard := makeAccount("downgrade-pool-malformed-guard", false, nil)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = $1::jsonb WHERE id = $2`,
		`{"model_rate_limits":{"unrelated":{"reason":"model_downgrade","rate_limit_reset_at":"bad timestamp"}}}`, malformedGuard)
	require.NoError(t, err)

	until := time.Now().Add(time.Hour)
	// An account in the same group that does not support this request model
	// is not a witness for pool availability.
	require.NoError(t, client.Account.UpdateOneID(second).SetCredentials(map[string]any{
		"model_mapping": map[string]any{"other-model": "other-model"},
	}).Exec(ctx))
	applied, err := repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until, 0.3, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.False(t, applied)
	require.NoError(t, client.Account.UpdateOneID(second).SetCredentials(map[string]any{}).Exec(ctx))
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until, 0.3, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.True(t, applied)
	applied, err = repo.TryBlockDowngradedModel(ctx, second, "gpt-6-astra", "gpt-6-astra", until, 0.3, false, modelDowngradeTestCandidateFilter)
	require.NoError(t, err)
	require.False(t, applied)
}

func TestModelDowngradeGuardUngroupedSimpleAndSharedAccount(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	makeAccount := func(name string) int64 {
		account, err := client.Account.Create().SetName(name).
			SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).
			SetStatus(service.StatusActive).SetSchedulable(true).
			SetCredentials(map[string]any{}).SetExtra(map[string]any{}).
			SetConcurrency(1).SetPriority(1).Save(ctx)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
			_ = client.Account.DeleteOneID(account.ID).Exec(context.Background())
		})
		return account.ID
	}
	first := makeAccount("model-downgrade-ungrouped-first")
	_ = makeAccount("model-downgrade-ungrouped-second")
	until := time.Now().Add(time.Hour)
	filter := func(_ context.Context, candidate *service.Account, _ *int64) bool {
		return candidate.IsModelSupported("gpt-6-astra")
	}
	applied, err := repo.TryBlockDowngradedModel(ctx, first, " ", "gpt-6-astra", until, 1, false, filter)
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until, 1, false, nil)
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until, 1, false, filter)
	require.NoError(t, err)
	require.True(t, applied, "ungrouped accounts preserve another candidate")
	require.NoError(t, repo.ClearModelRateLimits(ctx, first))

	groupA, err := client.Group.Create().SetName("model-downgrade-shared-a").SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	groupB, err := client.Group.Create().SetName("model-downgrade-shared-b").SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = $1", first)
		_ = client.Group.DeleteOneID(groupA.ID).Exec(context.Background())
		_ = client.Group.DeleteOneID(groupB.ID).Exec(context.Background())
	})
	for _, groupID := range []int64{groupA.ID, groupB.ID} {
		_, err = client.AccountGroup.Create().SetAccountID(first).SetGroupID(groupID).Save(ctx)
		require.NoError(t, err)
	}
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until, 1, false, filter)
	require.NoError(t, err)
	require.False(t, applied, "one request alias cannot prove alternatives in every group")
	applied, err = repo.TryBlockDowngradedModel(ctx, first, "gpt-6-astra", "gpt-6-astra", until, 1, true, filter)
	require.NoError(t, err)
	require.True(t, applied, "simple mode uses the global account pool")
}
