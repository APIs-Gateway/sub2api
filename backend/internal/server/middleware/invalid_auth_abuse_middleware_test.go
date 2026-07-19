//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func invalidAuthAbuseTestConfig() *config.Config {
	return &config.Config{
		RunMode: config.RunModeSimple,
		APIKeyAuth: config.APIKeyAuthCacheConfig{
			InvalidAbuse: config.InvalidAuthAbuseConfig{
				Enabled: true, Threshold: 2, WindowSeconds: 60, BlockSeconds: 10, Capacity: 16,
			},
		},
	}
}

func TestAPIKeyAuthInvalidAbuseLimiterBlocksClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := invalidAuthAbuseTestConfig()
	apiKeyService := service.NewAPIKeyService(&stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			return nil, service.ErrAPIKeyNotFound
		},
	}, nil, nil, nil, nil, nil, cfg)

	r := gin.New()
	var reason IngressRejectReason
	r.Use(func(c *gin.Context) {
		c.Next()
		reason, _ = GetIngressRejectReason(c)
	})
	r.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
	r.GET("/t", func(c *gin.Context) { c.Status(http.StatusOK) })

	for i, wantStatus := range []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.RemoteAddr = "198.51.100.10:1234"
		req.Header.Set("x-api-key", "invalid-key")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, wantStatus, rec.Code, "attempt %d", i+1)
		if i == 2 {
			require.Equal(t, "10", rec.Header().Get("Retry-After"))
			require.Equal(t, IngressRejectInvalidAuthRateLimited, reason)
		}
	}
	health := apiKeyService.InvalidAuthAbuseHealth()
	require.Equal(t, uint64(2), health.Recorded)
	require.Equal(t, uint64(1), health.Blocks)
	require.Equal(t, uint64(1), health.Rejected)
}

func TestAPIKeyAuthRejectsOversizedCredentialsBeforeRepositoryLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		path   string
		header string
	}{
		{name: "openai", path: "/t", header: "x-api-key"},
		{name: "google", path: "/v1beta/test", header: "x-goog-api-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := invalidAuthAbuseTestConfig()
			called := false
			apiKeyService := service.NewAPIKeyService(&stubApiKeyRepo{
				getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
					called = true
					return nil, service.ErrAPIKeyNotFound
				},
			}, nil, nil, nil, nil, nil, cfg)
			r := gin.New()
			var reason IngressRejectReason
			r.Use(func(c *gin.Context) {
				c.Next()
				reason, _ = GetIngressRejectReason(c)
			})
			if tc.name == "google" {
				r.Use(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, cfg))
			} else {
				r.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
			}
			r.GET(tc.path, func(c *gin.Context) { c.Status(http.StatusOK) })

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set(tc.header, strings.Repeat("x", service.MaxAPIKeyCredentialBytes+1))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			require.Equal(t, http.StatusUnauthorized, rec.Code)
			require.False(t, called)
			require.Equal(t, IngressRejectInvalidAPIKey, reason)
		})
	}
}
