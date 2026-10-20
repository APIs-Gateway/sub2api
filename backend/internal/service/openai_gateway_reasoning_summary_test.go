package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeneratedResponsesReasoningSummaryFollowsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		path        string
		body        string
		account     *Account
		wantEffort  string
		wantSummary bool
	}{
		{
			name: "chat bridge to strict third party",
			path: "/v1/chat/completions",
			body: `{"model":"deepseek-v4.1-flash","reasoning_effort":"low","messages":[{"role":"user","content":"hello"}],"stream":false}`,
			account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.commandcode.ai/provider/v1"},
				Extra:       map[string]any{"openai_responses_supported": true}},
			wantEffort: "low",
		},
		{
			name: "anthropic bridge to strict third party",
			path: "/v1/messages",
			body: `{"model":"deepseek-v4.1-flash","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`,
			account: &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.commandcode.ai/provider/v1"},
				Extra:       map[string]any{"openai_responses_supported": true}},
			wantEffort: "medium",
		},
		{
			name: "official API key retains summary",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5.4","reasoning_effort":"high","messages":[{"role":"user","content":"hello"}],"stream":false}`,
			account: &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra:       map[string]any{"openai_responses_supported": true}},
			wantEffort: "high", wantSummary: true,
		},
		{
			name: "codex OAuth retains summary",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5.4","reasoning_effort":"high","messages":[{"role":"user","content":"hello"}],"stream":false}`,
			account: &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
				Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"}},
			wantEffort: "high", wantSummary: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop after body capture"}}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			var err error
			if tt.path == "/v1/messages" {
				_, err = svc.ForwardAsAnthropic(context.Background(), c, tt.account, body, "", "")
			} else {
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, tt.account, body, "", "")
			}
			require.Error(t, err)
			require.NotNil(t, upstream.lastReq, "request must reach the upstream")
			require.Equal(t, tt.wantEffort, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
			summary := gjson.GetBytes(upstream.lastBody, "reasoning.summary")
			if tt.wantSummary {
				require.Equal(t, "auto", summary.String())
			} else {
				require.False(t, summary.Exists())
			}
		})
	}
}

func TestReasoningSummaryOfficialAccountDecision(t *testing.T) {
	require.False(t, shouldRequestResponsesReasoningSummary(nil))
	require.True(t, shouldRequestResponsesReasoningSummary(&Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}))
	require.True(t, shouldRequestResponsesReasoningSummary(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.openai.com/v1"}}))
	require.False(t, shouldRequestResponsesReasoningSummary(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.commandcode.ai/provider/v1"}}))
}
