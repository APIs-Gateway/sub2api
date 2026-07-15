package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
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

func TestOpenAIGatewayHandlerCodexModels_CanceledRequestDoesNotWriteResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(ctx)

	h := &OpenAIGatewayHandler{}
	h.CodexModels(c)

	require.False(t, c.Writer.Written())
}

func TestOpenAIGatewayHandlerCodexModels_FailsOverRetryableManifestError(t *testing.T) {
	h, groupID := newCodexModelsFailoverHandlerForTest(2, 3)
	calls := make([]int64, 0, 2)
	fetch := func(_ context.Context, account *service.Account, _, _ string) (*service.CodexModelsManifest, error) {
		calls = append(calls, account.ID)
		if account.ID == 1 {
			return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "temporary upstream failure")
		}
		return &service.CodexModelsManifest{
			Body: []byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`),
			ETag: `W/"manifest-2"`,
		}, nil
	}

	recorder := performCodexModelsRequestWithFetcher(t, h, groupID, fetch)

	require.Equal(t, []int64{1, 2}, calls)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, `W/"manifest-2"`, recorder.Header().Get("ETag"))
	require.JSONEq(t, `{"models":[{"slug":"gpt-5.6-sol"}]}`, recorder.Body.String())
}

func TestOpenAIGatewayHandlerCodexModels_DoesNotFailOverPermanentManifestError(t *testing.T) {
	h, groupID := newCodexModelsFailoverHandlerForTest(2, 3)
	calls := make([]int64, 0, 1)
	fetch := func(_ context.Context, account *service.Account, _, _ string) (*service.CodexModelsManifest, error) {
		calls = append(calls, account.ID)
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_TOKEN_MISSING", "permanent account error")
	}

	recorder := performCodexModelsRequestWithFetcher(t, h, groupID, fetch)

	require.Equal(t, []int64{1}, calls)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Contains(t, recorder.Body.String(), "permanent account error")
}

func TestOpenAIGatewayHandlerCodexModels_HonorsAccountSwitchLimit(t *testing.T) {
	h, groupID := newCodexModelsFailoverHandlerForTest(4, 2)
	calls := make([]int64, 0, 3)
	fetch := func(_ context.Context, account *service.Account, _, _ string) (*service.CodexModelsManifest, error) {
		calls = append(calls, account.ID)
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "temporary failure")
	}

	recorder := performCodexModelsRequestWithFetcher(t, h, groupID, fetch)

	require.Equal(t, []int64{1, 2, 3}, calls)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
}

func TestOpenAIGatewayHandlerCodexModels_ReturnsLastErrorWhenAccountsAreExhausted(t *testing.T) {
	h, groupID := newCodexModelsFailoverHandlerForTest(2, 3)
	calls := make([]int64, 0, 2)
	fetch := func(_ context.Context, account *service.Account, _, _ string) (*service.CodexModelsManifest, error) {
		calls = append(calls, account.ID)
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "temporary failure from account %d", account.ID)
	}

	recorder := performCodexModelsRequestWithFetcher(t, h, groupID, fetch)

	require.Equal(t, []int64{1, 2}, calls)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Contains(t, recorder.Body.String(), "temporary failure from account 2")
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

func newCodexModelsFailoverHandlerForTest(accountCount, maxSwitches int) (*OpenAIGatewayHandler, int64) {
	accounts := make([]service.Account, 0, accountCount)
	for i := 1; i <= accountCount; i++ {
		accounts = append(accounts, service.Account{
			ID:          int64(i),
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Priority:    i - 1,
			Concurrency: 1,
			Credentials: map[string]any{"access_token": "test-token"},
		})
	}
	h := newCodexModelsHandlerForTest(&codexModelsAccountRepoStub{accounts: accounts})
	h.maxAccountSwitches = maxSwitches
	return h, 42
}

func performCodexModelsRequestWithFetcher(t *testing.T, h *OpenAIGatewayHandler, groupID int64, fetch codexModelsManifestFetcher) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.144.0", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})

	h.codexModels(c, fetch)
	return recorder
}
