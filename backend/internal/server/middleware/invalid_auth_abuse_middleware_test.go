//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestAPIKeyAuthMarksQueryAndMalformedCredentialRejects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		google     bool
		path       string
		headerName string
		header     string
		wantStatus int
		wantReason IngressRejectReason
	}{
		{name: "main query", path: "/t?key=bad", wantStatus: http.StatusBadRequest, wantReason: IngressRejectQueryAPIKeyDeprecated},
		{name: "main missing", path: "/t", wantStatus: http.StatusUnauthorized, wantReason: IngressRejectAPIKeyRequired},
		{name: "main malformed", path: "/t", headerName: "Authorization", header: "Basic abc", wantStatus: http.StatusUnauthorized, wantReason: IngressRejectInvalidAPIKey},
		{name: "google query", google: true, path: "/v1beta/test?api_key=bad", wantStatus: http.StatusBadRequest, wantReason: IngressRejectQueryAPIKeyDeprecated},
		{name: "google missing", google: true, path: "/v1beta/test", wantStatus: http.StatusUnauthorized, wantReason: IngressRejectAPIKeyRequired},
		{name: "google malformed", google: true, path: "/v1beta/test", headerName: "Authorization", header: "Basic abc", wantStatus: http.StatusUnauthorized, wantReason: IngressRejectInvalidAPIKey},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := invalidAuthAbuseTestConfig()
			apiKeyService := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			r := gin.New()
			var reason IngressRejectReason
			r.Use(func(c *gin.Context) {
				c.Next()
				reason, _ = GetIngressRejectReason(c)
			})
			if tc.google {
				r.Use(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, cfg))
			} else {
				r.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
			}
			r.GET("/t", func(c *gin.Context) { c.Status(http.StatusOK) })
			r.GET("/v1beta/test", func(c *gin.Context) { c.Status(http.StatusOK) })

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.headerName != "" {
				req.Header.Set(tc.headerName, tc.header)
			}
			req.RemoteAddr = "198.51.100.11:1234"
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantReason, reason)
		})
	}
}

func TestAPIKeyCredentialHeaderGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/t", nil)
		return c
	}

	require.False(t, apiKeyHeadersTooLarge(nil))
	require.False(t, hasAPIKeyCredentialInput(nil))

	c := newContext()
	c.Request.Header.Set("Authorization", strings.Repeat("x", maxAPIKeyAuthorizationHeaderBytes+1))
	require.True(t, apiKeyHeadersTooLarge(c))

	c = newContext()
	c.Request.Header.Set("x-api-key", strings.Repeat("x", service.MaxAPIKeyCredentialBytes+1))
	require.True(t, apiKeyHeadersTooLarge(c))

	c = newContext()
	c.Request.Header.Set("x-goog-api-key", strings.Repeat("x", service.MaxAPIKeyCredentialBytes+1))
	require.True(t, apiKeyHeadersTooLarge(c))

	for _, header := range []string{"Authorization", "x-api-key", "x-goog-api-key"} {
		c = newContext()
		c.Request.Header.Set(header, "provided")
		require.True(t, hasAPIKeyCredentialInput(c), header)
	}
}

func TestIngressRejectHelpersNormalizeClientIPAndRecordFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/t", nil)
	c.Request.RemoteAddr = "[2001:db8:abcd:1234::1]:443"
	require.Equal(t, "2001:db8:abcd:1234::", invalidAuthClientKey(c))
	c.Request.RemoteAddr = "not-an-ip"
	require.Equal(t, "0.0.0.0", invalidAuthClientKey(c))

	probe := &invalidAuthProbe{}
	require.False(t, rejectInvalidAuthAbuse(c, nil))
	require.False(t, rejectInvalidAuthAbuse(c, probe))
	probe.blocked = true
	require.True(t, rejectInvalidAuthAbuse(c, probe))
	require.Equal(t, "1", c.Writer.Header().Get("Retry-After"))
	recordInvalidAuthFailure(c, probe)
	require.Equal(t, 1, probe.records)
	recordInvalidAuthFailure(c, nil)
}

type invalidAuthProbe struct {
	blocked bool
	records int
}

func (p *invalidAuthProbe) CheckInvalidAuthAbuse(string) (time.Duration, bool) {
	return 0, p.blocked
}

func (p *invalidAuthProbe) RecordInvalidAuthFailure(string) {
	p.records++
}
