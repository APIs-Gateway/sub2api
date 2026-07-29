//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type grokOAuthHandlerClientStub struct{}

func (grokOAuthHandlerClientStub) ExchangeCode(context.Context, string, string, string, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		IDToken:      "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6Imdyb2tAZXhhbXBsZS5jb20ifQ.",
		ExpiresIn:    3600,
	}, nil
}

func (grokOAuthHandlerClientStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{
		AccessToken:  "refreshed-access-token",
		RefreshToken: "refreshed-refresh-token",
		ExpiresIn:    3600,
	}, nil
}

type grokHandlerAdminService struct {
	*stubAdminService
	account *service.Account
}

func (s *grokHandlerAdminService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if s.account == nil || s.account.ID != id {
		return nil, errors.New("account not found")
	}
	copy := *s.account
	return &copy, nil
}

func setupGrokOAuthHandlerRouter(h *GrokOAuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth-url", h.GenerateAuthURL)
	router.POST("/exchange", h.ExchangeCode)
	router.POST("/refresh", h.RefreshToken)
	router.POST("/accounts/:id/refresh", h.RefreshAccountToken)
	router.POST("/accounts", h.CreateAccountFromOAuth)
	return router
}

func grokHandlerRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestGrokOAuthHandlerGenerateExchangeAndRefresh(t *testing.T) {
	oauthService := service.NewGrokOAuthService(nil, grokOAuthHandlerClientStub{})
	defer oauthService.Stop()
	adminService := newStubAdminService()
	router := setupGrokOAuthHandlerRouter(NewGrokOAuthHandler(oauthService, adminService))

	generate := grokHandlerRequest(t, router, http.MethodPost, "/auth-url", map[string]any{"redirect_uri": "http://localhost/callback"})
	require.Equal(t, http.StatusOK, generate.Code)
	var generated struct {
		Data service.GrokAuthURLResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(generate.Body.Bytes(), &generated))
	require.NotEmpty(t, generated.Data.SessionID)

	exchange := grokHandlerRequest(t, router, http.MethodPost, "/exchange", map[string]any{
		"session_id": generated.Data.SessionID,
		"code":       "authorization-code",
		"state":      generated.Data.State,
	})
	require.Equal(t, http.StatusOK, exchange.Code)
	require.Contains(t, exchange.Body.String(), "access-token")

	refresh := grokHandlerRequest(t, router, http.MethodPost, "/refresh", map[string]any{
		"rt":        "refresh-token",
		"client_id": "client-id",
	})
	require.Equal(t, http.StatusOK, refresh.Code)
	require.Contains(t, refresh.Body.String(), "refreshed-access-token")
}

