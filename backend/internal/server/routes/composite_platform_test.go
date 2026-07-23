//go:build unit

package routes

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompositeTargetPlatformMiddlewareResolvesAndRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"grok-4","input":"hello"}`))
	context.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{Platform: service.PlatformComposite},
	})

	compositeTargetPlatformMiddleware()(context)

	platform, ok := service.ResolvedTargetPlatformFromContext(context.Request.Context())
	require.True(t, ok)
	require.Equal(t, service.PlatformGrok, platform)
	body, err := io.ReadAll(context.Request.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"grok-4","input":"hello"}`, string(body))
}

func TestCompositeImplicitTargetPlatformMiddlewareResolvesGemini(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	context.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{Platform: service.PlatformComposite},
	})

	compositeImplicitTargetPlatformMiddleware(service.PlatformGemini)(context)

	platform, ok := service.ResolvedTargetPlatformFromContext(context.Request.Context())
	require.True(t, ok)
	require.Equal(t, service.PlatformGemini, platform)
}
