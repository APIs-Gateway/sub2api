//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAuthCacheInvalidationOutboxTriggersAndTwoPassClaim(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE auth_cache_invalidation_outbox RESTART IDENTITY")
	require.NoError(t, err)

	user := mustCreateUser(t, integrationEntClient, &service.User{})
	key := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: user.ID, Key: "sk-outbox-" + time.Now().Format("150405.000000"), Name: "outbox"})
	_, err = integrationDB.ExecContext(ctx, "UPDATE api_keys SET status = $1 WHERE id = $2", service.StatusDisabled, key.ID)
	require.NoError(t, err)

	wantDigest := sha256.Sum256([]byte(key.Key))
	wantCacheKey := hex.EncodeToString(wantDigest[:])
	var storedCacheKey string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT cache_key FROM auth_cache_invalidation_outbox WHERE cache_key = $1", wantCacheKey).Scan(&storedCacheKey))
	require.Equal(t, wantCacheKey, storedCacheKey)

	repo := NewAuthCacheInvalidationOutboxRepository(integrationDB)
	events, err := repo.Claim(ctx, "integration-worker-1", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, wantCacheKey, events[0].CacheKey)
	require.Equal(t, 0, events[0].Stage)

	require.NoError(t, repo.ScheduleSecondPass(ctx, events[0].ID, "integration-worker-1", time.Now().Add(-time.Second)))
	secondPass, err := repo.Claim(ctx, "integration-worker-2", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, secondPass, 1)
	require.Equal(t, 1, secondPass[0].Stage)
	require.NoError(t, repo.DeleteClaimed(ctx, secondPass[0].ID, "integration-worker-2"))

	stats, err := repo.Stats(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.Pending)
}