func TestGrokOAuthHandlerValidationErrors(t *testing.T) {
	oauthService := service.NewGrokOAuthService(nil, grokOAuthHandlerClientStub{})
	defer oauthService.Stop()
	router := setupGrokOAuthHandlerRouter(NewGrokOAuthHandler(oauthService, newStubAdminService()))

	require.Equal(t, http.StatusBadRequest,
		grokHandlerRequest(t, router, http.MethodPost, "/exchange", map[string]any{}).Code)
	require.Equal(t, http.StatusBadRequest,
		grokHandlerRequest(t, router, http.MethodPost, "/refresh", map[string]any{}).Code)
	req := httptest.NewRequest(http.MethodPost, "/refresh", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	req = httptest.NewRequest(http.MethodPost, "/auth-url", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "auth URL endpoint intentionally falls back to empty request")
}

func TestGrokOAuthHandlerRefreshAccountToken(t *testing.T) {
	oauthService := service.NewGrokOAuthService(nil, grokOAuthHandlerClientStub{})
	defer oauthService.Stop()
	adminService := &grokHandlerAdminService{
		stubAdminService: newStubAdminService(),
		account: &service.Account{
			ID:       42,
			Name:     "grok",
			Platform: service.PlatformGrok,
			Type:     service.AccountTypeOAuth,
			Credentials: map[string]any{
				"refresh_token": "refresh-token",
				"base_url":      "https://custom.example/v1",
				"email":         "grok@example.com",
			},
		},
	}
	router := setupGrokOAuthHandlerRouter(NewGrokOAuthHandler(oauthService, adminService))

	require.Equal(t, http.StatusBadRequest,
		grokHandlerRequest(t, router, http.MethodPost, "/accounts/not-an-id/refresh", map[string]any{}).Code)

	adminService.account.Platform = service.PlatformOpenAI
	require.Equal(t, http.StatusBadRequest,
		grokHandlerRequest(t, router, http.MethodPost, "/accounts/42/refresh", map[string]any{}).Code)
	adminService.account.Platform = service.PlatformGrok
	adminService.account.Type = service.AccountTypeAPIKey
	require.Equal(t, http.StatusBadRequest,
		grokHandlerRequest(t, router, http.MethodPost, "/accounts/42/refresh", map[string]any{}).Code)

	adminService.account.Type = service.AccountTypeOAuth
	refresh := grokHandlerRequest(t, router, http.MethodPost, "/accounts/42/refresh", map[string]any{})
	require.Equal(t, http.StatusOK, refresh.Code)
	require.Contains(t, refresh.Body.String(), `"id":42`)
}

func TestGrokOAuthHandlerCreateAccountFromOAuth(t *testing.T) {
	oauthService := service.NewGrokOAuthService(nil, grokOAuthHandlerClientStub{})
	defer oauthService.Stop()
	adminService := newStubAdminService()
	router := setupGrokOAuthHandlerRouter(NewGrokOAuthHandler(oauthService, adminService))

	require.Equal(t, http.StatusBadRequest,
		grokHandlerRequest(t, router, http.MethodPost, "/accounts", map[string]any{}).Code)

	authURL, err := oauthService.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	create := grokHandlerRequest(t, router, http.MethodPost, "/accounts", map[string]any{
		"session_id":  authURL.SessionID,
		"code":        "authorization-code",
		"state":       authURL.State,
		"name":        " ",
		"concurrency": 3,
		"priority":    7,
		"group_ids":   []int64{11, 12},
	})
	require.Equal(t, http.StatusOK, create.Code)
	require.Contains(t, create.Body.String(), "grok@example.com")
	require.Len(t, adminService.createdAccounts, 1)
	require.Equal(t, "grok@example.com", adminService.createdAccounts[0].Name)
	require.Equal(t, service.PlatformGrok, adminService.createdAccounts[0].Platform)
	require.Equal(t, service.AccountTypeOAuth, adminService.createdAccounts[0].Type)
}

func TestGrokOAuthHandlerServiceAndAdminErrors(t *testing.T) {
	oauthService := service.NewGrokOAuthService(nil, grokOAuthHandlerClientStub{})
	defer oauthService.Stop()
	adminService := &grokHandlerAdminService{stubAdminService: newStubAdminService()}
	router := setupGrokOAuthHandlerRouter(NewGrokOAuthHandler(oauthService, adminService))

	generate := grokHandlerRequest(t, router, http.MethodPost, "/auth-url", map[string]any{"proxy_id": 1})
	require.Equal(t, http.StatusBadRequest, generate.Code)

	invalidState := grokHandlerRequest(t, router, http.MethodPost, "/exchange", map[string]any{
		"session_id": "missing",
		"code":       "code",
	})
	require.Equal(t, http.StatusBadRequest, invalidState.Code)

	proxyRefresh := grokHandlerRequest(t, router, http.MethodPost, "/refresh", map[string]any{
		"refresh_token": "refresh-token",
		"proxy_id":      1,
	})
	require.Equal(t, http.StatusOK, proxyRefresh.Code)

	missingAccount := grokHandlerRequest(t, router, http.MethodPost, "/accounts/99/refresh", map[string]any{})
	require.Equal(t, http.StatusInternalServerError, missingAccount.Code)

	adminService.account = &service.Account{
		ID: 42, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "refresh-token"},
	}
	adminService.updateAccountErr = errors.New("update failed")
	updateErr := grokHandlerRequest(t, router, http.MethodPost, "/accounts/42/refresh", map[string]any{})
	require.Equal(t, http.StatusInternalServerError, updateErr.Code)

	adminService.updateAccountErr = nil
	adminService.createAccountErr = errors.New("create failed")
	authURL, err := oauthService.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	createErr := grokHandlerRequest(t, router, http.MethodPost, "/accounts", map[string]any{
		"session_id": authURL.SessionID,
		"code":       "authorization-code",
		"state":      authURL.State,
	})
	require.Equal(t, http.StatusInternalServerError, createErr.Code)
}
