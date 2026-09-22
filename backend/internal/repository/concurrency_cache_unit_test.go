//go:build unit

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

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

	require.NoError(t, client.Del(ctx, accountSlotKey(accountID)).Err())
	cache.refreshActiveIndex(ctx, accountActiveIndex, accountID)
	_, err = client.ZScore(ctx, accountActiveIndexKey, member).Result()
	require.NoError(t, err)

	require.NoError(t, client.Del(ctx, accountWaitKey(accountID)).Err())
	cache.refreshActiveIndex(ctx, accountActiveIndex, accountID)
	_, err = client.ZScore(ctx, accountActiveIndexKey, member).Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestRefreshActiveIndexDropsInvalidInput(t *testing.T) {
	cache, client := newConcurrencyCacheMiniRedis(t)
	ctx := context.Background()

	cache.touchActiveIndex(ctx, accountActiveIndexKey, 0, 60)
	cache.refreshActiveIndex(ctx, accountActiveIndex, 0)
	members, err := client.ZRange(ctx, accountActiveIndexKey, 0, -1).Result()
	require.NoError(t, err)
	require.Empty(t, members)
}

func TestCleanupStaleProcessSlotsWithEmptyIndexes(t *testing.T) {
	cache, _ := newConcurrencyCacheMiniRedis(t)
	require.NoError(t, cache.CleanupStaleProcessSlots(context.Background(), "active-"))
}

func TestCleanupExpiredAccountSlotKeysUsesOnlyActiveIndexCandidates(t *testing.T) {
	cache, client := newConcurrencyCacheMiniRedis(t)
	ctx := context.Background()
	activeID := int64(43)
	staleID := int64(44)
	now := time.Now().Unix()

	require.NoError(t, client.ZAdd(ctx, accountSlotKey(activeID), redis.Z{Score: float64(now), Member: "request"}).Err())
	require.NoError(t, client.ZAdd(ctx, accountActiveIndexKey,
		redis.Z{Score: float64(now + 60), Member: strconv.FormatInt(activeID, 10)},
		redis.Z{Score: float64(now - 1), Member: strconv.FormatInt(staleID, 10)},
	).Err())

	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	activeMembers, err := client.ZRange(ctx, accountSlotKey(activeID), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"request"}, activeMembers)
	_, err = client.ZScore(ctx, accountActiveIndexKey, strconv.FormatInt(staleID, 10)).Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestActiveIndexBestEffortWhenRedisUnavailable(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &concurrencyCache{rdb: client, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	mr.Close()

	cache.touchActiveIndex(context.Background(), accountActiveIndexKey, 1, 60)
	cache.refreshActiveIndex(context.Background(), accountActiveIndex, 1)
}
