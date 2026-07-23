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

type compositeFailingReadCloser struct{}

func (compositeFailingReadCloser) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (compositeFailingReadCloser) Close() error             { return nil }

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

func TestCompositeTargetPlatformMiddlewareSkipsUnsupportedRequests(t *testing.T) {
	tests := []struct {
		name   string
		apiKey *service.APIKey
		method string
		body   string
	}{
		{name: "no api key", method: http.MethodPost, body: `{"model":"grok-4"}`},
		{name: "non composite", apiKey: &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}}, method: http.MethodPost, body: `{"model":"grok-4"}`},
		{name: "get", apiKey: &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}}, method: http.MethodGet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(tt.method, "/v1/models", strings.NewReader(tt.body))
			if tt.apiKey != nil {
				context.Set(string(middleware.ContextKeyAPIKey), tt.apiKey)
			}
			require.NotPanics(t, func() { compositeTargetPlatformMiddleware()(context) })
			_, ok := service.ResolvedTargetPlatformFromContext(context.Request.Context())
			require.False(t, ok)
		})
	}

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NotPanics(t, func() { compositeTargetPlatformMiddleware()(context) })
}

func TestCompositeTargetPlatformMiddlewareRestoresUnknownModelAndHandlesReadErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	unknownBody := `{"model":"llama-4"}`
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(unknownBody))
	context.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}})
	compositeTargetPlatformMiddleware()(context)
	body, err := io.ReadAll(context.Request.Body)
	require.NoError(t, err)
	require.Equal(t, unknownBody, string(body))
	_, ok := service.ResolvedTargetPlatformFromContext(context.Request.Context())
	require.False(t, ok)

	readError, _ := gin.CreateTestContext(httptest.NewRecorder())
	readError.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	readError.Request.Body = compositeFailingReadCloser{}
	readError.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}})
	compositeTargetPlatformMiddleware()(readError)
	require.True(t, readError.IsAborted())
	require.Equal(t, http.StatusBadRequest, readError.Writer.Status())
}

func TestCompositeTargetPlatformMiddlewareHandlesMaxBodySize(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-4"}`))
	context.Request.Body = http.MaxBytesReader(recorder, context.Request.Body, 1)
	context.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}})

	compositeTargetPlatformMiddleware()(context)
	require.True(t, context.IsAborted())
	require.Equal(t, http.StatusRequestEntityTooLarge, context.Writer.Status())
}

func TestCompositeImplicitTargetPlatformMiddlewareSkipsNonCompositeRequests(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	context.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}})
	compositeImplicitTargetPlatformMiddleware(service.PlatformGemini)(context)
	_, ok := service.ResolvedTargetPlatformFromContext(context.Request.Context())
	require.False(t, ok)

	noRequest, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NotPanics(t, func() { compositeImplicitTargetPlatformMiddleware(service.PlatformGemini)(noRequest) })
	require.NotPanics(t, func() { resetCompositeRequestBody(nil, nil) })
}
