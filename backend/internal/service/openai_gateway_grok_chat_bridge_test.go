//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGrokChatResponsesBridgeEligibilityKeepsOnlyLosslessRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "text with function tool",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"auto"}`,
			want: true,
		},
		{
			name: "image content",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`,
			want: true,
		},
		{
			name: "stop stays native",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":"hello"}],"stop":["END"]}`,
			want: false,
		},
		{
			name: "unknown content stays native",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AA=="}}]}]}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eligible, reason := grokChatResponsesBridgeEligibility([]byte(tt.body))
			require.Equal(t, tt.want, eligible, "reason=%s", reason)
		})
	}
}

func TestForwardGrokChatCompletionsUsesResponsesBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-4.3","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"grok-chat-responses"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_grok_chat","model":"grok-4.3","status":"in_progress","output":[]}}`,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.completed","response":{"id":"resp_grok_chat","object":"response","model":"grok-4.3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`,
			`data: [DONE]`,
		}, "\n\n"))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := grokProtocolOAuthAccount(7201)

	result, err := svc.forwardGrokChatCompletions(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, xai.DefaultBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer oauth-protocol-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "grok-4.3", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	require.Contains(t, recorder.Body.String(), `"chat.completion"`)
	require.Equal(t, int64(6), gjson.GetBytes(recorder.Body.Bytes(), "usage.total_tokens").Int())
}

func TestForwardGrokChatCompletionsUsesNativeChatForUnsupportedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-4.3","stream":false,"stop":["END"],"prompt_cache_key":"internal-only","messages":[{"role":"user","content":"hello"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	upstreamBody := `{"id":"chatcmpl_grok","object":"chat.completion","model":"grok-4.3","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"grok-chat-native"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := grokProtocolAPIKeyAccount(7202)

	result, err := svc.forwardGrokChatCompletions(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, xai.DefaultBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-protocol-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "END", gjson.GetBytes(upstream.lastBody, "stop.0").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.JSONEq(t, upstreamBody, recorder.Body.String())
}

func TestBuildGrokChatCompletionsURLUsesAccountBaseURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://xai.test/v1/chat/completions", xai.BuildChatCompletionsURL("https://xai.test/v1/"))
}
