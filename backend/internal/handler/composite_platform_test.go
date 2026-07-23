//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
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

func TestCompositeHandlerPlatformValidation(t *testing.T) {
	groupID := int64(10)
	apiKey := &service.APIKey{
		ID:      100,
		UserID:  200,
		GroupID: &groupID,
		User:    &service.User{ID: 200},
		Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite, AllowMessagesDispatch: true},
	}
	subject := middleware2.AuthSubject{UserID: 200, Concurrency: 1}
	openAIHandler := newOpenAIHandlerWithParseDependencies()

	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		run        func(*gin.Context)
	}{
		{name: "gateway messages", path: "/v1/messages", body: `{"model":"grok-4","messages":[]}`, wantStatus: http.StatusBadRequest, run: (&GatewayHandler{cfg: &config.Config{}}).Messages},
		{name: "gateway count tokens", path: "/v1/messages/count_tokens", body: `{"model":"grok-4","messages":[]}`, wantStatus: http.StatusBadRequest, run: (&GatewayHandler{cfg: &config.Config{}}).CountTokens},
		{name: "gateway chat completions", path: "/v1/chat/completions", body: `{"model":"grok-4","messages":[]}`, wantStatus: http.StatusBadRequest, run: (&GatewayHandler{cfg: &config.Config{}}).ChatCompletions},
		{name: "gateway responses", path: "/v1/responses", body: `{"model":"grok-4","input":[]}`, wantStatus: http.StatusBadRequest, run: (&GatewayHandler{cfg: &config.Config{}}).Responses},
		{name: "openai chat completions", path: "/openai/v1/chat/completions", body: `{"model":"claude-sonnet-4-5","messages":[]}`, wantStatus: http.StatusBadRequest, run: openAIHandler.ChatCompletions},
		{name: "openai responses", path: "/openai/v1/responses", body: `{"model":"claude-sonnet-4-5","input":[]}`, wantStatus: http.StatusBadRequest, run: openAIHandler.Responses},
		{name: "openai messages", path: "/openai/v1/messages", body: `{"model":"claude-sonnet-4-5","max_tokens":1,"messages":[]}`, wantStatus: http.StatusBadRequest, run: openAIHandler.Messages},
		{name: "openai embeddings", path: "/openai/v1/embeddings", body: `{"model":"grok-4","input":"hello"}`, wantStatus: http.StatusNotFound, run: openAIHandler.Embeddings},
		{name: "openai images", path: "/openai/v1/images/generations", body: `{"model":"gpt-image-2","prompt":"hello","size":"1024x1024"}`, wantStatus: http.StatusNotFound, run: openAIHandler.Images},
		{name: "openai alpha search", path: "/openai/v1/alpha/search", body: `{"model":"grok-4"}`, wantStatus: http.StatusNotFound, run: openAIHandler.AlphaSearch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			if tt.name == "openai images" {
				ctx.Request = ctx.Request.WithContext(service.WithResolvedTargetPlatform(ctx.Request.Context(), service.PlatformGrok))
			}
			ctx.Set(string(middleware2.ContextKeyAPIKey), apiKey)
			ctx.Set(string(middleware2.ContextKeyUser), subject)

			tt.run(ctx)

			require.Equal(t, tt.wantStatus, recorder.Code)
		})
	}
}
