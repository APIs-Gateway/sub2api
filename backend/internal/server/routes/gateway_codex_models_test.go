package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayRoutesCodexModelsManifestPathIsRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	registered := make(map[string]string)
	for _, route := range router.Routes() {
		if route.Method == http.MethodGet {
			registered[route.Path] = route.Handler
		}
	}

	require.NotEmpty(t, registered["/backend-api/codex/models"], "GET /backend-api/codex/models should be registered")
	require.NotEmpty(t, registered["/v1/models"], "GET /v1/models should be registered")
	require.NotEmpty(t, registered["/models"], "GET /models should be registered")
	require.Equal(t, registered["/v1/models"], registered["/models"], "root alias should use the same platform-aware handler")
}

type codexModelsRouteAccountRepoStub struct {
	service.AccountRepository
}

func (s *codexModelsRouteAccountRepoStub) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]service.Account, error) {
	return nil, nil
}

func TestGatewayRoutesV1ModelsWithClientVersionUsesCodexManifestHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(22)
	apiKey := &service.APIKey{
		UserID:  7,
		User:    &service.User{ID: 7, Status: service.StatusActive},
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	}
	gatewaySvc := service.NewOpenAIGatewayService(
		&codexModelsRouteAccountRepoStub{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	router := gin.New()
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway: handler.NewGatewayHandler(
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				&config.Config{},
				nil,
				nil,
			),
			OpenAIGateway: handler.NewOpenAIGatewayHandler(
				gatewaySvc,
				nil, nil, nil, nil, nil, nil, nil,
				&config.Config{},
			),
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
			c.Next()
		}),
		nil,
		nil,
		nil,
		nil,
		&config.Config{},
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.137.0", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "No available OpenAI accounts")
}
