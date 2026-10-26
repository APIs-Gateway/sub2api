//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const imageBalanceTestScope = "openai:image_generation"

func newExtendModelRateLimitTestAccount(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	account, err := testEntClient(t).Account.Create().
		SetName(fmt.Sprintf("image-balance-reset-%d", time.Now().UnixNano())).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id = $1`, account.ID)
		_ = testEntClient(t).Account.DeleteOneID(account.ID).Exec(ctx)
	})
	return account.ID
}

func extendModelRateLimitTestState(t *testing.T, id int64) (string, string) {
	t.Helper()
	var extra []byte
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COALESCE(extra, '{}'::jsonb) FROM accounts WHERE id = $1`, id).Scan(&extra))
	var state struct {
		ModelRateLimits map[string]struct {
			ResetAt string `json:"rate_limit_reset_at"`
			Reason  string `json:"reason"`
		} `json:"model_rate_limits"`
	}
	require.NoError(t, json.Unmarshal(extra, &state))
	return state.ModelRateLimits[imageBalanceTestScope].ResetAt, state.ModelRateLimits[imageBalanceTestScope].Reason
}

func extendModelRateLimitOutboxCount(t *testing.T, id int64) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM scheduler_outbox WHERE account_id = $1`, id).Scan(&count))
	return count
}

func TestExtendModelRateLimitPreservesLater429Reset(t *testing.T) {
	ctx := context.Background()
	id := newExtendModelRateLimitTestAccount(t)
	cache := &schedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, cache)
	later := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetModelRateLimit(ctx, id, imageBalanceTestScope, later, "upstream_429"))
	local := later.In(time.FixedZone("UTC+08", 8*3600)).Format(time.RFC3339)
	_, err := integrationDB.ExecContext(ctx,
		`UPDATE accounts SET extra = $1::jsonb WHERE id = $2`,
		fmt.Sprintf(`{"model_rate_limits":{"%s":{"rate_limit_reset_at":"%s","reason":"upstream_429"}}}`, imageBalanceTestScope, local), id)
	require.NoError(t, err)
	before := extendModelRateLimitOutboxCount(t, id)
	beforeSnapshots := len(cache.setAccounts)

	require.NoError(t, repo.ExtendModelRateLimit(ctx, id, imageBalanceTestScope, time.Now().Add(5*time.Minute), "openai_images_insufficient_balance"))
	reset, reason := extendModelRateLimitTestState(t, id)
	require.Equal(t, local, reset, "the later reset and its timezone must remain unchanged")
	require.Equal(t, "upstream_429", reason, "the later reason must remain unchanged")
	require.Equal(t, before, extendModelRateLimitOutboxCount(t, id), "a no-op must not enqueue an outbox event")
	require.Len(t, cache.setAccounts, beforeSnapshots, "a no-op must not refresh the snapshot")
}

func TestExtendModelRateLimitReplacesMissingOrInvalidReset(t *testing.T) {
	for _, old := range []string{`{}`, `{"model_rate_limits":{"openai:image_generation":{"rate_limit_reset_at":"invalid","reason":"stale"}}}`} {
		t.Run(old, func(t *testing.T) {
			ctx := context.Background()
			id := newExtendModelRateLimitTestAccount(t)
			cache := &schedulerCacheRecorder{}
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, cache)
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = $1::jsonb WHERE id = $2`, old, id)
			require.NoError(t, err)
			before := extendModelRateLimitOutboxCount(t, id)
			resetAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)

			require.NoError(t, repo.ExtendModelRateLimit(ctx, id, imageBalanceTestScope, resetAt, "openai_images_insufficient_balance"))
			reset, reason := extendModelRateLimitTestState(t, id)
			require.Equal(t, resetAt.Format(time.RFC3339), reset)
			require.Equal(t, "openai_images_insufficient_balance", reason)
			require.Equal(t, before+1, extendModelRateLimitOutboxCount(t, id))
			require.Len(t, cache.setAccounts, 1)

			// An equal reset is also a no-op and retains the original reason.
			require.NoError(t, repo.ExtendModelRateLimit(ctx, id, imageBalanceTestScope, resetAt, "different_reason"))
			_, sameReason := extendModelRateLimitTestState(t, id)
			require.Equal(t, reason, sameReason)
			require.Equal(t, before+1, extendModelRateLimitOutboxCount(t, id))
			require.Len(t, cache.setAccounts, 1)
		})
	}
}

func TestExtendModelRateLimitConcurrent429CannotShortenReset(t *testing.T) {
	ctx := context.Background()
	id := newExtendModelRateLimitTestAccount(t)
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	later := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
	shorter := time.Now().UTC().Add(5 * time.Minute)
	transaction, err := integrationDB.BeginTx(ctx, &sql.TxOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = transaction.Rollback() })
	_, err = transaction.ExecContext(ctx,
		`UPDATE accounts SET extra = $1::jsonb WHERE id = $2`,
		fmt.Sprintf(`{"model_rate_limits":{"%s":{"rate_limit_reset_at":"%s","reason":"upstream_429"}}}`, imageBalanceTestScope, later.Format(time.RFC3339)), id)
	require.NoError(t, err)

	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		close(started)
		done <- repo.ExtendModelRateLimit(waitCtx, id, imageBalanceTestScope, shorter, "openai_images_insufficient_balance")
	}()
	<-started
	require.NoError(t, transaction.Commit())
	require.NoError(t, <-done)
	reset, reason := extendModelRateLimitTestState(t, id)
	require.Equal(t, later.Format(time.RFC3339), reset)
	require.Equal(t, "upstream_429", reason)
}
