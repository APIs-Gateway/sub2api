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

func TestTotpStepUpGrantIsScopedAndExpires(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := &TotpCache{rdb: client}
	ctx := context.Background()

	require.NoError(t, cache.SetStepUpGrant(ctx, 7, "sid-a", time.Minute))
	granted, err := cache.HasStepUpGrant(ctx, 7, "sid-a")
	require.NoError(t, err)
	require.True(t, granted)

	granted, err = cache.HasStepUpGrant(ctx, 7, "sid-b")
	require.NoError(t, err)
	require.False(t, granted)

	mr.FastForward(2 * time.Minute)
	granted, err = cache.HasStepUpGrant(ctx, 7, "sid-a")
	require.NoError(t, err)
	require.False(t, granted)
}

func TestTotpStepUpGrantReportsRedisFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := &TotpCache{rdb: client}
	mr.Close()

	require.Error(t, cache.SetStepUpGrant(context.Background(), 7, "sid-a", time.Minute))
	granted, err := cache.HasStepUpGrant(context.Background(), 7, "sid-a")
	require.Error(t, err)
	require.False(t, granted)
}
