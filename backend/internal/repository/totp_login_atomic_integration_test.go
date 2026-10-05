//go:build integration

package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestTotpLoginAtomicRedisClaim(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := &TotpCache{rdb: rdb}
	token := "atomic-login-" + time.Now().Format(time.RFC3339Nano)
	expected := &service.TotpLoginSession{UserID: 7, Email: "fixture@example.com", TokenExpiry: time.Now().Add(time.Minute), PendingOAuthBind: &service.PendingOAuthBindLoginSession{PendingSessionToken: "pending", BrowserSessionKey: "browser"}}
	require.NoError(t, cache.SetLoginSession(ctx, token, expected, time.Minute))
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			session, err := cache.ConsumeLoginSession(ctx, token)
			require.NoError(t, err)
			if session != nil {
				winners.Add(1)
				require.Equal(t, expected.UserID, session.UserID)
				require.Equal(t, expected.PendingOAuthBind, session.PendingOAuthBind)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())
	session, err := cache.GetLoginSession(ctx, token)
	require.NoError(t, err)
	require.Nil(t, session)

	t.Run("expired", func(t *testing.T) {
		require.NoError(t, cache.SetLoginSession(ctx, token, expected, time.Millisecond))
		require.Eventually(t, func() bool { return rdb.Exists(ctx, totpLoginKeyPrefix+token).Val() == 0 }, time.Second, time.Millisecond)
		session, err := cache.ConsumeLoginSession(ctx, token)
		require.NoError(t, err)
		require.Nil(t, session)
	})
	t.Run("malformed_is_not_reusable", func(t *testing.T) {
		require.NoError(t, rdb.Set(ctx, totpLoginKeyPrefix+token, "invalid-json", time.Minute).Err())
		session, err := cache.ConsumeLoginSession(ctx, token)
		require.Error(t, err)
		require.Nil(t, session)
		require.Zero(t, rdb.Exists(ctx, totpLoginKeyPrefix+token).Val())
	})
	t.Run("redis_failure", func(t *testing.T) {
		options := *rdb.Options()
		closed := redis.NewClient(&options)
		require.NoError(t, closed.Close())
		session, err := (&TotpCache{rdb: closed}).ConsumeLoginSession(ctx, token)
		require.Error(t, err)
		require.Nil(t, session)
	})
}
