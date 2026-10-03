//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestStartupCleanupPreservesLiveAndUnknownPeers(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 120).(*concurrencyCache)
	start := time.Now()
	server.SetTime(start)
	require.NoError(t, cache.HeartbeatProcess(ctx, "rdead"))
	server.SetTime(start.Add(61 * time.Second))
	require.NoError(t, cache.HeartbeatProcess(ctx, "rlive"))
	require.NoError(t, cache.HeartbeatProcess(ctx, "rnew"))

	for _, prefix := range []string{"rdead", "rlive", "roldversion", "rdeadly"} {
		acquired, err := cache.AcquireAccountSlot(ctx, 10, 4, prefix+"-1")
		require.NoError(t, err)
		require.True(t, acquired)
		acquired, err = cache.AcquireUserSlot(ctx, 20, 4, prefix+"-2")
		require.NoError(t, err)
		require.True(t, acquired)
		require.NoError(t, cache.TrackAPIKeySlot(ctx, 30, prefix+"-3"))
	}
	queued, err := cache.IncrementAccountWaitCount(ctx, 10, 1)
	require.NoError(t, err)
	require.True(t, queued)
	queued, err = cache.IncrementWaitCount(ctx, 20, 1)
	require.NoError(t, err)
	require.True(t, queued)
	// A live peer may be waiting without holding a slot on this entity.
	queued, err = cache.IncrementAccountWaitCount(ctx, 11, 1)
	require.NoError(t, err)
	require.True(t, queued)
	queued, err = cache.IncrementWaitCount(ctx, 21, 1)
	require.NoError(t, err)
	require.True(t, queued)

	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "rnew"))
	for _, key := range []string{accountSlotKey(10), userSlotKey(20), apiKeySlotKey(30)} {
		members, readErr := client.ZRange(ctx, key, 0, -1).Result()
		require.NoError(t, readErr)
		require.Len(t, members, 3, "only the exact stale prefix must be removed: %s", key)
		for _, member := range members {
			require.NotContains(t, member, "rdead-")
		}
	}
	acquired, err := cache.AcquireAccountSlot(ctx, 10, 3, "rnew-1")
	require.NoError(t, err)
	require.False(t, acquired)
	acquired, err = cache.AcquireUserSlot(ctx, 20, 3, "rnew-2")
	require.NoError(t, err)
	require.False(t, acquired)
	for _, id := range []int64{10, 11} {
		queued, err = cache.IncrementAccountWaitCount(ctx, id, 1)
		require.NoError(t, err)
		require.False(t, queued)
	}
	for _, id := range []int64{20, 21} {
		queued, err = cache.IncrementWaitCount(ctx, id, 1)
		require.NoError(t, err)
		require.False(t, queued)
	}
	server.FastForward(121 * time.Second)
	queued, err = cache.IncrementAccountWaitCount(ctx, 11, 1)
	require.NoError(t, err)
	require.True(t, queued, "unowned residue still expires through the existing wait TTL")
}

func TestStartupCleanupKeepsCurrentAndBoundaryHeartbeat(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 120).(*concurrencyCache)
	start := time.Now()
	server.SetTime(start)
	require.NoError(t, cache.HeartbeatProcess(ctx, "rboundary"))
	require.NoError(t, cache.HeartbeatProcess(ctx, "rself"))
	server.SetTime(start.Add(60 * time.Second))
	acquired, err := cache.AcquireAccountSlot(ctx, 10, 2, "rboundary-1")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "rself"))
	members, err := client.ZRange(ctx, accountSlotKey(10), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"rboundary-1"}, members, "heartbeat at the window boundary remains live")
	server.SetTime(start.Add(61 * time.Second))
	acquired, err = cache.AcquireAccountSlot(ctx, 11, 1, "rself-1")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "rself"))
	members, err = client.ZRange(ctx, accountSlotKey(11), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"rself-1"}, members)
}

func TestStartupCleanupHeartbeatReadFailureLeavesSlotsAndWaits(t *testing.T) {
	ctx := context.Background()
	cache, client := newConcurrencyCacheMiniRedis(t)
	acquired, err := cache.AcquireAccountSlot(ctx, 10, 1, "peer-1")
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = cache.IncrementAccountWaitCount(ctx, 10, 1)
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, processHeartbeatKey, "wrong-type", 0).Err())
	require.Error(t, cache.CleanupStaleProcessSlots(ctx, "new"))
	members, err := client.ZRange(ctx, accountSlotKey(10), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"peer-1"}, members)
	waiting, err := client.Get(ctx, accountWaitKey(10)).Int()
	require.NoError(t, err)
	require.Equal(t, 1, waiting)
}
