//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSessionBindingContext_UsesTrustedProxyChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies([]string{"10.0.0.0/8"}))
	router.Use(SessionBindingContext(&config.Config{}))
	router.GET("/binding", func(c *gin.Context) {
		binding := service.SessionBindingFromContext(c.Request.Context())
		c.JSON(http.StatusOK, gin.H{"ip": binding.IP, "user_agent": binding.UserAgent})
	})

	req := httptest.NewRequest(http.MethodGet, "/binding", nil)
	req.RemoteAddr = "10.0.0.8:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("User-Agent", "session-test/1.0")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"ip":"203.0.113.7","user_agent":"session-test/1.0"}`, w.Body.String())
}

func TestSessionBindingContext_DoesNotTrustForwardedIPFromUntrustedPeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies([]string{"10.0.0.0/8"}))
	router.Use(SessionBindingContext(&config.Config{}))
	router.GET("/binding", func(c *gin.Context) {
		binding := service.SessionBindingFromContext(c.Request.Context())
		c.String(http.StatusOK, binding.IP)
	})

	req := httptest.NewRequest(http.MethodGet, "/binding", nil)
	req.RemoteAddr = "198.51.100.8:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "198.51.100.8", w.Body.String())
}

func TestSessionBindingContextHonorsTrustForwardedToggle(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name           string
		trustForwarded bool
		wantIP         string
	}{
		{name: "trust disabled records proxy address", trustForwarded: false, wantIP: "127.0.0.1"},
		{name: "trust enabled records forwarded client IP", trustForwarded: true, wantIP: "1.2.3.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tc.trustForwarded {
				cfg.SetForwardedClientIPSettings(true, []string{"X-Cdn-Client-IP"})
			} else {
				cfg.SetTrustForwardedIPForAPIKeyACL(false)
			}

			router := gin.New()
			require.NoError(t, router.SetTrustedProxies(nil))
			router.Use(SessionBindingContext(cfg))
			router.GET("/binding", func(c *gin.Context) {
				binding := service.SessionBindingFromContext(c.Request.Context())
				require.NotNil(t, binding)
				require.Equal(t, tc.wantIP, binding.IP)
				require.Equal(t, "test-agent", binding.UserAgent)
				require.Equal(t, tc.wantIP, SecurityClientIP(c))
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/binding", nil)
			req.RemoteAddr = "127.0.0.1:54321"
			if tc.trustForwarded {
				req.Header.Set("X-Cdn-Client-IP", "1.2.3.4")
			} else {
				req.Header.Set("X-Real-IP", "1.2.3.4")
			}
			req.Header.Set("User-Agent", "test-agent")
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestSecurityClientIPFallsBackWithoutInjectedBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/ip", func(c *gin.Context) {
		c.String(http.StatusOK, SecurityClientIP(c))
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "9.9.9.9:12345"
	req.Header.Set("X-Real-IP", "1.2.3.4")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "9.9.9.9", w.Body.String())
}
