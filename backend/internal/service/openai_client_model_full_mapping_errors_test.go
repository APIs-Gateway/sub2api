//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// client_model_full_mapping 错误路径：上游错误文案里的真实模型名（C）在写给客户端之前换成用户请求的名字（A）；
// ops / 日志 / 返回的 error 里保留上游原文。fullMapA / fullMapC 常量见 openai_client_model_full_mapping_test.go。

func newFullMapErrContext(t *testing.T, path string, withView bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	if withView {
		v, ok := newClientModelView(fullMapA, fullMapC)
		require.True(t, ok)
		setClientModelView(c, v)
	}
	return c, rec
}

func fullMapErrHTTPResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}, "x-request-id": []string{"rid_fullmap_err"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func fullMapErrAccount() *Account {
	return &Account{ID: 7, Name: "free-a6", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
}

// fullMapBindPassthroughRule 绑一条「400 + 关键词 → 透传 body」的规则。
func fullMapBindPassthroughRule(c *gin.Context, keyword string) {
	respCode := http.StatusBadRequest
	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{{
		ID: 1, Name: "pass", Enabled: true, Priority: 1,
		ErrorCodes: []int{http.StatusBadRequest}, Keywords: []string{keyword}, MatchMode: model.MatchModeAll,
		PassthroughCode: false, ResponseCode: &respCode, PassthroughBody: true,
	}})
	BindErrorPassthroughService(c, ruleSvc)
}

const fullMapRuleFailedSSE = `data: {"type":"response.created","response":{"id":"resp_p","model":"deepseek-v4.1-flash","status":"in_progress","output":[]}}

data: {"type":"response.failed","response":{"id":"resp_p","model":"deepseek-v4.1-flash","status":"failed","output":[],"error":{"type":"invalid_request_error","code":"bad_param","message":"deepseek-v4.1-flash: unsupported parameter"}}}

`

const fullMapModelNotFoundBody = `{"error":{"message":"The model deepseek-v4.1-flash does not exist","code":"model_not_found"}}`

func TestClientModelFullMappingErr_ResolveUpstream_ModelNotFound(t *testing.T) {
	c, _ := newFullMapErrContext(t, "/v1/chat/completions", true)

	status, _, msg := ResolveUpstreamErrorResponse(c, PlatformOpenAI, http.StatusBadRequest, []byte(fullMapModelNotFoundBody))

	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, msg, fullMapA)
	require.NotContains(t, msg, fullMapC)
	// ops 里保留上游原文。
	opsMsg, ok := c.Get(OpsUpstreamErrorMessageKey)
	require.True(t, ok)
	require.Contains(t, opsMsg, fullMapC)
}

func TestClientModelFullMappingErr_ResolveUpstream_PassthroughRule(t *testing.T) {
	c, _ := newFullMapErrContext(t, "/v1/chat/completions", true)
	respCode := http.StatusBadRequest
	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{{
		ID: 1, Name: "pass", Enabled: true, Priority: 1,
		ErrorCodes: []int{http.StatusBadRequest}, Keywords: []string{"does not exist"}, MatchMode: model.MatchModeAll,
		PassthroughCode: false, ResponseCode: &respCode, PassthroughBody: true,
	}})
	BindErrorPassthroughService(c, ruleSvc)

	_, errType, msg := ResolveUpstreamErrorResponse(c, PlatformOpenAI, http.StatusBadRequest, []byte(fullMapModelNotFoundBody))

	require.Equal(t, "upstream_error", errType)
	require.Contains(t, msg, fullMapA)
	require.NotContains(t, msg, fullMapC)
}

