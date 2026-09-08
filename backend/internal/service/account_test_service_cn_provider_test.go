//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func cnProviderTestAccount(id int64, apiProtocol string, extra map[string]any, credentialOverrides map[string]any) *Account {
	creds := map[string]any{
		"api_key":      "sk-cn-test",
		"base_url":     "https://relay.example.com/v1",
		"cn_provider":  true,
		"api_protocol": apiProtocol,
	}
	for k, v := range credentialOverrides {
		creds[k] = v
	}
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: creds,
		Extra:       extra,
	}
}

func noAllowlistTestCfg() *config.Config {
	return &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}
}

func cnProviderTestRepo(account *Account) *openAIAccountTestRepo {
	return &openAIAccountTestRepo{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
}

// --- issue #771: CN provider accounts with an explicit api_protocol=chat_completions
// must not be routed to /v1/responses even when the (unrelated) Responses capability
// heuristic would otherwise say yes. ---

func TestAccountTestService_CNProviderChatCompletionsOverridesResponsesHeuristic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newTestContext()

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}

	// Extra explicitly signals Responses support -- without the CN-provider override this
	// would incorrectly route to /v1/responses (issue #771).
	account := cnProviderTestAccount(7701, "chat_completions", map[string]any{openai_compat.ExtraKeyResponsesSupported: true}, nil)
	svc := &AccountTestService{accountRepo: cnProviderTestRepo(account), httpUpstream: upstream, cfg: noAllowlistTestCfg()}

	err := svc.TestAccountConnection(c, account.ID, "deepseek-chat", "hi", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://relay.example.com/v1/chat/completions", upstream.requests[0].URL.String())
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

// --- issue #804 (item 2): CN provider accounts with an explicit api_protocol=responses
// must be routed to /v1/responses even when the Responses capability heuristic has no
// signal at all (the DeepSeek gap). ---

func TestAccountTestService_CNProviderResponsesOverridesChatCompletionsHeuristic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}

	// No Extra flags at all -- without the CN-provider override this defaults to
	// Chat Completions and never reaches the DeepSeek-style native Responses endpoint.
	account := cnProviderTestAccount(7702, "responses", nil, nil)
	svc := &AccountTestService{accountRepo: cnProviderTestRepo(account), httpUpstream: upstream, cfg: noAllowlistTestCfg()}

	err := svc.TestAccountConnection(c, account.ID, "deepseek-reasoner", "", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://relay.example.com/v1/responses", upstream.requests[0].URL.String())
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

// --- issue #804 (item 1): CN provider accounts with an explicit api_protocol=anthropic
// must be routed to a dedicated native Anthropic probe instead of the OpenAI branch or
// the generic Claude tester's ?beta=true / api.anthropic.com fallback. ---

func TestAccountTestService_CNProviderAnthropicProbesNativeEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"message_stop\"}\n\n"))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}

	account := cnProviderTestAccount(7703, "anthropic", nil, map[string]any{
		"api_key":  "sk-anthropic-cn-test",
		"base_url": "https://open.bigmodel.cn/api/anthropic",
	})
	svc := &AccountTestService{accountRepo: cnProviderTestRepo(account), httpUpstream: upstream, cfg: noAllowlistTestCfg()}

	err := svc.TestAccountConnection(c, account.ID, "glm-4.7", "", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	// Native Anthropic path, no ?beta=true (the generic Claude tester appends it).
	require.Equal(t, "https://open.bigmodel.cn/api/anthropic/v1/messages", req.URL.String())
	require.Empty(t, req.URL.RawQuery)
	require.Equal(t, "sk-anthropic-cn-test", req.Header.Get("x-api-key"))
	require.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

func TestAccountTestService_CNProviderAnthropicMissingBaseURLFailsFast(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newTestContext()

	upstream := &queuedHTTPUpstream{}
	account := cnProviderTestAccount(7704, "anthropic", nil, map[string]any{"base_url": ""})
	svc := &AccountTestService{accountRepo: cnProviderTestRepo(account), httpUpstream: upstream, cfg: noAllowlistTestCfg()}

	err := svc.TestAccountConnection(c, account.ID, "glm-4.7", "", "")
	require.Error(t, err)
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), "base_url is not configured")
}

func TestAccountTestService_CNProviderAnthropicRejectsOpenAICompatBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newTestContext()

	upstream := &queuedHTTPUpstream{}
	account := cnProviderTestAccount(7705, "anthropic", nil, map[string]any{
		"base_url": "https://open.bigmodel.cn/api/paas/v4",
	})
	svc := &AccountTestService{accountRepo: cnProviderTestRepo(account), httpUpstream: upstream, cfg: noAllowlistTestCfg()}

	err := svc.TestAccountConnection(c, account.ID, "glm-4.7", "", "")
	require.Error(t, err)
	// Fails fast locally: no upstream request with the wrong endpoint shape.
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), "looks like an OpenAI-compatible endpoint")
}

func TestAccountTestService_CNProviderAnthropic401MarksAccountError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := newTestContext()

	upstream := &queuedHTTPUpstream{responses: []*http.Response{
		newJSONResponse(http.StatusUnauthorized, `{"error":{"message":"invalid key"}}`),
	}}
	account := cnProviderTestAccount(7706, "anthropic", nil, map[string]any{
		"base_url": "https://api.moonshot.cn/anthropic",
	})
	repo := cnProviderTestRepo(account)
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: noAllowlistTestCfg()}

	err := svc.TestAccountConnection(c, account.ID, "kimi-k2.5", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Anthropic endpoint returned 401")
	require.Equal(t, account.ID, repo.setErrorID)
}

func TestAccountTestService_CNProviderAnthropicMissingAPIKeyFailsFast(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newTestContext()

	upstream := &queuedHTTPUpstream{}
	account := cnProviderTestAccount(7707, "anthropic", nil, map[string]any{"api_key": ""})
	svc := &AccountTestService{accountRepo: cnProviderTestRepo(account), httpUpstream: upstream, cfg: noAllowlistTestCfg()}

	err := svc.TestAccountConnection(c, account.ID, "glm-4.7", "", "")
	require.Error(t, err)
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), "No API key available")
}

// --- cnAnthropicBaseURLMisconfigHint direct coverage ---

func TestCNAnthropicBaseURLMisconfigHint(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		wantEmpty bool
	}{
		{name: "native_anthropic_path_no_hint", baseURL: "https://open.bigmodel.cn/api/anthropic", wantEmpty: true},
		{name: "root_path_no_hint", baseURL: "https://relay.example.com", wantEmpty: true},
		{name: "paas_path_flagged", baseURL: "https://open.bigmodel.cn/api/paas/v4", wantEmpty: false},
		{name: "version_suffix_flagged", baseURL: "https://relay.example.com/v1", wantEmpty: false},
		{name: "chat_completions_suffix_flagged", baseURL: "https://relay.example.com/openai/chat/completions", wantEmpty: false},
		{name: "responses_suffix_flagged", baseURL: "https://relay.example.com/openai/responses", wantEmpty: false},
		{name: "invalid_url_no_hint", baseURL: "not a url", wantEmpty: true},
		{name: "empty_url_no_hint", baseURL: "", wantEmpty: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := cnAnthropicBaseURLMisconfigHint(tt.baseURL)
			if tt.wantEmpty {
				require.Empty(t, hint)
			} else {
				require.NotEmpty(t, hint)
				require.Contains(t, hint, "OpenAI-compatible endpoint")
			}
		})
	}
}
