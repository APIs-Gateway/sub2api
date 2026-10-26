package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheReasoningContent_UnavailableCache(t *testing.T) {
	ctx := context.Background()
	for _, c := range []*gatewayCache{nil, {}} {
		require.Error(t, c.SetReasoningContent(ctx, "item", "text", time.Minute))
		_, err := c.GetReasoningContent(ctx, "item")
		require.Error(t, err)
		require.NotErrorIs(t, err, service.ErrReasoningContentNotFound)
	}
}

func TestGatewayCacheReasoningContent_EdgeCases(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client)
	ctx := context.Background()

	// 空白 itemID：Set 是 no-op，Get 直接视为未命中，不访问 Redis。
	require.NoError(t, cache.SetReasoningContent(ctx, "   ", "x", time.Minute))
	_, err := cache.GetReasoningContent(ctx, "  ")
	require.ErrorIs(t, err, service.ErrReasoningContentNotFound)

	// itemID 两侧空白被裁掉，读写落在同一个 key。
	require.NoError(t, cache.SetReasoningContent(ctx, " item_trim ", "trimmed", time.Minute))
	got, err := cache.GetReasoningContent(ctx, "item_trim")
	require.NoError(t, err)
	require.Equal(t, "trimmed", got)
	require.True(t, mr.Exists(reasoningContentPrefix+"item_trim"))

	// 负 TTL 同样兜底为默认 7 天。
	require.NoError(t, cache.SetReasoningContent(ctx, "item_neg_ttl", "x", -time.Second))
	require.Greater(t, mr.TTL(reasoningContentPrefix+"item_neg_ttl"), 6*24*time.Hour)

}

// 真实读取失败（Redis 不可达）返回原始错误，而不是未命中哨兵。
func TestGatewayCacheReasoningContent_RedisErrorIsNotMiss(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 200 * time.Millisecond,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client)

	_, err := cache.GetReasoningContent(context.Background(), "item")
	require.Error(t, err)
	require.NotErrorIs(t, err, service.ErrReasoningContentNotFound)
}
