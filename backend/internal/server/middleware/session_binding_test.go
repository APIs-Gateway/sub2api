//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSessionBindingContext_UsesTrustedProxyChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies([]string{"10.0.0.0/8"}))
	router.Use(SessionBindingContext())
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
	router.Use(SessionBindingContext())
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
