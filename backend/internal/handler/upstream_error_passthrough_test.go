package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 所有 failover-exhausted 入口走共享 ResolveUpstreamErrorResponse:
// 上游请求形 4xx(422)保留真实状态码,但默认返回安全文案。本文件覆盖各薄入口的 reroute 行。

func newPassthroughTestCtx() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c, rec
}

func failover422() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:   http.StatusUnprocessableEntity,
		ResponseBody: []byte(`{"error":{"message":"bad request foo"}}`),
	}
}

func TestHandleFailoverExhausted_KeepsUpstream4xxStatus(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	(&GatewayHandler{}).handleFailoverExhausted(c, failover422(), service.PlatformAnthropic, false)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestHandleCCFailoverExhausted_KeepsUpstream4xxStatusWithSafeMessage(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	(&GatewayHandler{}).handleCCFailoverExhausted(c, failover422(), false)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "invalid_request_error", errField["type"])
	assert.Equal(t, "Invalid request parameters, please check the request body and try again", errField["message"])
}

func TestHandleCCFailoverExhausted_NilErrKeepsExhaustedMessage(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	(&GatewayHandler{}).handleCCFailoverExhausted(c, nil, false)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandleResponsesFailoverExhausted_KeepsUpstream4xxStatus(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	(&GatewayHandler{}).handleResponsesFailoverExhausted(c, failover422(), false)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestHandleGeminiFailoverExhausted_KeepsUpstream4xxStatus(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	(&GatewayHandler{}).handleGeminiFailoverExhausted(c, failover422())
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestOpenAIHandleFailoverExhausted_KeepsUpstream4xxStatus(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, failover422(), false)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestOpenAIHandleFailoverExhausted_KeepsBodyLimitStatusAndSafeMessage(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	failover := &service.UpstreamFailoverError{
		StatusCode:   http.StatusRequestEntityTooLarge,
		ResponseBody: []byte(`{"error":{"message":"private upstream body-limit detail"}}`),
	}

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, failover, false)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "Request body is too large")
	assert.NotContains(t, rec.Body.String(), "private upstream body-limit detail")
}

func TestHandleAnthropicFailoverExhausted_KeepsUpstream4xxStatus(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, failover422(), false)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// 5xx 仍保留 502 语义(非透传),确保默认矩阵未被破坏。
func TestHandleFailoverExhausted_Keeps5xxAs502(t *testing.T) {
	c, rec := newPassthroughTestCtx()
	failover := &service.UpstreamFailoverError{
		StatusCode:   http.StatusServiceUnavailable,
		ResponseBody: []byte(`{"error":{"message":"upstream down"}}`),
	}
	(&GatewayHandler{}).handleFailoverExhausted(c, failover, service.PlatformAnthropic, false)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
