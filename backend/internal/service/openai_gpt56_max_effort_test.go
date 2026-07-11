package service

import (
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

func TestNormalizeOpenAIReasoningEffortForGPT56(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		model string
		want  string
	}{
		{name: "Sol preserves max", raw: "max", model: "gpt-5.6-sol", want: "max"},
		{name: "Terra alias preserves max", raw: "max", model: "openai/gpt-5.6-terra", want: "max"},
		{name: "Luna suffix preserves max", raw: "max", model: "gpt-5.6-luna-2026-07-10", want: "max"},
		{name: "other models still normalize to xhigh", raw: "max", model: "gpt-5.4", want: "xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeOpenAIReasoningEffortForModel(tt.raw, tt.model))
		})
	}
}

func TestExtractOpenAIReasoningEffortModelCandidates(t *testing.T) {
	bodyWithoutEffort := []byte(`{"model":"gpt-5.6-sol-max","input":"hello"}`)
	bodyWithMax := []byte(`{"model":"sol","reasoning":{"effort":"max"},"input":"hello"}`)

	tests := []struct {
		name       string
		body       []byte
		candidates []string
		want       string
	}{
		{
			name:       "original suffix survives OAuth normalization",
			body:       bodyWithoutEffort,
			candidates: []string{"gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol-max"},
			want:       "max",
		},
		{
			name:       "explicit max uses mapped GPT-5.6 model",
			body:       bodyWithMax,
			candidates: []string{"gpt-5.6-sol", "sol"},
			want:       "max",
		},
		{
			name:       "non GPT-5.6 explicit max remains xhigh",
			body:       bodyWithMax,
			candidates: []string{"gpt-5.4", "sol"},
			want:       "xhigh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOpenAIReasoningEffortFromBody(tt.body, tt.candidates...)
			require.NotNil(t, got)
			require.Equal(t, tt.want, *got)
		})
	}

	got := extractOpenAIReasoningEffort(
		map[string]any{"model": "gpt-5.6-sol-max", "input": "hello"},
		"gpt-5.6-sol",
		"gpt-5.6-sol-max",
	)
	require.NotNil(t, got)
	require.Equal(t, "max", *got)
}

func TestNormalizeOpenAICodexCompactReasoningEffortForAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","input":"compact me","reasoning":{"effort":"max","summary":"auto"}}`)

	tests := []struct {
		name    string
		path    string
		account *Account
		changed bool
		want    string
	}{
		{
			name:    "OpenAI OAuth compact downgrades max",
			path:    "/openai/v1/responses/compact",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			changed: true,
			want:    "xhigh",
		},
		{
			name:    "OpenAI OAuth regular Responses preserves max",
			path:    "/openai/v1/responses",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			want:    "max",
		},
		{
			name:    "OpenAI API key compact preserves max",
			path:    "/openai/v1/responses/compact",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			want:    "max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, nil)

			normalized, changed, err := normalizeOpenAICodexCompactReasoningEffortForAccount(c, tt.account, body)

			require.NoError(t, err)
			require.Equal(t, tt.changed, changed)
			require.Equal(t, tt.want, gjson.GetBytes(normalized, "reasoning.effort").String())
			require.Equal(t, "auto", gjson.GetBytes(normalized, "reasoning.summary").String())
		})
	}
}

func TestOpenAIGatewayServiceForwardOAuthGPT56EffortCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name                string
		path                string
		body                []byte
		wantUpstreamModel   string
		wantUpstreamEffort  string
		wantReasoningEffort string
	}{
		{
			name:                "OAuth normalization keeps suffix-derived max in usage metadata",
			path:                "/openai/v1/responses",
			body:                []byte(`{"model":"gpt-5.6-sol-max","instructions":"suffix-test","input":"hello","stream":false}`),
			wantUpstreamModel:   "gpt-5.6-sol",
			wantReasoningEffort: "max",
		},
		{
			name:                "OAuth compact sends xhigh while preserving regular GPT-5.6 support",
			path:                "/openai/v1/responses/compact",
			body:                []byte(`{"model":"gpt-5.6-sol","instructions":"compact-test","input":"hello","reasoning":{"effort":"max"}}`),
			wantUpstreamModel:   "gpt-5.6-sol",
			wantUpstreamEffort:  "xhigh",
			wantReasoningEffort: "xhigh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{
				resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"usage":{"input_tokens":1,"output_tokens":2}}`)),
				},
			}
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
			account := &Account{
				ID:          56,
				Name:        "openai-oauth-gpt56",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Concurrency: 1,
				Credentials: map[string]any{
					"access_token":       "oauth-token",
					"chatgpt_account_id": "chatgpt-acc",
				},
				Status:      StatusActive,
				Schedulable: true,
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

			result, err := svc.Forward(context.Background(), c, account, tt.body)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tt.wantUpstreamModel, gjson.GetBytes(upstream.lastBody, "model").String())
			if tt.wantUpstreamEffort != "" {
				require.Equal(t, tt.wantUpstreamEffort, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
			}
			require.NotNil(t, result.ReasoningEffort)
			require.Equal(t, tt.wantReasoningEffort, *result.ReasoningEffort)
		})
	}
}
