package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type availableModelsAdminService struct {
	*stubAdminService
	account service.Account
}

func (s *availableModelsAdminService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if s.account.ID == id {
		acc := s.account
		return &acc, nil
	}
	return s.stubAdminService.GetAccount(context.Background(), id)
}

func setupAvailableModelsRouter(adminSvc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts/:id/models", handler.GetAvailableModels)
	return router
}

type syncUpstreamHTTPUpstream struct {
	resp *http.Response
	err  error
}

func (u *syncUpstreamHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	if u.err != nil {
		return nil, u.err
	}
	return u.resp, nil
}

func (u *syncUpstreamHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func setupSyncUpstreamModelsRouter(adminSvc service.AdminService, upstream service.HTTPUpstream) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	accountTestSvc := service.NewAccountTestService(
		nil,
		nil,
		nil,
		nil,
		upstream,
		&config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		nil,
	)
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, accountTestSvc, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/:id/models/sync-upstream", handler.SyncUpstreamModels)
	return router
}

func TestAccountHandlerGetAvailableModels_OpenAIOAuthUsesExplicitModelMapping(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       42,
			Name:     "openai-oauth",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"gpt-5": "gpt-5.1",
				},
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	require.Equal(t, "gpt-5", resp.Data[0].ID)
}

func TestAccountHandlerGetAvailableModels_MappedModelOrderIsStable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		platform     string
		accountType  string
		catalogIDs   []string
		catalogNames []string
	}{
		{name: "openai", platform: service.PlatformOpenAI, accountType: service.AccountTypeOAuth,
			catalogIDs: []string{"gpt-6-astra", "gpt-6"}, catalogNames: []string{"GPT-6 Astra", "GPT-6 (Astra)"}},
		{name: "gemini", platform: service.PlatformGemini, accountType: service.AccountTypeAPIKey,
			catalogIDs: []string{"gemini-3.5-flash", "gemini-3-flash-preview"}, catalogNames: []string{"Gemini 3.5 Flash", "Gemini 3 Flash Preview"}},
		{name: "anthropic", platform: service.PlatformAnthropic, accountType: service.AccountTypeAPIKey,
			catalogIDs: []string{"claude-opus-5-5", "claude-opus-5"}, catalogNames: []string{"Claude Opus 5.5", "Claude Opus 5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapping := map[string]any{
				"zeta-custom": "upstream-zeta", "alpha-custom": "upstream-alpha",
				tc.catalogIDs[1]: "upstream-second", tc.catalogIDs[0]: "upstream-first",
			}
			svc := &availableModelsAdminService{
				stubAdminService: newStubAdminService(),
				account: service.Account{
					ID: 1400, Platform: tc.platform, Type: tc.accountType, Status: service.StatusActive,
					Credentials: map[string]any{"model_mapping": mapping},
				},
			}
			router := setupAvailableModelsRouter(svc)
			wantIDs := []string{tc.catalogIDs[0], tc.catalogIDs[1], "alpha-custom", "zeta-custom"}
			wantNames := []string{tc.catalogNames[0], tc.catalogNames[1], "alpha-custom", "zeta-custom"}
			for i := 0; i < 16; i++ {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/1400/models", nil))
				require.Equal(t, http.StatusOK, rec.Code)
				var resp struct {
					Data []struct {
						ID          string `json:"id"`
						DisplayName string `json:"display_name"`
						Type        string `json:"type"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				ids := make([]string, 0, len(resp.Data))
				names := make([]string, 0, len(resp.Data))
				for _, model := range resp.Data {
					ids = append(ids, model.ID)
					names = append(names, model.DisplayName)
					require.Equal(t, "model", model.Type)
				}
				require.Equal(t, wantIDs, ids, "request %d changed model order or set", i)
				require.Equal(t, wantNames, names, "request %d changed model display names", i)
			}
			require.Equal(t, map[string]any{
				"zeta-custom": "upstream-zeta", "alpha-custom": "upstream-alpha",
				tc.catalogIDs[1]: "upstream-second", tc.catalogIDs[0]: "upstream-first",
			}, mapping, "listing must not mutate the account mapping")
		})
	}
}

func TestAccountHandlerGetAvailableModels_AnthropicSetupTokenMappingAdmission(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mapping       map[string]any
		allowUnlisted bool
		wantDefault   bool
	}{
		{"legacy whitelist", map[string]any{"custom-sonnet": "claude-sonnet-4-6"}, false, false},
		{"rename with opt in", map[string]any{"custom-sonnet": "claude-sonnet-4-6"}, true, true},
		{"identity still whitelists", map[string]any{"custom-sonnet": "claude-sonnet-4-6", "claude-opus-4-6": "claude-opus-4-6"}, true, false},
		{"legacy wildcard identity fails closed outside pattern", map[string]any{"custom-sonnet": "claude-sonnet-4-6", "claude-opus-*": "claude-opus-*"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials := map[string]any{
				"model_mapping":                tc.mapping,
				"model_mapping_allow_unlisted": tc.allowUnlisted,
			}
			account := service.Account{
				ID:          7544,
				Platform:    service.PlatformAnthropic,
				Type:        service.AccountTypeSetupToken,
				Status:      service.StatusActive,
				Credentials: credentials,
			}
			svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: account}
			router := setupAvailableModelsRouter(svc)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/7544/models", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var resp struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			ids := make([]string, 0, len(resp.Data))
			for _, model := range resp.Data {
				ids = append(ids, model.ID)
			}
			require.Contains(t, ids, "custom-sonnet")
			if tc.wantDefault {
				require.Contains(t, ids, "claude-haiku-4-5-20251001")
			} else {
				require.NotContains(t, ids, "claude-haiku-4-5-20251001")
			}
			require.Equal(t, tc.mapping, credentials["model_mapping"], "listing must not mutate the cached mapping")
		})
	}
}

func TestAccountHandlerGetAvailableModels_OpenAIOAuthMappingOptInPreservesDefaultCatalog(t *testing.T) {
	mapping := map[string]any{"my-gpt": "gpt-5.4"}
	svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{
		ID:       7545,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"model_mapping": mapping, "model_mapping_allow_unlisted": true,
		},
	}}
	rec := httptest.NewRecorder()
	setupAvailableModelsRouter(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/7545/models", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
	}
	require.Contains(t, ids, "my-gpt")
	require.Contains(t, ids, "gpt-5.4")
	require.Equal(t, append(openai.DefaultModelIDs(), "my-gpt"), ids, "opt-in must preserve the curated catalog order")
	require.Equal(t, map[string]any{"my-gpt": "gpt-5.4"}, mapping)
}

func TestAccountHandlerGetAvailableModels_GeminiAPIKeyMappingOptInPreservesDefaultCatalog(t *testing.T) {
	svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{
		ID:       7546,
		Platform: service.PlatformGemini,
		Type:     service.AccountTypeAPIKey,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"model_mapping":                map[string]any{"my-gemini": "gemini-2.5-pro"},
			"model_mapping_allow_unlisted": true,
		},
	}}
	rec := httptest.NewRecorder()
	setupAvailableModelsRouter(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/7546/models", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
	}
	require.Contains(t, ids, "my-gemini")
	require.Contains(t, ids, "gemini-2.5-pro")
	wantCatalogIDs := make([]string, 0, len(geminicli.DefaultModels)+1)
	for _, model := range geminicli.DefaultModels {
		wantCatalogIDs = append(wantCatalogIDs, model.ID)
	}
	require.Equal(t, append(wantCatalogIDs, "my-gemini"), ids, "opt-in must preserve the curated catalog order")
}

func TestAccountHandlerGetAvailableModels_OpenAIOAuthPassthroughFallsBackToDefaults(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       43,
			Name:     "openai-oauth-passthrough",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"gpt-5": "gpt-5.1",
				},
			},
			Extra: map[string]any{
				"openai_passthrough": true,
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/43/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data)
	require.NotEqual(t, "gpt-5", resp.Data[0].ID)
}

func TestAccountHandlerGetAvailableModels_GeminiGoogleOneUsesConservativeCatalog(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       45,
			Name:     "google-one",
			Platform: service.PlatformGemini,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"oauth_type": "google_one",
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/45/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
	}
	require.ElementsMatch(t, []string{"gemini-2.0-flash", "gemini-2.5-flash", "gemini-2.5-pro"}, ids)
	require.NotContains(t, ids, "gemini-3.5-flash")
	require.NotContains(t, ids, "gemini-2.5-flash-image")
}

func TestAccountHandlerGetAvailableModels_AntigravityMappingRestrictsTestPicker(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       46,
			Name:     "antigravity-mapped",
			Platform: service.PlatformAntigravity,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"claude-sonnet-4-6": "claude-sonnet-4-6",
					"custom-sonnet":     "claude-sonnet-4-6",
					"gemini-alias":      "gemini-3.6-flash",
					"custom-gemini":     "gemini-3.6-flash",
					"gemini-cross":      "claude-sonnet-4-6",
				},
			},
		},
	}
	router := setupAvailableModelsRouter(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/46/models", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	byID := make(map[string]string, len(resp.Data))
	for _, model := range resp.Data {
		byID[model.ID] = model.DisplayName
	}
	require.Equal(t, "Claude Sonnet 4.6", byID["claude-sonnet-4-6"])
	require.Equal(t, "custom-sonnet", byID["custom-sonnet"])
	require.Equal(t, "gemini-alias", byID["gemini-alias"])
	require.Contains(t, byID, "gemini-3.6-flash", "implicit Antigravity passthrough remains selectable")
	require.NotContains(t, byID, "claude-opus-4-6", "unmapped default must not be offered")
	require.NotContains(t, byID, "custom-gemini", "the test would send a Claude payload for a Gemini target")
	require.NotContains(t, byID, "gemini-cross", "the test would send a Gemini payload for a Claude target")
}

func TestAccountHandlerGetAvailableModels_AntigravityInvalidMappingTargets(t *testing.T) {
	for _, target := range []string{"", "   ", " claude-sonnet-4-6 "} {
		t.Run("target="+target, func(t *testing.T) {
			svc := &availableModelsAdminService{
				stubAdminService: newStubAdminService(),
				account: service.Account{
					ID:       48,
					Platform: service.PlatformAntigravity,
					Type:     service.AccountTypeOAuth,
					Status:   service.StatusActive,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"claude-opus-4-6": target,
							"custom-opus":     target,
						},
					},
				},
			}
			router := setupAvailableModelsRouter(svc)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/48/models", nil)
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			var resp struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			ids := make([]string, 0, len(resp.Data))
			for _, model := range resp.Data {
				ids = append(ids, model.ID)
			}
			require.NotContains(t, ids, "claude-opus-4-6")
			require.NotContains(t, ids, "custom-opus")
			require.Contains(t, ids, "gemini-3.6-flash", "implicit passthrough remains selectable")
		})
	}
}

func TestAccountHandlerGetAvailableModels_AntigravityEmptyMappingKeys(t *testing.T) {
	for _, key := range []string{"", "   "} {
		t.Run("key="+key, func(t *testing.T) {
			svc := &availableModelsAdminService{
				stubAdminService: newStubAdminService(),
				account: service.Account{
					ID:       50,
					Platform: service.PlatformAntigravity,
					Type:     service.AccountTypeOAuth,
					Status:   service.StatusActive,
					Credentials: map[string]any{
						"model_mapping": map[string]any{key: "claude-sonnet-4-6"},
					},
				},
			}
			router := setupAvailableModelsRouter(svc)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/50/models", nil)
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			var resp struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			ids := make([]string, 0, len(resp.Data))
			for _, model := range resp.Data {
				ids = append(ids, model.ID)
			}
			require.NotContains(t, ids, "claude-opus-4-6", "malformed mapping is still configured")
			require.NotContains(t, ids, key, "blank alias is not selectable")
			require.Contains(t, ids, "gemini-3.6-flash", "implicit passthrough remains selectable")
		})
	}
}

func TestAccountHandlerGetAvailableModels_AntigravityWildcardAndDefaultCatalog(t *testing.T) {
	account := service.Account{
		ID:       47,
		Name:     "antigravity-default",
		Platform: service.PlatformAntigravity,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
	}
	svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: account}
	router := setupAvailableModelsRouter(svc)
	requestModels := func() []string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/47/models", nil)
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		ids := make([]string, 0, len(resp.Data))
		for _, model := range resp.Data {
			ids = append(ids, model.ID)
		}
		return ids
	}

	defaults := requestModels()
	require.Contains(t, defaults, "claude-opus-4-6")
	account.Credentials = map[string]any{
		"model_mapping": map[string]any{"claude-sonnet-*": "claude-sonnet-4-6"},
	}
	svc.account = account
	mapped := requestModels()
	require.Contains(t, mapped, "claude-sonnet-4-6")
	require.Contains(t, mapped, "gemini-3.6-flash")
	require.NotContains(t, mapped, "claude-opus-4-6")
	require.NotContains(t, mapped, "claude-sonnet-*")

	account.Type = service.AccountTypeAPIKey
	account.Credentials = map[string]any{
		"model_mapping": map[string]any{"gemini-3.6-*": "gemini-3.6-flash-high"},
	}
	svc.account = account
	apiKeyModels := requestModels()
	require.Contains(t, apiKeyModels, "gemini-3.6-flash")
	require.NotContains(t, apiKeyModels, "gemini-3.6-*")
}

func TestAccountHandlerSyncUpstreamModels_ConfigErrorReturnsBadRequest(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       44,
			Name:     "openai-apikey-missing-key",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeAPIKey,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"base_url": "https://openai.example.com/v1",
			},
		},
	}
	router := setupSyncUpstreamModelsRouter(svc, &syncUpstreamHTTPUpstream{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/44/models/sync-upstream", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "No OpenAI API key is available")
}

func TestAccountHandlerSyncUpstreamModels_UpstreamErrorDoesNotExposeBody(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       45,
			Name:     "openai-apikey-upstream-error",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeAPIKey,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"api_key":  "openai-key",
				"base_url": "https://openai.example.com/v1",
			},
		},
	}
	upstream := &syncUpstreamHTTPUpstream{resp: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"SECRET_TOKEN should not be exposed"}`)),
	}}
	router := setupSyncUpstreamModelsRouter(svc, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/45/models/sync-upstream", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "Upstream model list request failed with HTTP 502")
	require.NotContains(t, rec.Body.String(), "SECRET_TOKEN")
}
