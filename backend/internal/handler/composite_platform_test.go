//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompositeTargetPlatformHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	ensureCompositeTargetPlatform(context, apiKey, "grok-4")
	platform, ok := service.ResolvedTargetPlatformFromContext(context.Request.Context())
	require.True(t, ok)
	require.Equal(t, service.PlatformGrok, platform)
	require.True(t, compositeTargetPlatformAllowed(context, apiKey, "grok-4", service.PlatformOpenAI, service.PlatformGrok))
	require.False(t, compositeTargetPlatformAllowed(context, apiKey, "grok-4", service.PlatformAnthropic))
	require.Equal(t, service.PlatformGrok, effectiveAPIKeyPlatform(context, apiKey))
}

func TestCompositeTargetPlatformHelpersFailClosedForUnknownModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	require.False(t, compositeTargetPlatformAllowed(context, apiKey, "llama-4", service.PlatformAnthropic))
	require.Equal(t, service.PlatformComposite, effectiveAPIKeyPlatform(context, apiKey))
}
