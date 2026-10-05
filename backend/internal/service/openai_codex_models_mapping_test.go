package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexMappingFixture struct {
	mu      sync.Mutex
	body    string
	status  int
	etag    string
	headers []http.Header
	server  *httptest.Server
	service *OpenAIGatewayService
	account *Account
}

func newCodexMappingFixture(t *testing.T, body string, oauth bool) *codexMappingFixture {
	t.Helper()
	f := &codexMappingFixture{body: body, status: http.StatusOK, etag: `W/"upstream"`}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		body, status, etag := f.body, f.status, f.etag
		f.mu.Unlock()
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.server.Close)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	f.service = &OpenAIGatewayService{cfg: cfg}
	f.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "fixture-key", "base_url": f.server.URL}}
	if oauth {
		original := chatgptCodexModelsURL
		chatgptCodexModelsURL = f.server.URL
		t.Cleanup(func() { chatgptCodexModelsURL = original })
		f.account.Type = AccountTypeOAuth
		f.account.Credentials["access_token"] = "fixture-token"
	}
	return f
}

func (f *codexMappingFixture) fetch(t *testing.T, validator string) *CodexModelsManifest {
	t.Helper()
	manifest, err := f.service.FetchCodexModelsManifest(context.Background(), f.account, "0.137.0", validator)
	require.NoError(t, err)
	return manifest
}

func codexMappingSlugs(t *testing.T, body []byte) []string {
	t.Helper()
	var envelope struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	ids := make([]string, 0, len(envelope.Models))
	for _, model := range envelope.Models {
		ids = append(ids, model.Slug)
	}
	return ids
}

func TestCodexAccountMapping_RealServiceProjection(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		label := "apikey"
		if oauth {
			label = "oauth"
		}
		t.Run(label, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				mapping  map[string]any
				unlisted bool
				want     []string
			}{
				{"strict_wildcard", map[string]any{"gpt-*": "gpt-5.4"}, false, []string{"gpt-5.4", "gpt-6.1-sol"}},
				{"exact_alias", map[string]any{"company-fast": "gpt-5.4"}, false, []string{"company-fast"}},
				{"unlisted_rename", map[string]any{"company-fast": "gpt-5.4"}, true, []string{"gpt-5.4", "blocked", "gpt-6.1-sol", "company-fast"}},
				{"identity_keeps_whitelist", map[string]any{"gpt-5.4": "gpt-5.4", "company-fast": "gpt-5.4"}, true, []string{"gpt-5.4", "company-fast"}},
				{"winning_wildcard", map[string]any{"gpt-*": "absent", "gpt-5.*": "gpt-6.1-sol"}, false, []string{"gpt-5.4"}},
				{"invalid_targets", map[string]any{"blank": " ", "absent": "missing", "wild": "gpt-*"}, false, []string{}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f := newCodexMappingFixture(t, `{"opaque":{"integer":900719925474099312345,"future":{"keep":true}},"models":[{"slug":"gpt-5.4","display_name":"actual","limits":{"integer":900719925474099312345}},{"slug":"blocked"},{"slug":"gpt-6.1-sol","use_responses_lite":false}]}`, oauth)
					f.account.Credentials["model_mapping"] = tc.mapping
					f.account.Credentials[ModelMappingAllowUnlistedCredentialKey] = tc.unlisted
					manifest := f.fetch(t, "")
					require.Equal(t, tc.want, codexMappingSlugs(t, manifest.Body))
					require.Contains(t, string(manifest.Body), `900719925474099312345`)
					require.Contains(t, string(manifest.Body), `"future":{"keep":true}`)
					require.NotEqual(t, `W/"upstream"`, manifest.ETag)
				})
			}
		})
	}
}

