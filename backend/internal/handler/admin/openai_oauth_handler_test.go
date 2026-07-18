package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexPATCreateResponse struct {
	Code int `json:"code"`
}

func setupCodexPATCreateRouter(t *testing.T) (*gin.Engine, *stubAdminService, *OpenAIOAuthHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminService := newStubAdminService()
	oauthService := service.NewOpenAIOAuthService(nil, nil)
	t.Cleanup(oauthService.Stop)
	handler := NewOpenAIOAuthHandler(oauthService, adminService, nil)
	router.POST("/api/v1/admin/openai/create-from-codex-pat", handler.CreateAccountFromCodexPAT)
	return router, adminService, handler
}

func TestCreateAccountFromCodexPATCreatesProtectedCredentialsAndDefaults(t *testing.T) {
	router, adminService, handler := setupCodexPATCreateRouter(t)
	var gotProxyURL string
	handler.validateCodexPAT = func(_ context.Context, accessToken, proxyURL string) (*service.OpenAITokenInfo, error) {
		require.Equal(t, "at-test-token", accessToken)
		gotProxyURL = proxyURL
		return &service.OpenAITokenInfo{
			AccessToken:           accessToken,
			AuthMode:              service.OpenAIAuthModePersonalAccessToken,
			Email:                 "user@example.com",
			ChatGPTUserID:         "user-123",
			ChatGPTAccountID:      "acct-123",
			ChatGPTAccountFedRAMP: true,
			PlanType:              "plus",
		}, nil
	}

	body := `{
		"access_token":"at-test-token",
		"group_ids":[2,3],
		"proxy_id":4,
		"credential_extras":{"auth_mode":"oauth","access_token":"wrong","model_mapping":{"gpt-5":"gpt-5-codex"}},
		"extra":{"import_source":"user-supplied"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/create-from-codex-pat", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var response codexPATCreateResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, 0, response.Code)
	require.Equal(t, "http://127.0.0.1:8080", gotProxyURL)
	require.Len(t, adminService.createdAccounts, 1)

	input := adminService.createdAccounts[0]
	require.Equal(t, "user@example.com", input.Name)
	require.Equal(t, service.PlatformOpenAI, input.Platform)
	require.Equal(t, service.AccountTypeOAuth, input.Type)
	require.Equal(t, 3, input.Concurrency)
	require.Equal(t, 50, input.Priority)
	require.Equal(t, []int64{2, 3}, input.GroupIDs)
	require.Equal(t, "at-test-token", input.Credentials["access_token"])
	require.Equal(t, service.OpenAIAuthModePersonalAccessToken, input.Credentials["auth_mode"])
	require.NotEqual(t, "oauth", input.Credentials["auth_mode"])
	require.Equal(t, map[string]any{"gpt-5": "gpt-5-codex"}, input.Credentials["model_mapping"])
	require.Equal(t, "codex_personal_access_token", input.Extra["import_source"])
	require.Equal(t, "codex_personal_access_token", input.Extra["auth_provider"])
	require.Equal(t, fingerprintForTest("at-test-token"), input.Extra["access_token_sha256"])
}

func TestCreateAccountFromCodexPATRejectsInvalidRequestBeforeValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing access token", body: `{}`},
		{name: "negative concurrency", body: `{"access_token":"at-test-token","concurrency":-1}`},
		{name: "negative priority", body: `{"access_token":"at-test-token","priority":-1}`},
		{name: "negative rate multiplier", body: `{"access_token":"at-test-token","rate_multiplier":-1}`},
		{name: "oversized load factor", body: `{"access_token":"at-test-token","load_factor":10001}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, adminService, handler := setupCodexPATCreateRouter(t)
			called := false
			handler.validateCodexPAT = func(context.Context, string, string) (*service.OpenAITokenInfo, error) {
				called = true
				return nil, errors.New("validator should not be called")
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/create-from-codex-pat", strings.NewReader(tt.body))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.False(t, called)
			require.Empty(t, adminService.createdAccounts)
		})
	}
}

func TestCreateAccountFromCodexPATMapsValidationError(t *testing.T) {
	router, adminService, handler := setupCodexPATCreateRouter(t)
	handler.validateCodexPAT = func(context.Context, string, string) (*service.OpenAITokenInfo, error) {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_PAT_INVALID", "invalid PAT")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/create-from-codex-pat", strings.NewReader(`{"access_token":"at-test-token"}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, adminService.createdAccounts)
	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, "invalid PAT", response["message"])
}

func fingerprintForTest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
