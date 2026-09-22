package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheOpenAIResponsesSessionWindowOwnership(t *testing.T) {
	mr := miniredis.RunT(t)
	cache := &gatewayCache{rdb: redis.NewClient(&redis.Options{Addr: mr.Addr()})}
	ctx := context.Background()

	previous, err := cache.ClaimOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner-a"), time.Minute)
	require.NoError(t, err)
	require.Empty(t, previous)
	previous, err = cache.ClaimOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner-b"), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []byte("owner-a"), previous)

	refreshed, err := cache.CompareAndRefreshOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner-a"), time.Minute)
	require.NoError(t, err)
	require.False(t, refreshed)
	refreshed, err = cache.CompareAndRefreshOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner-b"), time.Minute)
	require.NoError(t, err)
	require.True(t, refreshed)

	deleted, err := cache.CompareAndDeleteOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner-a"))
	require.NoError(t, err)
	require.False(t, deleted)
	deleted, err = cache.CompareAndDeleteOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner-b"))
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = cache.rdb.Get(ctx, buildOpenAIResponsesSessionWindowKey(7, "scope")).Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestGatewayCacheOpenAIResponsesSessionWindowRejectsInvalidClaims(t *testing.T) {
	ctx := context.Background()
	var unavailable *gatewayCache
	_, err := unavailable.ClaimOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner"), time.Minute)
	require.Error(t, err)
	refreshed, err := unavailable.CompareAndRefreshOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner"), time.Minute)
	require.Error(t, err)
	require.False(t, refreshed)
	deleted, err := unavailable.CompareAndDeleteOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner"))
	require.Error(t, err)
	require.False(t, deleted)

	mr := miniredis.RunT(t)
	cache := &gatewayCache{rdb: redis.NewClient(&redis.Options{Addr: mr.Addr()})}
	_, err = cache.ClaimOpenAIResponsesSessionWindow(ctx, 7, " ", []byte("owner"), time.Minute)
	require.Error(t, err)
	_, err = cache.ClaimOpenAIResponsesSessionWindow(ctx, 7, "scope", nil, time.Minute)
	require.Error(t, err)
	_, err = cache.ClaimOpenAIResponsesSessionWindow(ctx, 7, "scope", []byte("owner"), 0)
	require.Error(t, err)
	_, err = cache.CompareAndRefreshOpenAIResponsesSessionWindow(ctx, 7, "scope", nil, time.Minute)
	require.Error(t, err)
	_, err = cache.CompareAndRefreshOpenAIResponsesSessionWindow(ctx, 7, " ", []byte("owner"), time.Minute)
	require.Error(t, err)
	_, err = cache.CompareAndDeleteOpenAIResponsesSessionWindow(ctx, 7, "scope", nil)
	require.Error(t, err)
	_, err = cache.CompareAndDeleteOpenAIResponsesSessionWindow(ctx, 7, " ", []byte("owner"))
	require.Error(t, err)
}