func TestCodexAccountMapping_RepresentationAndConditional(t *testing.T) {
	for _, mode := range []string{"no_mapping", "passthrough", "legacy_passthrough", "identity", "empty"} {
		t.Run(mode, func(t *testing.T) {
			body := `{ "opaque": 900719925474099312345, "models": [ { "slug": "gpt-5.4", "future": { "keep": true } } ] }`
			if mode == "empty" {
				body = `{ "models": [] , "opaque":900719925474099312345 }`
			}
			f := newCodexMappingFixture(t, body, false)
			if mode != "no_mapping" {
				f.account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.4"}
			}
			if mode == "passthrough" {
				f.account.Extra = map[string]any{"openai_passthrough": true}
			}
			if mode == "legacy_passthrough" {
				f.account.Extra = map[string]any{"openai_oauth_passthrough": true}
			}
			manifest := f.fetch(t, "")
			require.Equal(t, body, string(manifest.Body))
			if mode == "identity" || mode == "empty" {
				require.NotEqual(t, `W/"upstream"`, manifest.ETag)
				cached := f.fetch(t, `"other", `+strings.TrimPrefix(manifest.ETag, "W/"))
				require.True(t, cached.NotModified)
				require.Empty(t, cached.Body)
				f.mu.Lock()
				require.Empty(t, f.headers[1].Get("If-None-Match"))
				f.mu.Unlock()
			} else {
				require.Equal(t, `W/"upstream"`, manifest.ETag)
			}
		})
	}
}

func TestCodexAccountMapping_ChangesCannotReuseUpstream304(t *testing.T) {
	f := newCodexMappingFixture(t, `{"models":[{"slug":"gpt-5.4"},{"slug":"blocked"}]}`, false)
	f.account.Credentials["model_mapping"] = map[string]any{"first": "gpt-5.4"}
	first := f.fetch(t, "")
	cached := f.fetch(t, first.ETag)
	require.True(t, cached.NotModified)
	f.account.Credentials["model_mapping"] = map[string]any{"second": "gpt-5.4"}
	second := f.fetch(t, first.ETag)
	require.False(t, second.NotModified)
	require.Equal(t, []string{"second"}, codexMappingSlugs(t, second.Body))
	require.NotEqual(t, first.ETag, second.ETag)
	f.mu.Lock()
	for _, header := range f.headers {
		require.Empty(t, header.Get("If-None-Match"))
	}
	f.status = http.StatusNotModified
	f.mu.Unlock()
	_, err := f.service.FetchCodexModelsManifest(context.Background(), f.account, "0.137.0", second.ETag)
	require.Error(t, err)
	require.True(t, IsRetryableCodexModelsManifestError(err))
}

func TestCodexAccountMapping_DuplicatesDoNotFabricateEntitlements(t *testing.T) {
	f := newCodexMappingFixture(t, `{"opaque":{ "text": "<tag>", "escape": "\u003c", "big":900719925474099312345 },"models":[{"slug":"gpt-5.4","future":900719925474099312345,"unknown":{ "text": "<tag>", "escape": "\u003c" }},{"slug":"gpt-5.4","future":0},{"slug":"company-fast","future":-1},{"slug":"gpt-*"}]}`, false)
	f.account.Credentials["model_mapping"] = map[string]any{"company-fast": "gpt-5.4"}
	manifest := f.fetch(t, "")
	require.Equal(t, []string{"company-fast"}, codexMappingSlugs(t, manifest.Body))
	require.Contains(t, string(manifest.Body), `"future":900719925474099312345`)
	require.NotContains(t, string(manifest.Body), `"future":-1`)
	require.Contains(t, string(manifest.Body), `"opaque":{ "text": "<tag>", "escape": "\u003c", "big":900719925474099312345 }`)
	require.Contains(t, string(manifest.Body), `"unknown":{ "text": "<tag>", "escape": "\u003c" }`)
}

func TestCodexAccountMapping_InvalidAndCanceledRemainErrors(t *testing.T) {
	for _, body := range []string{`{"models":null}`, `{"models":{}}`, `{"error":{"message":"bad"}}`} {
		t.Run(body, func(t *testing.T) {
			f := newCodexMappingFixture(t, body, false)
			f.account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-5.4"}
			_, err := f.service.FetchCodexModelsManifest(context.Background(), f.account, "0.137.0", "")
			require.Error(t, err)
			require.True(t, IsRetryableCodexModelsManifestError(err))
		})
	}
	f := newCodexMappingFixture(t, `{"models":[]}`, false)
	f.account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-5.4"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.service.FetchCodexModelsManifest(ctx, f.account, "0.137.0", "")
	require.Error(t, err)
	require.False(t, IsRetryableCodexModelsManifestError(err))
	f.mu.Lock()
	require.Empty(t, f.headers)
	f.mu.Unlock()
}

