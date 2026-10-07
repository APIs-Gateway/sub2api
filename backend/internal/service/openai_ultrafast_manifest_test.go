//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAstraUltrafast_LiveManifest(t *testing.T) {
	for _, tc := range []struct {
		name, base, body string
		added, invalid   bool
	}{
		{"official_list", "https://api.openai.com/v1", `{"object":"list","data":[{"id":"gpt-6-astra"},{"id":"gpt-6-sol"}]}`, true, false},
		{"official_native", "https://api.openai.com", `{"opaque":{"keep":1},"models":[{"slug":"gpt-6-astra","opaque":{"keep":2,"keep":3}},{"slug":"other","str":"literal"}]}`, true, false},
		{"explicit_null", "https://api.openai.com", `{"models":[{"slug":"gpt-6-astra","service_tiers":null}]}`, false, false},
		{"explicit_empty", "https://api.openai.com", `{"models":[{"slug":"gpt-6-astra","service_tiers":[]}]}`, false, false},
		{"explicit_priority", "https://api.openai.com", `{"models":[{"slug":"gpt-6-astra","service_tiers":[{"id":"priority"}]}]}`, false, false},
		{"custom", "https://provider.example", `{"models":[{"slug":"gpt-6-astra"}]}`, false, false},
		{"spoof", "https://api.openai.com.provider.example", `{"models":[{"slug":"gpt-6-astra"}]}`, false, false},
		{"other_model", "https://api.openai.com", `{"models":[{"slug":"gpt-6-sol"}]}`, false, false},
		{"nonobject", "https://api.openai.com", `{"models":[null,1,{"slug":7}]}`, false, false},
		{"empty", "https://api.openai.com", `{"models":[]}`, false, false},
		{"duplicate_slug", "https://api.openai.com", `{"models":[{"slug":"other","slug":"gpt-6-astra"}]}`, false, true},
		{"duplicate_envelope", "https://api.openai.com", `{"models":[],"models":[{"slug":"gpt-6-astra"}]}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := func() *http.Response {
				return &http.Response{StatusCode: 200, Header: http.Header{"Etag": []string{`"provider-reused"`}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			}
			u := &httpUpstreamRecorder{responses: []*http.Response{response(), response()}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: u}
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-only", "base_url": tc.base}}
			manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.153.0", `"provider-reused"`)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.False(t, manifest.NotModified)
			body := string(manifest.Body)
			require.Equal(t, tc.added, strings.Contains(body, `"id":"ultrafast"`))
			if tc.added {
				require.NotEqual(t, `"provider-reused"`, manifest.ETag)
			}
			if tc.name == "official_native" {
				require.Contains(t, body, `"opaque":{"keep":2,"keep":3}`)
			}
			if strings.HasPrefix(tc.name, "explicit_") {
				require.JSONEq(t, tc.body, body)
			}
			if tc.base == "https://api.openai.com" || tc.base == "https://api.openai.com/v1" {
				require.Empty(t, u.lastReq.Header.Get("If-None-Match"))
				second, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.153.0", manifest.ETag)
				require.NoError(t, err)
				require.True(t, second.NotModified)
				require.Equal(t, manifest.ETag, second.ETag)
			} else {
				require.Equal(t, `"provider-reused"`, u.lastReq.Header.Get("If-None-Match"))
			}
		})
	}
}

func TestAstraUltrafast_OAuthDoesNotInferFromPlan(t *testing.T) {
	for _, native := range []string{"", `,"service_tiers":[{"id":"ultrafast","name":"Account-qualified"}]`} {
		t.Run(native, func(t *testing.T) {
			body := `{"models":[{"slug":"gpt-6-astra"` + native + `}]}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			old := chatgptCodexModelsURL
			chatgptCodexModelsURL = server.URL
			defer func() { chatgptCodexModelsURL = old }()
			account := newCodexModelsTestAccount()
			account.Extra = map[string]any{"plan_type": "pro"}
			svc := &OpenAIGatewayService{}
			manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.153.0", "")
			require.NoError(t, err)
			require.Equal(t, body, string(manifest.Body))
			require.Equal(t, native != "", gjson.GetBytes(manifest.Body, "models.0.service_tiers").Exists())
		})
	}
}
