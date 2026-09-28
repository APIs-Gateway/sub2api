//go:build unit

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newProductionPaymentRateLimitRouter(t *testing.T, cfg *config.Config, trustedProxies []string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(trustedProxies))
	router.Use(servermiddleware.SessionBindingContext(cfg))
	cfg.Pricing.DataDir = t.TempDir()
	noop := func(c *gin.Context) { c.Next() }
	registerRoutes(router, &handler.Handlers{
		Payment:        &handler.PaymentHandler{},
		PaymentWebhook: &handler.PaymentWebhookHandler{},
		Admin:          &handler.AdminHandlers{Payment: &adminhandler.PaymentHandler{}},
	}, noop, noop, noop, nil, nil, nil, nil, cfg, rdb)
	return router
}

func postProductionPublicOrderVerify(router *gin.Engine, remoteAddr, forwardedIP string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payment/public/orders/verify", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if forwardedIP != "" {
		req.Header.Set("X-Forwarded-For", forwardedIP)
	}
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// Exercise the production router wiring, not just RegisterPaymentRoutes:
// dropping redisClient from that call would silently disable the protection.
func TestRegisterRoutesWiresPublicOrderVerifyLimiter(t *testing.T) {
	router := newProductionPaymentRateLimitRouter(t, &config.Config{}, nil)
	for i := 1; i <= 21; i++ {
		w := postProductionPublicOrderVerify(router, "198.51.100.80:1234", "")
		if i <= 20 {
			require.Equal(t, http.StatusBadRequest, w.Code, "request %d should reach the handler", i)
		} else {
			require.Equal(t, http.StatusTooManyRequests, w.Code)
		}
	}
}

// With default trusted_proxies unset, a local reverse proxy is the only peer
// Gin can verify. Do not put all paying customers in one 20/minute bucket.
func TestPublicOrderVerifySkipsSharedUntrustedReverseProxy(t *testing.T) {
	router := newProductionPaymentRateLimitRouter(t, &config.Config{}, nil)
	for i := 1; i <= 25; i++ {
		w := postProductionPublicOrderVerify(router, "127.0.0.1:4321", "198.51.100.81")
		require.Equal(t, http.StatusBadRequest, w.Code, "proxy-shared request %d should reach handler", i)
	}
}

// The legacy forwarded-IP setting may be used for other compatibility paths,
// but a direct client must not rotate X-Forwarded-For to escape this limiter.
func TestPublicOrderVerifyIgnoresSpoofedLegacyForwardedIP(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetForwardedClientIPSettings(true, nil)
	router := newProductionPaymentRateLimitRouter(t, cfg, nil)
	for i := 1; i <= 21; i++ {
		forwardedIP := "198.51.100.81"
		if i == 21 {
			forwardedIP = "198.51.100.82"
		}
		w := postProductionPublicOrderVerify(router, "198.51.100.80:1234", forwardedIP)
		if i <= 20 {
			require.Equal(t, http.StatusBadRequest, w.Code, "request %d should reach handler", i)
		} else {
			require.Equal(t, http.StatusTooManyRequests, w.Code)
		}
	}
}

// Once the proxy is configured as trusted, Gin can securely resolve each
// public client IP and the normal throttle remains effective behind it.
func TestPublicOrderVerifyLimitsThroughTrustedReverseProxy(t *testing.T) {
	router := newProductionPaymentRateLimitRouter(t, &config.Config{}, []string{"10.0.0.0/8"})
	for i := 1; i <= 21; i++ {
		w := postProductionPublicOrderVerify(router, "10.0.0.5:4321", "198.51.100.83")
		if i <= 20 {
			require.Equal(t, http.StatusBadRequest, w.Code, "request %d should reach handler", i)
		} else {
			require.Equal(t, http.StatusTooManyRequests, w.Code)
		}
	}
}
