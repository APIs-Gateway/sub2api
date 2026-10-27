package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestApplyOAuthCredentialsPreservesExistingNonAuthCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newStubAdminService()
	stub.getAccountResult = &service.Account{
		ID:       1,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh-token",
			"model_mapping": map[string]any{"gpt-5": "gpt-5"},
			"account_id":    "existing-account-id",
		},
	}
	handler := NewAccountHandler(stub, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/accounts/1/apply-oauth-credentials", bytes.NewBufferString(
		`{"type":"oauth","credentials":{"access_token":"new-token","refresh_token":"new-refresh-token"}}`,
	))
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, stub.updateAccountCalls)
	require.NotNil(t, stub.lastUpdateAccountInput)
	require.Equal(t, map[string]any{
		"access_token":  "new-token",
		"refresh_token": "new-refresh-token",
		"model_mapping": map[string]any{"gpt-5": "gpt-5"},
		"account_id":    "existing-account-id",
	}, stub.lastUpdateAccountInput.Credentials)
}

func TestApplyOAuthCredentialsRejectsNonOAuthAccountWithoutUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newStubAdminService()
	stub.getAccountResult = &service.Account{
		ID:          2,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-existing"},
	}
	handler := NewAccountHandler(stub, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/accounts/2/apply-oauth-credentials", bytes.NewBufferString(
		`{"type":"oauth","credentials":{"access_token":"new-token"}}`,
	))
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, stub.updateAccountCalls)
}
