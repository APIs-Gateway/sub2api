//go:build unit

package handler

import (
	"context"
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

func TestCompositeTargetPlatformHelpersFallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)

	require.NotPanics(t, func() {
		ensureCompositeTargetPlatform(nil, nil, "grok-4")
	})
	require.True(t, compositeTargetPlatformAllowed(nil, nil, "grok-4", service.PlatformGrok))
	require.Empty(t, effectiveAPIKeyPlatform(nil, nil))
	require.Equal(t, service.PlatformOpenAI, openAICompatibleRequestPlatform(context.Background(), nil))

	noRequest, _ := gin.CreateTestContext(httptest.NewRecorder())
	compositeKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}}
	require.True(t, compositeTargetPlatformAllowed(noRequest, compositeKey, "grok-4", service.PlatformGrok))
	require.Equal(t, service.PlatformComposite, effectiveAPIKeyPlatform(noRequest, compositeKey))

	nonCompositeKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	require.True(t, compositeTargetPlatformAllowed(ctx, nonCompositeKey, "grok-4", service.PlatformAnthropic))
	require.Equal(t, service.PlatformOpenAI, effectiveAPIKeyPlatform(ctx, nonCompositeKey))

	resolvedOpenAI := service.WithResolvedTargetPlatform(ctx.Request.Context(), service.PlatformOpenAI)
	ctx.Request = ctx.Request.WithContext(resolvedOpenAI)
	ensureCompositeTargetPlatform(ctx, compositeKey, "grok-4")
	require.Equal(t, service.PlatformOpenAI, effectiveAPIKeyPlatform(ctx, compositeKey))

	require.Equal(t, service.PlatformGrok, openAICompatibleRequestPlatform(service.WithResolvedTargetPlatform(context.Background(), service.PlatformGrok), nil))
	require.Equal(t, service.PlatformOpenAI, openAICompatibleRequestPlatform(service.WithResolvedTargetPlatform(context.Background(), service.PlatformOpenAI), nil))
	require.Equal(t, service.PlatformGrok, openAICompatibleRequestPlatform(service.WithResolvedTargetPlatform(context.Background(), service.PlatformAnthropic), &service.APIKey{Group: &service.Group{Platform: service.PlatformGrok}}))
}
