//go:build unit

package repository

import (
	"context"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newConcurrencyCacheMiniRedis(t *testing.T) (*concurrencyCache, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return &concurrencyCache{rdb: client, slotTTLSeconds: 60, waitQueueTTLSeconds: 120}, client
}

func TestRefreshActiveIndexUsesSlotAndWaitAsSourceOfTruth(t *testing.T) {
	cache, client := newConcurrencyCacheMiniRedis(t)
	ctx := context.Background()
	accountID := int64(41)
	member := strconv.FormatInt(accountID, 10)

	require.NoError(t, client.ZAdd(ctx, accountSlotKey(accountID), redis.Z{Score: 1, Member: "request"}).Err())
	require.NoError(t, client.Set(ctx, accountWaitKey(accountID), 1, 0).Err())
	cache.refreshActiveIndex(ctx, accountActiveIndex, accountID)

	_, err := client.ZScore(ctx, accountActiveIndexKey, member).Result()
	require.NoError(t, err)

	require.NoError(t, client.Del(ctx, accountSlotKey(accountID), accountWaitKey(accountID)).Err())
	cache.refreshActiveIndex(ctx, accountActiveIndex, accountID)
	_, err = client.ZScore(ctx, accountActiveIndexKey, member).Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestRefreshActiveIndexDropsInvalidInput(t *testing.T) {
	cache, client := newConcurrencyCacheMiniRedis(t)
	ctx := context.Background()

	cache.touchActiveIndex(ctx, accountActiveIndexKey, 0, 60)
	members, err := client.ZRange(ctx, accountActiveIndexKey, 0, -1).Result()
	require.NoError(t, err)
	require.Empty(t, members)
}
