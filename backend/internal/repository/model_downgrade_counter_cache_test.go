package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestModelDowngradeCounterScopesAndExpiresHits(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewModelDowngradeCounterCache(rdb)
	ctx := context.Background()
	count, err := cache.IncrementModelDowngradeCount(ctx, 7, "gpt-6-astra", 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	count, err = cache.IncrementModelDowngradeCount(ctx, 7, "gpt-6-astra", 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	for _, key := range []struct {
		id    int64
		model string
	}{{7, "gpt-6-sol"}, {8, "gpt-6-astra"}} {
		count, err = cache.IncrementModelDowngradeCount(ctx, key.id, key.model, 1)
		require.NoError(t, err)
		require.EqualValues(t, 1, count)
	}
	mr.FastForward(time.Minute)
	count, err = cache.IncrementModelDowngradeCount(ctx, 7, "gpt-6-astra", 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, cache.ResetModelDowngradeCount(ctx, 7, "gpt-6-astra"))
	count, err = cache.IncrementModelDowngradeCount(ctx, 7, "gpt-6-astra", 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
}
