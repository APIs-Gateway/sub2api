package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newPaymentRoutesRateLimitTestRouter(t *testing.T, redisClient *redis.Client) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	v1 := router.Group("/api/v1")
	noop := func(c *gin.Context) { c.Next() }
	RegisterPaymentRoutes(
		v1,
		&handler.PaymentHandler{},
		&handler.PaymentWebhookHandler{},
		&admin.PaymentHandler{},
		servermiddleware.JWTAuthMiddleware(noop),
		servermiddleware.AdminAuthMiddleware(noop),
		nil,
		nil,
		redisClient,
	)
	return router
}

func postPaymentRateLimitTestRoute(router *gin.Engine, path, remoteAddr, forwardedIP string) *httptest.ResponseRecorder {
	// Empty JSON fails binding before any payment service is touched. A 400
	// proves that the request reached the handler rather than the limiter.
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if forwardedIP != "" {
		req.Header.Set("X-Forwarded-For", forwardedIP)
	}
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestPublicOrderVerifyRateLimitedPerTrustedIPOnly(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := newPaymentRoutesRateLimitTestRouter(t, rdb)
	const verifyPath = "/api/v1/payment/public/orders/verify"

	for i := 1; i <= publicOrderVerifyRateLimit; i++ {
		w := postPaymentRateLimitTestRoute(router, verifyPath, "198.51.100.20:1234", "203.0.113.55")
		require.Equal(t, http.StatusBadRequest, w.Code, "request %d should reach the handler", i)
	}

	// Untrusted forwarded headers cannot reset the same remote IP's budget.
	w := postPaymentRateLimitTestRoute(router, verifyPath, "198.51.100.20:1234", "203.0.113.56")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Contains(t, w.Body.String(), "rate limit exceeded")
	require.NotEmpty(t, w.Header().Get("Retry-After"))

	// Another remote IP has a separate budget. The signed resume-token and
	// authenticated verify routes keep their previous behavior at the capped IP.
	w = postPaymentRateLimitTestRoute(router, verifyPath, "198.51.100.21:1234", "")
	require.Equal(t, http.StatusBadRequest, w.Code)
	w = postPaymentRateLimitTestRoute(router, "/api/v1/payment/public/orders/resolve", "198.51.100.20:1234", "")
	require.Equal(t, http.StatusBadRequest, w.Code)
	w = postPaymentRateLimitTestRoute(router, "/api/v1/payment/orders/verify", "198.51.100.20:1234", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)

	mr.FastForward(publicOrderVerifyRateLimitWindow)
	w = postPaymentRateLimitTestRoute(router, verifyPath, "198.51.100.20:1234", "")
	require.Equal(t, http.StatusBadRequest, w.Code, "expired window should restore the anonymous lookup")
}

func TestPublicOrderVerifyRateLimitFailsOpenWithoutRedis(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = rdb.Close() })
	router := newPaymentRoutesRateLimitTestRouter(t, rdb)
	w := postPaymentRateLimitTestRoute(router, "/api/v1/payment/public/orders/verify", "203.0.113.30:1234", "")
	require.Equal(t, http.StatusBadRequest, w.Code, "Redis outage must not block payment result recovery")
}
