package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type previewAdminService struct {
	*availableModelsAdminService
}

func (s *previewAdminService) GetProxy(_ context.Context, id int64) (*service.Proxy, error) {
	if id == 7 || id == 8 {
		return &service.Proxy{ID: id, Protocol: "http", Host: "proxy.example", Port: int(8000 + id)}, nil
	}
	return nil, errors.New("unknown proxy")
}

type previewUpstream struct {
	req         *http.Request
	proxy       string
	accountID   int64
	concurrency int
	profile     *tlsfingerprint.Profile
	err         error
}

func (u *previewUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.req, u.proxy, u.accountID, u.concurrency = req, proxy, id, concurrency
	if u.err != nil {
		return nil, u.err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"draft-model"}]}`))}, nil
}

func (u *previewUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.profile = profile
	return u.Do(req, proxy, id, concurrency)
}

func TestAccountHandlerModelPreviewDraft(t *testing.T) {
	for _, tc := range []struct {
		name, payload, wantURL, wantAuth, wantProxy string
	}{
		{"saved fallback", `{"account_id":42,"platform":"openai","type":"apikey","api_key":" "}`, "https://saved.example/v1/models", "Bearer saved-secret", "http://proxy.example:8007"},
		{"new credentials and proxy", `{"account_id":42,"platform":"openai","type":"apikey","base_url":" https://draft.example/v1 ","api_key":" draft-secret ","proxy_id":8}`, "https://draft.example/v1/models", "Bearer draft-secret", "http://proxy.example:8008"},
		{"clear proxy", `{"account_id":42,"platform":"openai","type":"apikey","proxy_id":0}`, "https://saved.example/v1/models", "Bearer saved-secret", ""},
		{"null clears proxy", `{"account_id":42,"platform":"openai","type":"apikey","proxy_id":null}`, "https://saved.example/v1/models", "Bearer saved-secret", ""},
		{"blank URL uses default", `{"account_id":42,"platform":"openai","type":"apikey","base_url":" "}`, "https://api.openai.com/v1/models", "Bearer saved-secret", "http://proxy.example:8007"},
		{"create draft", `{"platform":"openai","type":"apikey","base_url":"https://draft.example","api_key":"new-secret","proxy_id":8}`, "https://draft.example/v1/models", "Bearer new-secret", "http://proxy.example:8008"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := int64(7)
			svc := &previewAdminService{&availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{
				ID: 42, Platform: "openai", Type: "apikey", Concurrency: 3, ProxyID: &id,
				Credentials: map[string]any{"api_key": "saved-secret", "base_url": "https://saved.example", "header_override_enabled": true, "header_overrides": map[string]any{"X-Preview": "kept"}, "model_mapping": map[string]any{"saved": "target"}},
				Extra:       map[string]any{"enable_tls_fingerprint": true, "nested": map[string]any{"keep": "value"}},
			}}}
			before, err := json.Marshal(svc.account)
			require.NoError(t, err)
			upstream := &previewUpstream{}
			router := setupSyncUpstreamModelsRouter(svc, upstream)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/models/sync-upstream-preview", strings.NewReader(tc.payload)))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tc.wantURL, upstream.req.URL.String())
			require.Equal(t, tc.wantAuth, upstream.req.Header.Get("Authorization"))
			require.Equal(t, tc.wantProxy, upstream.proxy)
			require.Zero(t, upstream.accountID)
			require.Nil(t, upstream.profile, "API-key TLS fingerprint eligibility must stay unchanged")
			if strings.Contains(tc.payload, "account_id") {
				require.Equal(t, 3, upstream.concurrency)
				require.Equal(t, "kept", upstream.req.Header.Get("X-Preview"))
			}
			after, err := json.Marshal(svc.account)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "preview must not mutate saved credentials/extra/proxy")
			require.NotContains(t, rec.Body.String(), "secret")
			require.Empty(t, svc.createdAccounts)
		})
	}
}

func TestAccountHandlerModelPreviewRejectsUnsafeFallback(t *testing.T) {
	for _, tc := range []struct {
		name, platform, accountType, payload string
		status                               int
	}{
		{"different platform", "anthropic", "apikey", `{"account_id":42,"platform":"openai","type":"apikey","api_key":"new-secret"}`, 400},
		{"OAuth secret", "openai", "oauth", `{"account_id":42,"platform":"openai","type":"apikey","api_key":"new-secret"}`, 400},
		{"OAuth request", "openai", "oauth", `{"account_id":42,"platform":"openai","type":"oauth"}`, 400},
		{"missing saved account", "openai", "apikey", `{"account_id":9999,"platform":"openai","type":"apikey"}`, 404},
		{"missing key", "openai", "apikey", `{"platform":"openai","type":"apikey"}`, 400},
		{"unknown proxy", "openai", "apikey", `{"account_id":42,"platform":"openai","type":"apikey","proxy_id":99}`, 400},
		{"negative proxy", "openai", "apikey", `{"account_id":42,"platform":"openai","type":"apikey","proxy_id":-1}`, 400},
		{"invalid URL", "openai", "apikey", `{"account_id":42,"platform":"openai","type":"apikey","base_url":"file:///secret"}`, 400},
		{"AG blank URL", "antigravity", "apikey", `{"account_id":42,"platform":"antigravity","type":"apikey","base_url":""}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &previewAdminService{&availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{ID: 42, Platform: tc.platform, Type: tc.accountType, Credentials: map[string]any{"api_key": "saved-secret", "base_url": "https://saved.example"}}}}
			upstream := &previewUpstream{}
			router := setupSyncUpstreamModelsRouter(svc, upstream)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/models/sync-upstream-preview", strings.NewReader(tc.payload)))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Nil(t, upstream.req)
			require.NotContains(t, rec.Body.String(), "saved-secret")
		})
	}
}
