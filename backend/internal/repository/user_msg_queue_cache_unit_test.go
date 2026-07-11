//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newUserMsgQueueCacheMiniRedis(t *testing.T) (*userMsgQueueCache, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &userMsgQueueCache{rdb: client}, client
}

func TestReconcileExpiredLockCandidatesWithEmptyIndex(t *testing.T) {
	cache, _ := newUserMsgQueueCacheMiniRedis(t)
	cleaned, err := cache.ReconcileExpiredLockCandidates(context.Background(), 0)
	require.NoError(t, err)
	require.Zero(t, cleaned)
}

func TestReconcileExpiredLockCandidatesDropsInvalidCandidate(t *testing.T) {
	cache, client := newUserMsgQueueCacheMiniRedis(t)
	ctx := context.Background()
	nowMs, err := cache.GetCurrentTimeMs(ctx)
	require.NoError(t, err)
	require.NoError(t, client.ZAdd(ctx, umqLockIndexKey, redis.Z{Score: float64(nowMs), Member: "bad"}).Err())

	cleaned, err := cache.ReconcileExpiredLockCandidates(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, cleaned)
	_, err = client.ZScore(ctx, umqLockIndexKey, "bad").Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestRedisScriptInt64At(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result any
		index  int
		want   int64
		ok     bool
	}{
		{name: "int64", result: []any{int64(1)}, want: 1, ok: true},
		{name: "int", result: []any{int(2)}, want: 2, ok: true},
		{name: "string", result: []any{"3"}, want: 3, ok: true},
		{name: "bytes", result: []any{[]byte("4")}, want: 4, ok: true},
		{name: "bad type", result: []any{true}},
		{name: "bad value", result: []any{"not-a-number"}},
		{name: "missing", result: []any{}, index: 1},
		{name: "not array", result: int64(1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := redisScriptInt64At(tt.result, tt.index)
			if tt.ok {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestLockIndexMaintenanceToleratesRedisFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &userMsgQueueCache{rdb: client}
	mr.Close()

	cache.removeLockIndexMember(context.Background(), "1")
}
