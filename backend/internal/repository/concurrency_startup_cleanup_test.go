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

func TestStartupCleanupPreservesPeerSlotsAndWaitingLimits(t *testing.T) {
	ctx := context.Background()
	cache, client := newConcurrencyCacheMiniRedis(t)
	now, err := client.Time(ctx).Result()
	require.NoError(t, err)
	for _, spec := range []activeIndexSpec{accountActiveIndex, userActiveIndex, apiKeyActiveIndex} {
		key := spec.slotKey(10)
		require.NoError(t, client.ZAdd(ctx, key,
			redis.Z{Score: float64(now.Unix()), Member: "live-1"},
			redis.Z{Score: float64(now.Unix()), Member: "legacy-1"},
			redis.Z{Score: float64(now.Unix()) - 60, Member: "expired-boundary-1"},
			redis.Z{Score: float64(now.Unix()) - 61, Member: "expired-1"},
		).Err())
		cache.refreshActiveIndex(ctx, spec, 10)
	}
	for _, id := range []int64{10, 11} {
		queued, queueErr := cache.IncrementAccountWaitCount(ctx, id, 1)
		require.NoError(t, queueErr)
		require.True(t, queued)
		queued, queueErr = cache.IncrementWaitCount(ctx, id, 1)
		require.NoError(t, queueErr)
		require.True(t, queued)
	}
	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "new"))
	for _, spec := range []activeIndexSpec{accountActiveIndex, userActiveIndex, apiKeyActiveIndex} {
		members, readErr := client.ZRange(ctx, spec.slotKey(10), 0, -1).Result()
		require.NoError(t, readErr)
		require.ElementsMatch(t, []string{"live-1", "legacy-1"}, members)
	}
	acquired, err := cache.AcquireAccountSlot(ctx, 10, 2, "new-1")
	require.NoError(t, err)
	require.False(t, acquired)
	acquired, err = cache.AcquireUserSlot(ctx, 10, 2, "new-2")
	require.NoError(t, err)
	require.False(t, acquired)
	for _, id := range []int64{10, 11} {
		queued, queueErr := cache.IncrementAccountWaitCount(ctx, id, 1)
		require.NoError(t, queueErr)
		require.False(t, queued)
		queued, queueErr = cache.IncrementWaitCount(ctx, id, 1)
		require.NoError(t, queueErr)
		require.False(t, queued)
	}
}

func TestStartupCleanupPreservesPeerAfterStaleHeartbeatRefresh(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 120).(*concurrencyCache)
	start := time.Now()
	server.SetTime(start)
	const heartbeatKey = "concurrency:process:heartbeat"
	require.NoError(t, client.ZAdd(ctx, heartbeatKey, redis.Z{Score: float64(start.Unix() - 61), Member: "peer"}).Err())
	acquired, err := cache.AcquireAccountSlot(ctx, 10, 1, "peer-1")
	require.NoError(t, err)
	require.True(t, acquired)
	stale, err := client.ZRangeByScore(ctx, heartbeatKey, &redis.ZRangeBy{Min: "-inf", Max: "(" + strconv.FormatInt(start.Unix()-60, 10)}).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"peer"}, stale)
	// Resume after a stale snapshot or Redis outage while the request is still in flight.
	server.SetTime(start.Add(61 * time.Second))
	require.NoError(t, client.ZAdd(ctx, heartbeatKey, redis.Z{Score: float64(start.Unix() + 61), Member: "peer"}).Err())
	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "new"))
	members, err := client.ZRange(ctx, accountSlotKey(10), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"peer-1"}, members)
	acquired, err = cache.AcquireAccountSlot(ctx, 10, 1, "new-1")
	require.NoError(t, err)
	require.False(t, acquired)
}

func TestStartupCleanupPropagatesEachIndexErrorWithoutDeletingPeerCapacity(t *testing.T) {
	for _, failedSpec := range []activeIndexSpec{accountActiveIndex, userActiveIndex, apiKeyActiveIndex} {
		t.Run(failedSpec.indexKey, func(t *testing.T) {
			ctx := context.Background()
			cache, client := newConcurrencyCacheMiniRedis(t)
			acquired, err := cache.AcquireAccountSlot(ctx, 10, 1, "peer-1")
			require.NoError(t, err)
			require.True(t, acquired)
			acquired, err = cache.AcquireUserSlot(ctx, 10, 1, "peer-2")
			require.NoError(t, err)
			require.True(t, acquired)
			require.NoError(t, cache.TrackAPIKeySlot(ctx, 10, "peer-3"))
			queued, err := cache.IncrementAccountWaitCount(ctx, 10, 1)
			require.NoError(t, err)
			require.True(t, queued)
			queued, err = cache.IncrementWaitCount(ctx, 10, 1)
			require.NoError(t, err)
			require.True(t, queued)
			// A broken index must return an actionable error, including when
			// preceding indexes were already visited. Valid slots/waits stay.
			require.NoError(t, client.Set(ctx, failedSpec.indexKey, "wrong-type", 0).Err())
			err = cache.CleanupStaleProcessSlots(ctx, "new")
			require.ErrorContains(t, err, "read active index "+failedSpec.indexKey)
			require.ErrorContains(t, err, "WRONGTYPE")
			for _, entry := range []struct {
				key    string
				member string
			}{
				{accountSlotKey(10), "peer-1"},
				{userSlotKey(10), "peer-2"},
				{apiKeySlotKey(10), "peer-3"},
			} {
				members, readErr := client.ZRange(ctx, entry.key, 0, -1).Result()
				require.NoError(t, readErr)
				require.Equal(t, []string{entry.member}, members)
			}
			for _, key := range []string{accountWaitKey(10), waitQueueKey(10)} {
				waiting, readErr := client.Get(ctx, key).Int()
				require.NoError(t, readErr)
				require.Equal(t, 1, waiting)
			}
		})
	}
}
