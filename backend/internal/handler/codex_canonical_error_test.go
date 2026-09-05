package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Codex 端点上，上游错误必须被换成 Codex CLI 能用自己官方文案渲染的形态，
// 上游（常见是中文的）原始报错一个字都不能出现在响应里。

const upstreamChineseErrorBody = `{"error":{"message":"上游服务暂时不可用。 原因：上游服务、网络链路或代理返回异常响应。 request_id: 20260905064328784"}}`

func newCodexTestCtx(path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, rec
}

func TestOpenAIHandleFailoverExhausted_ResponsesRouteUsesCodexCanonicalError(t *testing.T) {
	t.Run("上游 401 必须改写，避免 Codex 去刷新用户自己的 token", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/responses")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:   http.StatusUnauthorized,
			ResponseBody: []byte(upstreamChineseErrorBody),
		}, false)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, "server_is_overloaded", gjson.Get(rec.Body.String(), "error.code").String())
		assert.NotContains(t, rec.Body.String(), "上游服务暂时不可用")
		assert.NotContains(t, rec.Body.String(), "request_id")
	})

	t.Run("上游 404 保留状态码但不泄露上游文案", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/responses")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:   http.StatusNotFound,
			ResponseBody: []byte(upstreamChineseErrorBody),
		}, false)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "Unknown error", gjson.Get(rec.Body.String(), "error.message").String())
		assert.NotContains(t, rec.Body.String(), "上游服务暂时不可用")
		assert.NotContains(t, rec.Body.String(), "request_id")
	})

	t.Run("上游 503 用 server_is_overloaded 换取 Codex 官方文案", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/responses")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:   http.StatusServiceUnavailable,
			ResponseBody: []byte(upstreamChineseErrorBody),
		}, false)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, "server_is_overloaded", gjson.Get(rec.Body.String(), "error.code").String())
		assert.NotContains(t, rec.Body.String(), "上游服务暂时不可用")
	})

	t.Run("codex direct 路由同样生效", func(t *testing.T) {
		c, rec := newCodexTestCtx("/backend-api/codex/responses")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:   http.StatusBadGateway,
			ResponseBody: []byte(upstreamChineseErrorBody),
		}, false)

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.NotContains(t, rec.Body.String(), "上游服务暂时不可用")
	})

	t.Run("非 Responses 路由保持既有行为", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:   http.StatusServiceUnavailable,
			ResponseBody: []byte(upstreamChineseErrorBody),
		}, false)

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Equal(t, "Upstream service temporarily unavailable", gjson.Get(rec.Body.String(), "error.message").String())
	})
}

func TestOpenAIHandleFailoverExhausted_ResponsesStreamStartedEmitsCanonicalFailedEvent(t *testing.T) {
	c, rec := newCodexTestCtx("/v1/responses")
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: []byte(upstreamChineseErrorBody),
	}, true)

	body := rec.Body.String()
	require.Contains(t, body, "event: response.failed")
	assert.NotContains(t, body, "上游服务暂时不可用")

	data := body[len("event: response.failed\ndata: "):]
	assert.Equal(t, "response.failed", gjson.Get(data, "type").String())
	assert.Equal(t, service.CodexErrCodeServerOverloaded, gjson.Get(data, "response.error.code").String())
	assert.Empty(t, gjson.Get(data, "response.error.message").String())
}

func TestOpenAIHandleFailoverExhausted_ResponsesKeepsGatewayOwnedMessages(t *testing.T) {
	// 网关自身的请求体过大限制不是上游错误，文案对用户有用，不能被换成 "Unknown error"。
	c, rec := newCodexTestCtx("/v1/responses")
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusRequestEntityTooLarge,
		ResponseBody: []byte(upstreamChineseErrorBody),
	}, false)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), service.OpenAIRequestBodyTooLargeClientMessage)
}

// 归一化只针对上游错误。网关自己的 401（API key 无效）文案对用户是有用信息，
// 即使在 Codex 端点上也必须原样保留。
func TestOpenAIErrorResponse_ResponsesRouteKeepsGatewayOwnAuthError(t *testing.T) {
	c, rec := newCodexTestCtx("/v1/responses")
	(&OpenAIGatewayHandler{}).errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "authentication_error", gjson.Get(rec.Body.String(), "error.type").String())
	assert.Equal(t, "Invalid API key", gjson.Get(rec.Body.String(), "error.message").String())
}

// 同一路由上，只要没有上游错误被记录，流内错误也保持网关自己的文案。
func TestOpenAIHandleStreamingAwareError_ResponsesRouteWithoutUpstreamKeepsMessage(t *testing.T) {
	c, rec := newCodexTestCtx("/v1/responses")
	(&OpenAIGatewayHandler{}).handleStreamingAwareError(
		c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", false,
	)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "Too many pending requests, please retry later", gjson.Get(rec.Body.String(), "error.message").String())
}
