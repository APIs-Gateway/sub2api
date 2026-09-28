package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheStickySessionCompareDeletePreservesNewBinding(t *testing.T) {
	mr := miniredis.RunT(t)
	cache := &gatewayCache{rdb: redis.NewClient(&redis.Options{Addr: mr.Addr()})}
	ctx := context.Background()
	const groupID int64 = 7
	const session = "openai:session"

	require.NoError(t, cache.SetSessionAccountID(ctx, groupID, session, 10, time.Minute))
	require.NoError(t, cache.SetSessionAccountID(ctx, groupID, session, 11, time.Minute))
	deleted, err := cache.CompareAndDeleteSessionAccountID(ctx, groupID, session, 10)
	require.NoError(t, err)
	require.False(t, deleted)
	current, err := cache.GetSessionAccountID(ctx, groupID, session)
	require.NoError(t, err)
	require.Equal(t, int64(11), current)

	deleted, err = cache.CompareAndDeleteSessionAccountID(ctx, groupID, session, 11)
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = cache.GetSessionAccountID(ctx, groupID, session)
	require.ErrorIs(t, err, redis.Nil)
}
