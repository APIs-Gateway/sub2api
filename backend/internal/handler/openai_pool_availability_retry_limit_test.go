package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 池账号可用性错误的超时类：同号重试上限取 SameAccountRetryLimit 与 pool_mode_retry_count 的较小值。
func TestPoolModeSameAccountRetry_TimeoutLimitCapsRetries(t *testing.T) {
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true}}
	counts := map[int64]int{}
	ferr := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway, RetryableOnSameAccount: true, SameAccountRetryLimit: 1}

	retryCount, retryLimit, ok := poolModeSameAccountRetry(account, ferr, counts)
	require.True(t, ok)
	require.Equal(t, 1, retryCount)
	require.Equal(t, 1, retryLimit)

	_, retryLimit, ok = poolModeSameAccountRetry(account, ferr, counts)
	require.False(t, ok, "超时类只重试 1 次，之后切号")
	require.Equal(t, 1, retryLimit)

	// 账号自己的上限更小时以账号为准；未设上限时沿用 pool_mode_retry_count（默认 3）。
	small := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true, "pool_mode_retry_count": 0}}
	_, _, ok = poolModeSameAccountRetry(small, ferr, map[int64]int{})
	require.False(t, ok)

	plain := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway, RetryableOnSameAccount: true}
	other := map[int64]int{}
	for i := 0; i < 3; i++ {
		_, _, ok = poolModeSameAccountRetry(account, plain, other)
		require.True(t, ok)
	}
	_, _, ok = poolModeSameAccountRetry(account, plain, other)
	require.False(t, ok)
}