func TestClientModelFullMappingErr_Responses_ContextWindow(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/responses", true)
	body := `{"error":{"code":"context_length_exceeded","message":"Input exceeds the context window of deepseek-v4.1-flash"}}`
	svc := &OpenAIGatewayService{}

	_, err := svc.handleErrorResponse(context.Background(), fullMapErrHTTPResponse(http.StatusBadRequest, "application/json", body), c, fullMapErrAccount(), nil, fullMapA)

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	msg := gjson.Get(rec.Body.String(), "error.message").String()
	require.Contains(t, msg, "exceeds the context window")
	require.Contains(t, msg, fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Responses_PassthroughRule(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/responses", true)
	respCode := http.StatusUnprocessableEntity
	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{{
		ID: 1, Name: "pass", Enabled: true, Priority: 1,
		ErrorCodes: []int{http.StatusUnprocessableEntity}, Keywords: []string{"unsupported"}, MatchMode: model.MatchModeAll,
		PassthroughCode: false, ResponseCode: &respCode, PassthroughBody: true,
	}})
	BindErrorPassthroughService(c, ruleSvc)
	body := `{"error":{"message":"deepseek-v4.1-flash: unsupported parameter"}}`

	_, err := (&OpenAIGatewayService{}).handleErrorResponse(context.Background(), fullMapErrHTTPResponse(http.StatusUnprocessableEntity, "application/json", body), c, fullMapErrAccount(), nil, fullMapA)

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Responses_Cyber(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/responses", true)
	body := `{"model":"deepseek-v4.1-flash","error":{"code":"cyber_policy","message":"deepseek-v4.1-flash blocked this request"}}`

	_, err := (&OpenAIGatewayService{}).handleErrorResponse(context.Background(), fullMapErrHTTPResponse(http.StatusBadRequest, "application/json", body), c, fullMapErrAccount(), nil, fullMapA)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, fullMapA, gjson.Get(rec.Body.String(), "model").String())
	require.Equal(t, fullMapA+" blocked this request", gjson.Get(rec.Body.String(), "error.message").String())
	require.Equal(t, "cyber_policy", gjson.Get(rec.Body.String(), "error.code").String())
	require.NotContains(t, rec.Body.String(), fullMapC)
	// 风控标记里保留上游原文。
	mark := GetOpsCyberPolicy(c)
	require.NotNil(t, mark)
	require.Contains(t, mark.Body, fullMapC)
}

func TestClientModelFullMappingErr_Responses_Cyber_NoViewUnchanged(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/responses", false)
	body := `{"model":"deepseek-v4.1-flash","error":{"code":"cyber_policy","message":"deepseek-v4.1-flash blocked this request"}}`

	_, err := (&OpenAIGatewayService{}).handleErrorResponse(context.Background(), fullMapErrHTTPResponse(http.StatusBadRequest, "application/json", body), c, fullMapErrAccount(), nil, fullMapA)

	require.Error(t, err)
	require.Equal(t, body, rec.Body.String(), "gate off: upstream body must reach the client byte for byte")
}

func TestClientModelFullMappingErr_Compat_ContextWindow(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/messages", true)
	body := `{"error":{"code":"context_length_exceeded","message":"Input exceeds the context window of deepseek-v4.1-flash"}}`

	// publicWriter 为 nil：上游 context-window 文案原样写出的那条路径。
	_, err := (&OpenAIGatewayService{}).handleCompatErrorResponse(fullMapErrHTTPResponse(http.StatusBadRequest, "application/json", body), c, fullMapErrAccount(), writeAnthropicError, fullMapA)

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC)
	msg := gjson.Get(rec.Body.String(), "error.message").String()
	require.Contains(t, msg, fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Compat_Cyber(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/messages", true)
	body := `{"error":{"code":"cyber_policy","message":"deepseek-v4.1-flash blocked this request"}}`

	_, err := (&OpenAIGatewayService{}).handleCompatErrorResponse(fullMapErrHTTPResponse(http.StatusBadRequest, "application/json", body), c, fullMapErrAccount(), writeAnthropicError, fullMapA)

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC)
	require.Equal(t, fullMapA+" blocked this request", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestClientModelFullMappingErr_PassthroughAccount_ContextWindow(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/chat/completions", true)
	body := `{"error":{"code":"context_length_exceeded","message":"Input exceeds the context window of deepseek-v4.1-flash"}}`

	err := (&OpenAIGatewayService{}).handleErrorResponsePassthrough(context.Background(), fullMapErrHTTPResponse(http.StatusBadRequest, "application/json", body), c, fullMapErrAccount(), nil, []byte(body))

	require.Error(t, err)
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_NonStreamingProtocolError(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/responses", true)
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", "")

	err := (&OpenAIGatewayService{}).writeOpenAINonStreamingProtocolError(resp, c, "deepseek-v4.1-flash returned garbage")

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	require.Equal(t, fullMapA+" returned garbage", gjson.Get(rec.Body.String(), "error.message").String())
	opsMsg, ok := c.Get(OpsUpstreamErrorMessageKey)
	require.True(t, ok)
	require.Contains(t, opsMsg, fullMapC)
}

const fullMapChatFailedStreamBody = `data: {"type":"response.created","response":{"id":"resp_f","model":"deepseek-v4.1-flash","status":"in_progress","output":[]}}

event: response.failed
data: {"type":"response.failed","response":{"id":"resp_f","object":"response","model":"deepseek-v4.1-flash","status":"failed","output":[],"error":{"code":"upstream_error","message":"input exceeds the context window of deepseek-v4.1-flash"}}}

`

func TestClientModelFullMappingErr_Chat_StreamFailure(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/chat/completions", true)
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", fullMapChatFailedStreamBody)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleChatStreamingResponse(resp, c, fullMapErrAccount(), fullMapA, fullMapA, fullMapC, time.Now(), 0)

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	msg := gjson.Get(rec.Body.String(), "error.message").String()
	require.Contains(t, msg, "exceeds the context window")
	require.Contains(t, msg, fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Chat_BufferedFailure(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/chat/completions", true)
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", fullMapChatFailedStreamBody)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleChatBufferedStreamingResponse(resp, c, fullMapErrAccount(), fullMapA, fullMapA, fullMapC, time.Now())

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC)
	msg := gjson.Get(rec.Body.String(), "error.message").String()
	require.Contains(t, msg, fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

const fullMapCyberFailedStreamBody = `data: {"type":"response.created","response":{"id":"resp_c","model":"deepseek-v4.1-flash","status":"in_progress","output":[]}}

data: {"type":"response.failed","response":{"id":"resp_c","object":"response","model":"deepseek-v4.1-flash","status":"failed","output":[],"error":{"code":"cyber_policy","message":"deepseek-v4.1-flash blocked this request"}}}

`

func TestClientModelFullMappingErr_Messages_Cyber(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/messages", true)
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", fullMapCyberFailedStreamBody)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, fullMapErrAccount(), fullMapA, fullMapA, fullMapC, time.Now())

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	require.Equal(t, fullMapA+" blocked this request", gjson.Get(rec.Body.String(), "error.message").String())
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Messages_Cyber_Stream(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/messages", true)
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", fullMapCyberFailedStreamBody)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, _ = svc.handleAnthropicStreamingResponse(resp, c, fullMapErrAccount(), fullMapA, fullMapA, fullMapC, time.Now())

	require.NotNil(t, GetOpsCyberPolicy(c))
	require.Contains(t, rec.Body.String(), "event: error")
	require.Contains(t, rec.Body.String(), fullMapA+" blocked this request")
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Messages_PassthroughRule_Stream(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/messages", true)
	fullMapBindPassthroughRule(c, "unsupported")
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", fullMapRuleFailedSSE)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleAnthropicStreamingResponse(resp, c, fullMapErrAccount(), fullMapA, fullMapA, fullMapC, time.Now())

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	require.Contains(t, rec.Body.String(), fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Chat_PassthroughRule_Stream(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/chat/completions", true)
	fullMapBindPassthroughRule(c, "unsupported")
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", fullMapRuleFailedSSE)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleChatStreamingResponse(resp, c, fullMapErrAccount(), fullMapA, fullMapA, fullMapC, time.Now(), 0)

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Chat_PassthroughRule_Buffered(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/chat/completions", true)
	fullMapBindPassthroughRule(c, "context window")
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", fullMapChatFailedStreamBody)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleChatBufferedStreamingResponse(resp, c, fullMapErrAccount(), fullMapA, fullMapA, fullMapC, time.Now())

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_Responses_PassthroughRule_Stream(t *testing.T) {
	c, rec := newFullMapErrContext(t, "/v1/responses", true)
	fullMapBindPassthroughRule(c, "unsupported")
	resp := fullMapErrHTTPResponse(http.StatusOK, "text/event-stream", fullMapRuleFailedSSE)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleStreamingResponse(context.Background(), resp, c, fullMapErrAccount(), time.Now(), fullMapA, fullMapC)

	require.Error(t, err)
	require.Contains(t, err.Error(), fullMapC, "returned error (logs) keeps the upstream text")
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), fullMapA)
	require.NotContains(t, rec.Body.String(), fullMapC)
}

func TestClientModelFullMappingErr_NoView_Unchanged(t *testing.T) {
	c, _ := newFullMapErrContext(t, "/v1/chat/completions", false)

	_, _, msg := ResolveUpstreamErrorResponse(c, PlatformOpenAI, http.StatusBadRequest, []byte(fullMapModelNotFoundBody))

	require.Equal(t, "The model "+fullMapC+" does not exist", msg)
}
