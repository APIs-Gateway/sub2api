package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexModelsAccountRepoStub struct {
	service.AccountRepository

	accounts []service.Account
	err      error
}

func (s *codexModelsAccountRepoStub) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]service.Account, error) {
	if s.err != nil {
		return nil, s.err
	}
	accounts := make([]service.Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		if account.Platform == platform {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func newCodexModelsHandlerForTest(repo service.AccountRepository) *OpenAIGatewayHandler {
	return &OpenAIGatewayHandler{
		gatewayService: service.NewOpenAIGatewayService(
			repo,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, nil, nil, nil, nil,
		),
	}
}

func TestOpenAIGatewayHandlerCodexModels_RequiresAPIKeyGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newCodexModelsHandlerForTest(&codexModelsAccountRepoStub{})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)

	h.CodexModels(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "API key group is required")
}

func TestOpenAIGatewayHandlerCodexModels_RejectsNonOpenAIGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newCodexModelsHandlerForTest(&codexModelsAccountRepoStub{})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: 10, Platform: service.PlatformAnthropic},
	})

	h.CodexModels(c)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "only available for OpenAI groups")
}

func TestOpenAIGatewayHandlerCodexModels_ReturnsUnavailableWhenNoAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(11)
	h := newCodexModelsHandlerForTest(&codexModelsAccountRepoStub{})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})

	h.CodexModels(c)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "No available OpenAI accounts")
}

func TestOpenAIGatewayHandlerCodexModels_MapsManifestFetchError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(12)
	h := newCodexModelsHandlerForTest(&codexModelsAccountRepoStub{
		accounts: []service.Account{
			{
				ID:          123,
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeOAuth,
				Status:      service.StatusActive,
				Schedulable: true,
				Concurrency: 1,
			},
		},
	})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/models?client_version=0.137.0", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})

	h.CodexModels(c)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "account has no Codex backend access token")
}
