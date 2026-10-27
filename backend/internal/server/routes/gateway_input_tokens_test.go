package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newInputTokensRoutingFixture(platform string) *gin.Engine {
	r := gin.New()
	groupID := int64(1)
	cfg := &config.Config{}
	cfg.Gateway.MaxBodySize = 1024
	RegisterGatewayRoutes(r, &handler.Handlers{Gateway: &handler.GatewayHandler{}, OpenAIGateway: handler.NewOpenAIGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, cfg)},
		middleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 7, User: &service.User{ID: 7}, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: platform}})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Header("X-Test-Inbound-Endpoint", handler.GetInboundEndpoint(c))
			c.Next()
		}), nil, nil, nil, nil, cfg)
	return r
}

// These requests execute actual wildcard route dispatch and authentication.
// Generation's dependency error is intentionally distinct from the preflight
// body's validation error; registration alone would miss the original bug.
func TestGatewayResponsesInputTokensAliasesDispatchPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok, service.PlatformAnthropic} {
		for _, path := range []string{"/v1/responses/input_tokens", "/responses/input_tokens", "/backend-api/codex/responses/input_tokens"} {
			t.Run(platform+path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				newInputTokensRoutingFixture(platform).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`[]`)))
				want := http.StatusBadRequest
				if platform == service.PlatformAnthropic {
					want = http.StatusNotFound
				}
				require.Equal(t, want, rec.Code)
				require.Equal(t, "/v1/responses/input_tokens", rec.Header().Get("X-Test-Inbound-Endpoint"))
			})
		}
	}
}

func TestGatewayResponsesInputTokensDoesNotCaptureOtherSuffixes(t *testing.T) {
	for _, path := range []string{"/v1/responses/input_tokens/foo", "/responses/input_tokensx", "/backend-api/codex/responses/compact", "/v1/responses/unknown", "/responses/input_tokens//"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newInputTokensRoutingFixture(service.PlatformOpenAI).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`[]`)))
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, "must retain generation/compact dependency handling")
			require.NotEqual(t, "/v1/responses/input_tokens", rec.Header().Get("X-Test-Inbound-Endpoint"))
		})
	}
}
