//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestEnsureDeepSeekChatReasoningPlaceholders_ZenHostAndFinalModel(t *testing.T) {
	missing := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	got := ensureDeepSeekChatReasoningPlaceholders("https://opencode.ai/zen/v1/chat/completions", missing)
	require.Equal(t, " ", gjson.GetBytes(got, "messages.1.reasoning_content").String())
	require.False(t, gjson.GetBytes(got, "messages.0.reasoning_content").Exists())
	require.False(t, gjson.GetBytes(got, "messages.2.reasoning_content").Exists())

	for _, target := range []string{"https://opencode.ai.evil.test/zen/v1/chat/completions", "https://elsewhere.example/v1/chat/completions", "not-a-url", "%"} {
		require.Equal(t, string(missing), string(ensureDeepSeekChatReasoningPlaceholders(target, missing)), target)
	}
	nonDeepSeek := bytes.Replace(missing, []byte("deepseek-v4-flash"), []byte("glm-5.3"), 1)
	require.Equal(t, string(nonDeepSeek), string(ensureDeepSeekChatReasoningPlaceholders("https://opencode.ai/zen/v1/chat/completions", nonDeepSeek)))
	nonArray := []byte(`{"model":"deepseek-v4-flash","messages":{"role":"assistant"}}`)
	require.Equal(t, string(nonArray), string(ensureDeepSeekChatReasoningPlaceholders("https://opencode.ai/zen/v1/chat/completions", nonArray)))

	aliased := bytes.Replace(missing, []byte("deepseek-v4-flash"), []byte("opencode-go/DeepSeek-V4-Pro"), 1)
	require.Equal(t, " ", gjson.GetBytes(ensureDeepSeekChatReasoningPlaceholders("https://opencode.ai/zen/v1/chat/completions", aliased), "messages.1.reasoning_content").String())
	require.Equal(t, " ", gjson.GetBytes(ensureDeepSeekChatReasoningPlaceholders("https://api.deepseek.com/v1/chat/completions", missing), "messages.1.reasoning_content").String())

	withPlain := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"assistant","reasoning_content":"actual thought","content":"done"}]}`)
	require.Equal(t, string(withPlain), string(ensureDeepSeekChatReasoningPlaceholders("https://opencode.ai/zen/v1/chat/completions", withPlain)))
}

func TestForwardAsRawChatCompletions_ZenDeepSeekReasoningPlaceholderUsesMappedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"weather"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"cloudy"},{"role":"assistant","reasoning_content":"real thought","content":"done"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"id":"chatcmpl_zen","object":"chat.completion","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`))),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := rawChatCompletionsTestAccount()
	account.Credentials["base_url"] = "https://opencode.ai/zen/v1"
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "deepseek-v4-flash"}

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.4", result.Model)
	require.Equal(t, "deepseek-v4-flash", result.UpstreamModel)
	require.Equal(t, "deepseek-v4-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, " ", gjson.GetBytes(upstream.lastBody, "messages.1.reasoning_content").String())
	require.Equal(t, "real thought", gjson.GetBytes(upstream.lastBody, "messages.3.reasoning_content").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages.0.reasoning_content").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages.2.reasoning_content").Exists())
}

func TestForwardResponses_ChatFallbackZenDeepSeekReasoningPlaceholder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":[{"type":"reasoning","id":"item_missing","summary":[],"encrypted_content":"opaque"},{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"cloudy"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"id":"chatcmpl_zen_fallback","object":"chat.completion","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`))),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := forceChatResponsesFallbackAccount()
	account.Credentials["base_url"] = "https://opencode.ai/zen/v1"
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "deepseek-v4-flash"}

	result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.4", result.Model)
	require.Equal(t, "deepseek-v4-flash", result.UpstreamModel)
	require.Equal(t, "deepseek-v4-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, " ", gjson.GetBytes(upstream.lastBody, `messages.#(role=="assistant").reasoning_content`).String())
	require.False(t, gjson.GetBytes(upstream.lastBody, `messages.#(role=="tool").reasoning_content`).Exists())
}
