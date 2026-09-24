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

func TestCleanupExpiredAccountSlotKeysPrunesInvalidAndEmptyCandidates(t *testing.T) {
	cache, client := newConcurrencyCacheMiniRedis(t)
	ctx := context.Background()
	staleSlotID := int64(45)
	now := time.Now().Unix()

	// Keep both entries live in the index so CleanupExpiredAccountSlotKeys must
	// inspect them. The slot itself is older than the cache TTL and must be
	// deleted by the candidate cleanup rather than by index-score pruning.
	require.NoError(t, client.ZAdd(ctx, accountSlotKey(staleSlotID), redis.Z{
		Score:  float64(now - 61),
		Member: "expired-request",
	}).Err())
	require.NoError(t, client.ZAdd(ctx, accountActiveIndexKey,
		redis.Z{Score: float64(now + 60), Member: "invalid-account"},
		redis.Z{Score: float64(now + 60), Member: strconv.FormatInt(staleSlotID, 10)},
	).Err())

	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	_, err := client.ZScore(ctx, accountActiveIndexKey, "invalid-account").Result()
	require.ErrorIs(t, err, redis.Nil)
	_, err = client.ZScore(ctx, accountActiveIndexKey, strconv.FormatInt(staleSlotID, 10)).Result()
	require.ErrorIs(t, err, redis.Nil)
	exists, err := client.Exists(ctx, accountSlotKey(staleSlotID)).Result()
	require.NoError(t, err)
	require.Zero(t, exists)
}

func TestCleanupExpiredAccountSlotKeysContinuesAfterOneCandidateFails(t *testing.T) {
	cache, client := newConcurrencyCacheMiniRedis(t)
	ctx := context.Background()
	failingID := int64(46)
	cleanableID := int64(47)
	now := time.Now().Unix()

	// A wrong Redis type makes the first account's Lua cleanup fail. The
	// following candidate still has to be cleaned in the same worker pass.
	require.NoError(t, client.Set(ctx, accountSlotKey(failingID), "not-a-zset", 0).Err())
	require.NoError(t, client.ZAdd(ctx, accountSlotKey(cleanableID), redis.Z{
		Score:  float64(now - 61),
		Member: "expired-request",
	}).Err())
	require.NoError(t, client.ZAdd(ctx, accountActiveIndexKey,
		redis.Z{Score: float64(now + 60), Member: strconv.FormatInt(failingID, 10)},
		redis.Z{Score: float64(now + 60), Member: strconv.FormatInt(cleanableID, 10)},
	).Err())

	err := cache.CleanupExpiredAccountSlotKeys(ctx)
	require.Error(t, err)
	_, scoreErr := client.ZScore(ctx, accountActiveIndexKey, strconv.FormatInt(failingID, 10)).Result()
	require.NoError(t, scoreErr)
	_, scoreErr = client.ZScore(ctx, accountActiveIndexKey, strconv.FormatInt(cleanableID, 10)).Result()
	require.ErrorIs(t, scoreErr, redis.Nil)
	exists, existsErr := client.Exists(ctx, accountSlotKey(cleanableID)).Result()
	require.NoError(t, existsErr)
	require.Zero(t, exists)
}

func TestCleanupExpiredAccountSlotKeysReturnsRedisTimeError(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &concurrencyCache{rdb: client, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	mr.Close()

	require.Error(t, cache.CleanupExpiredAccountSlotKeys(context.Background()))
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
