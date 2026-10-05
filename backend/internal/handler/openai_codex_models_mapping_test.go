package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexMappingHTTPTransport struct{ client *http.Client }

func (u *codexMappingHTTPTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(req)
}
func (u *codexMappingHTTPTransport) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func newCodexMappingHandler(t *testing.T, server *httptest.Server, mapping map[string]any) (*OpenAIGatewayHandler, *codexModelsAccountRepoStub) {
	t.Helper()
	repo := &codexModelsAccountRepoStub{accounts: []service.Account{{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "first-key", "base_url": server.URL, "model_mapping": mapping},
	}}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, nil, &codexMappingHTTPTransport{client: server.Client()},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return &OpenAIGatewayHandler{gatewayService: gateway, maxAccountSwitches: 3}, repo
}

func requestCodexMappingHandler(t *testing.T, h *OpenAIGatewayHandler, path, validator string, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	c.Request.Header.Set("If-None-Match", validator)
	groupID := int64(42)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}})
	h.CodexModels(c)
	// Gin's engine commits deferred status headers after a handler returns.
	c.Writer.WriteHeaderNow()
	return recorder
}

func TestCodexAccountMapping_ActualHandlerRepresentations(t *testing.T) {
	for _, path := range []string{"/backend-api/codex/models?client_version=0.137.0", "/v1/models?client_version=0.137.0", "/models?client_version=0.137.0"} {
		for _, standard := range []bool{false, true} {
			name := path + "/manifest"
			if standard {
				name = path + "/standard"
			}
			t.Run(name, func(t *testing.T) {
				var mu sync.Mutex
				var gotConditional []string
				body := `{"models":[{"slug":"gpt-5.4","limits":900719925474099312345},{"slug":"blocked"}],"opaque":{"integer":900719925474099312345}}`
				if standard {
					body = `{"object":"list","data":[{"id":"gpt-5.4"},{"id":"blocked"}]}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					gotConditional = append(gotConditional, r.Header.Get("If-None-Match"))
					mu.Unlock()
					w.Header().Set("ETag", `W/"upstream"`)
					_, _ = w.Write([]byte(body))
				}))
				defer server.Close()
				h, repo := newCodexMappingHandler(t, server, map[string]any{"company-fast": "gpt-5.4"})
				first := requestCodexMappingHandler(t, h, path, "", context.Background())
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				var envelope struct {
					Models []struct {
						Slug string `json:"slug"`
					} `json:"models"`
				}
				require.NoError(t, json.Unmarshal(first.Body.Bytes(), &envelope))
				require.Len(t, envelope.Models, 1)
				require.Equal(t, "company-fast", envelope.Models[0].Slug)
				require.True(t, repo.accounts[0].IsModelSupported(envelope.Models[0].Slug))
				require.False(t, repo.accounts[0].IsModelSupported("blocked"))
				if !standard {
					require.Contains(t, first.Body.String(), `900719925474099312345`)
				}
				require.NotEqual(t, `W/"upstream"`, first.Header().Get("ETag"))
				second := requestCodexMappingHandler(t, h, path, first.Header().Get("ETag"), context.Background())
				require.Equal(t, http.StatusNotModified, second.Code)
				require.Empty(t, second.Body.String())
				mu.Lock()
				require.Equal(t, []string{"", ""}, gotConditional)
				mu.Unlock()
			})
		}
	}
}

func TestCodexAccountMapping_ActualHandlerAccountSwitchCannotReuseValidator(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	var failFirst bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		fail := failFirst && r.Header.Get("Authorization") == "Bearer first-key"
		mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}
		if strings.TrimSpace(r.Header.Get("If-None-Match")) != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"same-upstream-validator"`)
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.4"},{"slug":"blocked"}]}`))
	}))
	defer server.Close()
	h, repo := newCodexMappingHandler(t, server, map[string]any{"first-alias": "gpt-5.4"})
	first := requestCodexMappingHandler(t, h, "/v1/models?client_version=0.137.0", "", context.Background())
	require.Equal(t, http.StatusOK, first.Code)
	secondAccount := repo.accounts[0]
	secondAccount.ID = 2
	secondAccount.Priority = 10
	secondAccount.Credentials = map[string]any{"api_key": "second-key", "base_url": server.URL, "model_mapping": map[string]any{"second-alias": "gpt-5.4"}}
	repo.accounts = append(repo.accounts, secondAccount)
	mu.Lock()
	failFirst = true
	mu.Unlock()
	second := requestCodexMappingHandler(t, h, "/v1/models?client_version=0.137.0", first.Header().Get("ETag"), context.Background())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Contains(t, second.Body.String(), `"slug":"second-alias"`)
	require.NotContains(t, second.Body.String(), `first-alias`)
	require.NotEqual(t, first.Header().Get("ETag"), second.Header().Get("ETag"))
	mu.Lock()
	require.Equal(t, []string{"Bearer first-key", "Bearer first-key", "Bearer second-key"}, auths)
	mu.Unlock()
}

func TestCodexAccountMapping_ActualHandlerCanceledDoesNotDispatch(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	h, _ := newCodexMappingHandler(t, server, map[string]any{"alias": "gpt-5.4"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := requestCodexMappingHandler(t, h, "/v1/models?client_version=0.137.0", "", ctx)
	require.Empty(t, recorder.Body.String())
	mu.Lock()
	require.Zero(t, calls)
	mu.Unlock()
}

func TestCodexAccountMapping_IdentityAccountSwitchSharedProviderValidator(t *testing.T) {
	for _, variant := range []string{"different_models", "different_metadata", "same_bytes"} {
		t.Run(variant, func(t *testing.T) {
			bodyA := `{ "models": [ { "slug": "gpt-5.4", "limit": 100 } ], "opaque":900719925474099312345 }`
			bodyB := bodyA
			modelB := "gpt-5.4"
			if variant == "different_models" {
				modelB = "gpt-5.5"
				bodyB = `{ "models": [ { "slug": "gpt-5.5", "limit": 100 } ], "opaque":900719925474099312345 }`
			} else if variant == "different_metadata" {
				bodyB = `{ "models": [ { "slug": "gpt-5.4", "limit": 200 } ], "opaque":900719925474099312345 }`
			}
			var mu sync.Mutex
			var auths, conditionals []string
			failFirst := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth := r.Header.Get("Authorization")
				mu.Lock()
				auths = append(auths, auth)
				conditionals = append(conditionals, r.Header.Get("If-None-Match"))
				fail := failFirst && auth == "Bearer first-key"
				mu.Unlock()
				if fail {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
					return
				}
				w.Header().Set("ETag", `W/"v1"`)
				if r.Header.Get("If-None-Match") != "" {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				body := bodyA
				if auth == "Bearer second-key" {
					body = bodyB
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			h, repo := newCodexMappingHandler(t, server, map[string]any{"gpt-5.4": "gpt-5.4"})
			first := requestCodexMappingHandler(t, h, "/v1/models?client_version=0.137.0", "", context.Background())
			require.Equal(t, http.StatusOK, first.Code)
			require.Equal(t, bodyA, first.Body.String())
			secondAccount := repo.accounts[0]
			secondAccount.ID = 2
			secondAccount.Priority = 10
			secondAccount.Credentials = map[string]any{"api_key": "second-key", "base_url": server.URL, "model_mapping": map[string]any{modelB: modelB}}
			repo.accounts = append(repo.accounts, secondAccount)
			mu.Lock()
			failFirst = true
			mu.Unlock()
			second := requestCodexMappingHandler(t, h, "/v1/models?client_version=0.137.0", first.Header().Get("ETag"), context.Background())
			if variant == "same_bytes" {
				require.Equal(t, http.StatusNotModified, second.Code)
				require.Empty(t, second.Body.String())
				require.Equal(t, first.Header().Get("ETag"), second.Header().Get("ETag"))
			} else {
				require.Equal(t, http.StatusOK, second.Code, second.Body.String())
				require.Equal(t, bodyB, second.Body.String())
				require.NotEqual(t, first.Header().Get("ETag"), second.Header().Get("ETag"))
			}
			mu.Lock()
			require.Equal(t, []string{"Bearer first-key", "Bearer first-key", "Bearer second-key"}, auths)
			require.Equal(t, []string{"", "", ""}, conditionals)
			mu.Unlock()
		})
	}
}

func TestCodexAccountMapping_ActualHandlerDuplicateStructuralKeysRejected(t *testing.T) {
	for _, body := range []string{
		`{"models":[{"slug":"gpt-5.4"}],"models":[{"slug":"blocked"}]}`,
		`{"models":[{"slug":"gpt-5.4"}],"mo\u0064els":[{"slug":"blocked"}]}`,
		`{"models":[{"slug":"blocked","slug":"gpt-5.4"}]}`,
		`{"models":[{"slug":"blocked","sl\u0075g":"gpt-5.4"}]}`,
		`{"models":[{"slug":"gpt-5.4","display_name":"old","display_\u006eame":"actual"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			h, _ := newCodexMappingHandler(t, server, map[string]any{"alias": "gpt-5.4"})
			response := requestCodexMappingHandler(t, h, "/v1/models?client_version=0.137.0", "", context.Background())
			require.Equal(t, http.StatusBadGateway, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "upstream_error")
			require.NotContains(t, response.Body.String(), `"models"`)
			mu.Lock()
			require.Equal(t, 1, calls)
			mu.Unlock()
		})
	}
}