func TestCodexAccountMapping_Validators(t *testing.T) {
	f := newCodexMappingFixture(t, `{"models":[{"slug":"gpt-5.4"}]}`, false)
	f.account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-5.4"}
	first := f.fetch(t, "")
	for _, value := range []string{first.ETag, strings.TrimPrefix(first.ETag, "W/"), `"wrong", ` + first.ETag, "*"} {
		require.True(t, f.fetch(t, value).NotModified)
	}
	for _, value := range []string{`bad`, `"missing`, `"wrong"`, `W/"wrong"`, `"wrong" extra`} {
		require.False(t, f.fetch(t, value).NotModified)
	}
}

func TestCodexAccountMapping_StarWithoutUpstreamValidator(t *testing.T) {
	f := newCodexMappingFixture(t, `{ "models": [ { "slug": "gpt-5.4" } ] }`, false)
	f.account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.4"}
	f.mu.Lock()
	f.etag = ""
	f.mu.Unlock()
	manifest := f.fetch(t, "*")
	require.True(t, manifest.NotModified)
	require.Empty(t, manifest.Body)
	require.NotEmpty(t, manifest.ETag)
}

func TestCodexAccountMapping_AccountOverridesCannotMakeFetchConditional(t *testing.T) {
	f := newCodexMappingFixture(t, `{"models":[{"slug":"gpt-5.4"},{"slug":"blocked"}]}`, false)
	f.account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-5.4"}
	f.account.Credentials["header_override_enabled"] = true
	f.account.Credentials["header_overrides"] = map[string]any{"If-None-Match": `W/"provider"`, "If-Modified-Since": "Mon, 05 Oct 2026 00:00:00 GMT", "X-Catalog-Test": "preserved"}
	manifest := f.fetch(t, `W/"client"`)
	require.Equal(t, []string{"alias"}, codexMappingSlugs(t, manifest.Body))
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.headers, 1)
	require.Empty(t, f.headers[0].Get("If-None-Match"))
	require.Empty(t, f.headers[0].Get("If-Modified-Since"))
	require.Equal(t, "preserved", f.headers[0].Get("X-Catalog-Test"))
}

func TestCodexAccountMapping_DuplicateStructuralKeysFailClosed(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, body := range []string{
			`{"models":[{"slug":"gpt-5.4"}],"models":[{"slug":"blocked"}]}`,
			`{"models":[{"slug":"gpt-5.4"}],"mo\u0064els":[{"slug":"blocked"}]}`,
			`{"models":[{"slug":"blocked","slug":"gpt-5.4"}]}`,
			`{"models":[{"slug":"blocked","sl\u0075g":"gpt-5.4"}]}`,
			`{"models":[{"slug":"gpt-5.4","display_name":"old","display_name":"actual"}]}`,
			`{"models":[{"slug":"gpt-5.4","display_name":"old","display_\u006eame":"actual"}]}`,
		} {
			t.Run(body+"/"+map[bool]string{false: "apikey", true: "oauth"}[oauth], func(t *testing.T) {
				f := newCodexMappingFixture(t, body, oauth)
				f.account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-5.4"}
				manifest, err := f.service.FetchCodexModelsManifest(context.Background(), f.account, "0.137.0", "")
				require.Error(t, err)
				require.True(t, IsRetryableCodexModelsManifestError(err))
				require.Nil(t, manifest)
				// This guard belongs only to mapped projection; old passthrough
				// and unmapped representations remain byte-for-byte opaque.
				delete(f.account.Credentials, "model_mapping")
				require.Equal(t, body, string(f.fetch(t, "").Body))
			})
		}
	}
	f := newCodexMappingFixture(t, `{"opaque":1,"opaque":2,"models":[{"slug":"gpt-5.4","future":1,"future":2,"big":900719925474099312345}]}`, false)
	f.account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-5.4"}
	body := string(f.fetch(t, "").Body)
	require.Contains(t, body, `"opaque":1,"opaque":2`)
	require.Contains(t, body, `"future":1,"future":2,"big":900719925474099312345`)
	require.Contains(t, body, `"slug":"alias"`)
}
