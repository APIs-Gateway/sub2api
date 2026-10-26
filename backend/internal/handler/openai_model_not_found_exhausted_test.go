package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 400 model_not_found 切号用尽：保留 400（不折叠成 502），带结构化 code，
// 对外文案是脱敏后的上游原文，ops 记录真实上游状态码。
func TestOpenAIHandleFailoverExhausted_ModelNotFound400KeepsStructured400(t *testing.T) {
	c, rec := newCodexTestCtx("/v1/chat/completions")
	body := []byte(`{"error":{"type":"invalid_request_error","code":"model_not_found","param":"model","message":"Model not found; inspect https://upstream.example/debug?access_token=super-secret-value&model=x"}}`)

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: body,
	}, false)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "model_not_found", gjson.Get(rec.Body.String(), "error.code").String())
	msg := gjson.Get(rec.Body.String(), "error.message").String()
	require.Contains(t, msg, "Model not found")
	require.Contains(t, msg, "access_token=***")
	require.NotContains(t, rec.Body.String(), "super-secret-value")
	status, ok := c.Get(service.OpsUpstreamStatusCodeKey)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, status)
}

// 对照：普通参数 400 保持既有固定安全文案，且不带 model_not_found code。
func TestOpenAIHandleFailoverExhausted_OtherBadRequestUnchanged(t *testing.T) {
	c, rec := newCodexTestCtx("/v1/chat/completions")

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(`{"error":{"code":"invalid_request_error","message":"internal upstream detail"}}`),
	}, false)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, gjson.Get(rec.Body.String(), "error.code").Exists())
	require.NotContains(t, rec.Body.String(), "internal upstream detail")
}

// Codex/Responses 端点仍走 Codex 归一化，状态码保留 400，上游原文与敏感参数不外泄。
func TestOpenAIHandleFailoverExhausted_ModelNotFound400ResponsesRouteStaysCanonical(t *testing.T) {
	c, rec := newCodexTestCtx("/v1/responses")

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(`{"error":{"code":"model_not_found","message":"model not found ?access_token=super-secret-value"}}`),
	}, false)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NotContains(t, rec.Body.String(), "super-secret-value")
}

// Anthropic 格式入口（/v1/messages 走 OpenAI 账号）同样保留 400 与脱敏文案。
func TestOpenAIHandleAnthropicFailoverExhausted_ModelNotFound400(t *testing.T) {
	c, rec := newCodexTestCtx("/v1/messages")

	(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(`{"error":{"code":"model_not_found","message":"Unknown provider for model claude-x"}}`),
	}, false)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "Unknown provider for model claude-x", gjson.Get(rec.Body.String(), "error.message").String())
}
