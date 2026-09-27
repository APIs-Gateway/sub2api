package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
)

func TestDecideResponsesProbeSupport(t *testing.T) {
	fnCall := []byte(`{"output":[{"type":"reasoning"},{"type":"function_call","name":"probe_ping"}]}`)
	reasoningOnly := []byte(`{"output":[{"type":"reasoning"}]}`)

	cases := []struct {
		name   string
		status int
		body   []byte
		want   bool
	}{
		// Endpoint clearly absent on third-party OpenAI-compatible upstreams.
		{"404 endpoint absent", 404, fnCall, false},
		{"405 method not allowed", 405, fnCall, false},
		// 2xx: tool capability is judged by presence of a function_call output item.
		{"200 with function_call", 200, fnCall, true},
		// Volcengine Ark coding/v3 × kimi-k2.6: reasoning only, no function_call.
		{"200 reasoning only", 200, reasoningOnly, false},
		{"200 invalid json", 200, []byte("not-json"), false},
		{"200 no output field", 200, []byte(`{"status":"completed"}`), false},
		// Non-2xx (other than 404/405): endpoint exists, capability undecidable -> conservative true.
		{"400 conservative true", 400, reasoningOnly, true},
		{"401 conservative true", 401, nil, true},
		{"500 conservative true", 500, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, decideResponsesProbeSupport(tc.status, tc.body))
		})
	}
}

func TestResponsesProbeBodyHasFunctionCall(t *testing.T) {
	require.True(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"function_call"}]}`)))
	require.True(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"reasoning"},{"type":"function_call"}]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"reasoning"}]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`garbage`)))
}

func TestSelectResponsesProbeModel(t *testing.T) {
	// No model_mapping -> fall back to DefaultTestModel (OpenAI official APIKey).
	require.Equal(t, openai.DefaultTestModel, selectResponsesProbeModel(&Account{}))

	// model_mapping values are upstream models; pick first by sort for reproducibility.
	acct := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{
			"client-b": "zeta-model",
			"client-a": "alpha-model",
		},
	}}
	require.Equal(t, "alpha-model", selectResponsesProbeModel(acct))

	// Wildcard / blank upstream values are skipped.
	acctWild := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{
			"a": "*",
			"b": "  ",
			"c": "real-model",
		},
	}}
	require.Equal(t, "real-model", selectResponsesProbeModel(acctWild))

	// Only wildcard mappings -> DefaultTestModel.
	acctAllWild := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{"a": "gpt-*"},
	}}
	require.Equal(t, openai.DefaultTestModel, selectResponsesProbeModel(acctAllWild))
}

func TestProbeOpenAIAPIKeyResponsesSupportAddsCodexHeaders(t *testing.T) {
	account := Account{
		ID:          96,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat-upstream.example/v1",
		},
	}
	updates := make(chan map[string]any, 1)
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      updates,
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"output":[{"type":"function_call"}]}`)),
	}}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}

	svc.ProbeOpenAIAPIKeyResponsesSupport(context.Background(), account.ID)

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://compat-upstream.example/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, codexCLIUserAgent, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "codex_cli_rs", upstream.lastReq.Header.Get("Originator"))
	require.Equal(t, codexCLIVersion, upstream.lastReq.Header.Get("Version"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("X-Codex-Window-ID"))

	select {
	case got := <-updates:
		require.Equal(t, true, got[openai_compat.ExtraKeyResponsesSupported])
	case <-time.After(time.Second):
		t.Fatal("expected probe result to be persisted")
	}
}

func TestOfficialOpenAIResponsesProbeURL(t *testing.T) {
	tests := []struct {
		baseURL string
		want    bool
	}{
		{"https://api.openai.com", true},
		{"https://api.openai.com/v1", true},
		{"https://API.OPENAI.COM:443/v1/", true},
		{"https://api.openai.com.example/v1", false},
		{"https://api.openai.com:8443/v1", false},
		{"https://api.openai.com/custom", false},
		{"http://api.openai.com/v1", false},
		{"https://user@api.openai.com/v1", false},
		{"https://api.openai.com/v1?proxy=1", false},
		{"https://api.openai.com/v1#fragment", false},
		{"not a url", false},
	}
	for _, tc := range tests {
		t.Run(tc.baseURL, func(t *testing.T) {
			require.Equal(t, tc.want, isOfficialOpenAIResponsesProbeURL(tc.baseURL))
		})
	}
}

