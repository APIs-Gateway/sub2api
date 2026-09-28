//go:build integration

package routes

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicOrderVerifyRateLimitWithRedis(t *testing.T) {
	rdb := startAuthRouteRedis(t, t.Context())
	router := newPaymentRoutesRateLimitTestRouter(t, rdb)
	const path = "/api/v1/payment/public/orders/verify"

	for i := 1; i <= publicOrderVerifyRateLimit; i++ {
		w := postPaymentRateLimitTestRoute(router, path, "198.51.100.60:1234", "")
		require.Equal(t, http.StatusBadRequest, w.Code, "request %d should reach the handler", i)
	}
	w := postPaymentRateLimitTestRoute(router, path, "198.51.100.60:1234", "")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
}
