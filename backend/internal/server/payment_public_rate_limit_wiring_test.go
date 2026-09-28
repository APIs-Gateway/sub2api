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
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Exercise the production router wiring, not just RegisterPaymentRoutes:
// dropping redisClient from that call would silently disable the protection.
func TestRegisterRoutesWiresPublicOrderVerifyLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	cfg := &config.Config{}
	cfg.Pricing.DataDir = t.TempDir()
	noop := func(c *gin.Context) { c.Next() }
	registerRoutes(router, &handler.Handlers{
		Payment:        &handler.PaymentHandler{},
		PaymentWebhook: &handler.PaymentWebhookHandler{},
		Admin:          &handler.AdminHandlers{Payment: &adminhandler.PaymentHandler{}},
	}, noop, noop, noop, nil, nil, nil, nil, cfg, rdb)

	for i := 1; i <= 21; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/payment/public/orders/verify", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "198.51.100.80:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if i <= 20 {
			require.Equal(t, http.StatusBadRequest, w.Code, "request %d should reach the handler", i)
		} else {
			require.Equal(t, http.StatusTooManyRequests, w.Code)
		}
	}
}