func TestProbeOpenAIAPIKeyResponsesSupportOfficialEndpointSkipsModelProbe(t *testing.T) {
	tests := []struct {
		name             string
		baseURL          string
		allowlistEnabled bool
	}{
		{"default", "", false},
		{"root", "https://api.openai.com", false},
		{"v1", "https://api.openai.com/v1", false},
		// With the allowlist enabled, URL validation canonicalizes this escaped
		// spelling to /v1 before the official-endpoint check.
		{"normalized escaped v1", "https://api.openai.com/%76%31", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			account := Account{
				ID: 97, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key": "sk-test", "base_url": tc.baseURL,
					"model_mapping": map[string]any{"legacy": "babbage-002", "current": "gpt-6-sol"},
				},
				Extra: map[string]any{openai_compat.ExtraKeyResponsesSupported: false},
			}
			require.Equal(t, "babbage-002", selectResponsesProbeModel(&account))
			updates := make(chan map[string]any, 1)
			repo := &snapshotUpdateAccountRepo{
				stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
				updateExtraCalls:      updates,
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"model_not_found"}}`)),
			}}
			svc := &AccountTestService{
				accountRepo: repo, httpUpstream: upstream,
				cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
					Enabled: tc.allowlistEnabled, UpstreamHosts: []string{"api.openai.com"},
				}}},
			}

			svc.ProbeOpenAIAPIKeyResponsesSupport(context.Background(), account.ID)

			require.Nil(t, upstream.lastReq, "official endpoint must not be judged by a legacy model")
			select {
			case got := <-updates:
				require.Equal(t, true, got[openai_compat.ExtraKeyResponsesSupported])
				require.True(t, openai_compat.ShouldUseResponsesAPI(got), "persisted cache should route through Responses")
			case <-time.After(time.Second):
				t.Fatal("expected official endpoint capability to be persisted")
			}
		})
	}
}

func TestProbeOpenAIAPIKeyResponsesSupportCustomEndpointKeepsModelUnavailableUnknown(t *testing.T) {
	for _, tc := range []struct {
		name             string
		baseURL          string
		wantProbeURL     string
		allowlistEnabled bool
	}{
		{"lookalike host", "https://api.openai.com.example/v1", "https://api.openai.com.example/v1/responses", false},
		{"custom port", "https://api.openai.com:8443/v1", "https://api.openai.com:8443/v1/responses", true},
		{"custom path", "https://api.openai.com/custom", "https://api.openai.com/custom/v1/responses", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := Account{
				ID: 98, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key": "sk-test", "base_url": tc.baseURL,
					"model_mapping": map[string]any{"legacy": "babbage-002", "current": "gpt-6-sol"},
				},
			}
			updates := make(chan map[string]any, 1)
			repo := &snapshotUpdateAccountRepo{
				stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
				updateExtraCalls:      updates,
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"model_not_found"}}`)),
			}}
			svc := &AccountTestService{
				accountRepo: repo, httpUpstream: upstream,
				cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
					Enabled: tc.allowlistEnabled, UpstreamHosts: []string{"api.openai.com"},
				}}},
			}

			svc.ProbeOpenAIAPIKeyResponsesSupport(context.Background(), account.ID)

			require.NotNil(t, upstream.lastReq, "custom endpoint must retain the capability probe")
			require.Equal(t, tc.wantProbeURL, upstream.lastReq.URL.String())
			select {
			case <-updates:
				t.Fatal("model-not-found does not prove a custom endpoint lacks Responses")
			default:
			}
		})
	}
}
