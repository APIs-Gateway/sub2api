//go:build integration

package repository

import (
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func (s *ConcurrencyCacheSuite) TestStartupCleanupPreservesPeerCapacityOnRedis() {
	blue := NewConcurrencyCache(s.rdb, testSlotTTLMinutes, 120).(*concurrencyCache)
	green := NewConcurrencyCache(s.rdb, testSlotTTLMinutes, 120).(*concurrencyCache)
	for _, prefix := range []string{"rblue", "rlegacy", "rdead"} {
		acquired, err := blue.AcquireAccountSlot(s.ctx, 4901, 3, prefix+"-1")
		require.NoError(s.T(), err)
		require.True(s.T(), acquired)
		acquired, err = blue.AcquireUserSlot(s.ctx, 4902, 3, prefix+"-2")
		require.NoError(s.T(), err)
		require.True(s.T(), acquired)
		require.NoError(s.T(), blue.TrackAPIKeySlot(s.ctx, 4903, prefix+"-3"))
	}
	for _, id := range []int64{4901, 4904} {
		queued, err := blue.IncrementAccountWaitCount(s.ctx, id, 1)
		require.NoError(s.T(), err)
		require.True(s.T(), queued)
	}
	for _, id := range []int64{4902, 4905} {
		queued, err := blue.IncrementWaitCount(s.ctx, id, 1)
		require.NoError(s.T(), err)
		require.True(s.T(), queued)
	}
	// Existing score/TTL is the only atomically verifiable expiration signal.
	now, err := s.rdb.Time(s.ctx).Result()
	require.NoError(s.T(), err)
	for _, key := range []string{accountSlotKey(4901), userSlotKey(4902), apiKeySlotKey(4903)} {
		members, readErr := s.rdb.ZRange(s.ctx, key, 0, -1).Result()
		require.NoError(s.T(), readErr)
		for _, member := range members {
			if len(member) >= 6 && member[:6] == "rdead-" {
				require.NoError(s.T(), s.rdb.ZAdd(s.ctx, key, redis.Z{Score: float64(now.Unix()) - testSlotTTL.Seconds(), Member: member}).Err())
			}
		}
	}
	require.NoError(s.T(), green.CleanupStaleProcessSlots(s.ctx, "rgreen"))
	for _, entry := range []struct {
		key      string
		expected []string
	}{
		{accountSlotKey(4901), []string{"rblue-1", "rlegacy-1"}},
		{userSlotKey(4902), []string{"rblue-2", "rlegacy-2"}},
		{apiKeySlotKey(4903), []string{"rblue-3", "rlegacy-3"}},
	} {
		members, err := s.rdb.ZRange(s.ctx, entry.key, 0, -1).Result()
		require.NoError(s.T(), err)
		require.ElementsMatch(s.T(), entry.expected, members)
	}
	acquired, err := green.AcquireAccountSlot(s.ctx, 4901, 2, "rgreen-1")
	require.NoError(s.T(), err)
	require.False(s.T(), acquired)
	acquired, err = green.AcquireUserSlot(s.ctx, 4902, 2, "rgreen-2")
	require.NoError(s.T(), err)
	require.False(s.T(), acquired)
	for _, id := range []int64{4901, 4904} {
		queued, err := green.IncrementAccountWaitCount(s.ctx, id, 1)
		require.NoError(s.T(), err)
		require.False(s.T(), queued)
	}
	for _, id := range []int64{4902, 4905} {
		queued, err := green.IncrementWaitCount(s.ctx, id, 1)
		require.NoError(s.T(), err)
		require.False(s.T(), queued)
	}
}
