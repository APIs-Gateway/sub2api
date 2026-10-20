//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 覆盖 Task 5：Chat Completions 入站（Responses SSE 上游 / raw Chat 上游）、Anthropic 入站、
// Responses 入站走 raw chat 降级、以及 Grok Responses 路径的「上游返回模型 != 发送模型」拦截。
// 每条路径各一个「不一致 → UpstreamFailoverError、客户端零字节」用例；每种上游协议至少一个「一致 → 放行」用例。

const (
	upstreamModelMismatchSentModel = "gpt-5.6-sol"
	upstreamModelMismatchGotModel  = "gpt-6-sol"
)

func newUpstreamModelMismatchPathContext(t *testing.T, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	c.Request = httptest.NewRequest(http.MethodPost, path, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func upstreamModelMismatchHTTPResponse(contentType, requestID, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{contentType},
			"x-request-id": []string{requestID},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// Responses SSE 上游：response.created 就带 model，随后有一段可见 delta 和终止事件。
func upstreamModelMismatchResponsesSSE(model string) string {
	return strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_mm","object":"response","model":"` + model + `","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_mm","role":"assistant","status":"in_progress","content":[]}}`,
		"",
		`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_mm","part":{"type":"output_text","text":""}}`,
		"",
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_mm","delta":"leak"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_mm","object":"response","model":"` + model + `","status":"completed","output":[{"type":"message","id":"msg_mm","role":"assistant","status":"completed","content":[{"type":"output_text","text":"leak"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`,
		"",
	}, "\n")
}

// Chat Completions SSE 上游：首个 chunk 顶层就带 model。
func upstreamModelMismatchChatSSE(model string) string {
	return strings.Join([]string{
		`data: {"id":"chatcmpl_mm","object":"chat.completion.chunk","model":"` + model + `","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_mm","object":"chat.completion.chunk","model":"` + model + `","choices":[{"index":0,"delta":{"content":"leak"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_mm","object":"chat.completion.chunk","model":"` + model + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"chatcmpl_mm","object":"chat.completion.chunk","model":"` + model + `","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
}

// Chat Completions 非流式 JSON 上游。
func upstreamModelMismatchChatJSON(model string) string {
	return `{"id":"chatcmpl_mm","object":"chat.completion","model":"` + model + `","choices":[{"index":0,"message":{"role":"assistant","content":"leak"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`
}

func requireUpstreamModelMismatchPathFailover(t *testing.T, c *gin.Context, rec *httptest.ResponseRecorder, err error, sent, got string, stream bool) {
	t.Helper()
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "expected UpstreamFailoverError, got %v", err)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "upstream_model_mismatch")
	require.False(t, c.Writer.Written(), "mismatch must be caught before any client output")
	require.Empty(t, rec.Body.String(), "no upstream bytes may leak to the client")
	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, sent, mark.SentModel)
	require.Equal(t, got, mark.ResponseModel)
	require.Equal(t, stream, mark.Stream)
}

func upstreamModelMismatchTestAccount() *Account {
	return &Account{ID: 1, Name: "openai-oauth", Platform: PlatformOpenAI}
}

// ---------- openai_gateway_chat_completions.go ----------

func TestUpstreamModelMismatch_HandleChatStreamingResponseFailsOver(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_chat_stream", upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleChatStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now(), 0)

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, upstreamModelMismatchSentModel, upstreamModelMismatchGotModel, true)
}

func TestUpstreamModelMismatch_HandleChatStreamingResponseMatchPasses(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_chat_stream_ok", upstreamModelMismatchResponsesSSE(upstreamModelMismatchSentModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleChatStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now(), 0)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"content":"leak"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// 账号级 model_mapping：A 已是映射后的 luna，上游返回 luna → 放行。
func TestUpstreamModelMismatch_HandleChatStreamingResponseMappedModelIsExempt(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_chat_stream_mapped", upstreamModelMismatchResponsesSSE("gpt-5.6-luna"))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleChatStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, "gpt-5.6-luna", "gpt-5.6-luna", time.Now(), 0)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"content":"leak"`)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// model 只在 response.completed 才出现且客户端已收到输出 → 不拦截，仅打标。
func TestUpstreamModelMismatch_HandleChatStreamingResponseLateModelOnlyMarks(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_late","object":"response","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_late","role":"assistant","status":"in_progress","content":[]}}`,
		"",
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_late","delta":"hello"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_late","object":"response","model":"` + upstreamModelMismatchGotModel + `","status":"completed","output":[{"type":"message","id":"msg_late","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`,
		"",
	}, "\n")
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_chat_stream_late", upstreamBody)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleChatStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now(), 0)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"content":"hello"`)
	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, upstreamModelMismatchGotModel, mark.ResponseModel)
}

func TestUpstreamModelMismatch_HandleChatBufferedStreamingResponseFailsOver(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_chat_buffered", upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleChatBufferedStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, upstreamModelMismatchSentModel, upstreamModelMismatchGotModel, false)
	require.Equal(t, 5, GetOpsUpstreamModelMismatch(c).Usage.InputTokens, "buffered path must carry parsed usage into the mark")
}

func TestUpstreamModelMismatch_HandleChatBufferedStreamingResponseMatchPasses(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_chat_buffered_ok", upstreamModelMismatchResponsesSSE(upstreamModelMismatchSentModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleChatBufferedStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "leak", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// ---------- openai_gateway_chat_completions_raw.go ----------

func TestUpstreamModelMismatch_StreamRawChatCompletionsFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_raw_stream", upstreamModelMismatchChatSSE(upstreamModelMismatchGotModel))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), body, "")

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, "gpt-5.5", upstreamModelMismatchGotModel, true)
}

func TestUpstreamModelMismatch_StreamRawChatCompletionsMatchPasses(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_raw_stream_ok", upstreamModelMismatchChatSSE("gpt-5.5"))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"content":"leak"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// 中转站先发 ": OPENROUTER PROCESSING" 注释行再发首个 chunk：注释行必须暂存到首块过完模型比对，
// 否则响应头已写出、clientOutputStarted 置位，拦截退化为观察模式。
func TestUpstreamModelMismatch_StreamRawChatCompletionsCommentPreambleFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", body)
	upstreamBody := ": OPENROUTER PROCESSING\n\n" + upstreamModelMismatchChatSSE(upstreamModelMismatchGotModel)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_raw_stream_comment", upstreamBody)}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), body, "")

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, "gpt-5.5", upstreamModelMismatchGotModel, true)
}

// 同样的注释行前导，模型一致 → 注释行与首个 chunk 按原顺序都到客户端
func TestUpstreamModelMismatch_StreamRawChatCompletionsCommentPreambleMatchPasses(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", body)
	upstreamBody := ": OPENROUTER PROCESSING\n\n" + upstreamModelMismatchChatSSE("gpt-5.5")
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_raw_stream_comment_ok", upstreamBody)}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	out := rec.Body.String()
	require.True(t, strings.HasPrefix(out, ": OPENROUTER PROCESSING\n\n"), "comment preamble must reach the client first, got: %q", out)
	commentIdx := strings.Index(out, ": OPENROUTER PROCESSING")
	firstChunkIdx := strings.Index(out, `"delta":{"role":"assistant"}`)
	require.Greater(t, firstChunkIdx, commentIdx, "first chunk must follow the comment line")
	require.Contains(t, out, `"content":"leak"`)
	require.Contains(t, out, "data: [DONE]")
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

func TestUpstreamModelMismatch_BufferRawChatCompletionsFailsOver(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("application/json", "rid_raw_json", upstreamModelMismatchChatJSON(upstreamModelMismatchGotModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.bufferRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now())

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, "gpt-5.5", upstreamModelMismatchGotModel, false)
	require.Equal(t, 4, GetOpsUpstreamModelMismatch(c).Usage.InputTokens)
}

func TestUpstreamModelMismatch_BufferRawChatCompletionsMatchPasses(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("application/json", "rid_raw_json_ok", upstreamModelMismatchChatJSON("gpt-5.5"))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.bufferRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "leak", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// ---------- openai_gateway_messages.go ----------

func TestUpstreamModelMismatch_HandleAnthropicStreamingResponseFailsOver(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_msg_stream", upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleAnthropicStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	requireUpstreamModelMismatchPathFailover(t, c, rec, err, upstreamModelMismatchSentModel, upstreamModelMismatchGotModel, true)
}

func TestUpstreamModelMismatch_HandleAnthropicStreamingResponseMatchPasses(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_msg_stream_ok", upstreamModelMismatchResponsesSSE(upstreamModelMismatchSentModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleAnthropicStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: content_block_delta")
	require.Contains(t, rec.Body.String(), `"text":"leak"`)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

func TestUpstreamModelMismatch_HandleAnthropicBufferedStreamingResponseFailsOver(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_msg_buffered", upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, upstreamModelMismatchSentModel, upstreamModelMismatchGotModel, false)
}

func TestUpstreamModelMismatch_HandleAnthropicBufferedStreamingResponseMatchPasses(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_msg_buffered_ok", upstreamModelMismatchResponsesSSE(upstreamModelMismatchSentModel))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "leak", gjson.Get(rec.Body.String(), "content.0.text").String())
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// ---------- openai_gateway_messages_chat_fallback.go ----------

func TestUpstreamModelMismatch_StreamChatCompletionsAsAnthropicFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_msg_chat_stream", upstreamModelMismatchChatSSE(upstreamModelMismatchGotModel))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, forceChatMessagesFallbackAccount(), body, "")

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, "gpt-5.4", upstreamModelMismatchGotModel, true)
}

func TestUpstreamModelMismatch_StreamChatCompletionsAsAnthropicMatchPasses(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_msg_chat_stream_ok", upstreamModelMismatchChatSSE("gpt-5.4"))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, forceChatMessagesFallbackAccount(), body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"text":"leak"`)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

func TestUpstreamModelMismatch_BufferChatCompletionsAsAnthropicFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_msg_chat_json", upstreamModelMismatchChatJSON(upstreamModelMismatchGotModel))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, forceChatMessagesFallbackAccount(), body, "")

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, "gpt-5.4", upstreamModelMismatchGotModel, false)
	require.Equal(t, 4, GetOpsUpstreamModelMismatch(c).Usage.InputTokens)
}

func TestUpstreamModelMismatch_BufferChatCompletionsAsAnthropicMatchPasses(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_msg_chat_json_ok", upstreamModelMismatchChatJSON("gpt-5.4"))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, forceChatMessagesFallbackAccount(), body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "leak", gjson.Get(rec.Body.String(), "content.0.text").String())
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// ---------- openai_gateway_responses_chat_fallback.go ----------

func TestUpstreamModelMismatch_StreamChatCompletionsAsResponsesFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_resp_chat_stream", upstreamModelMismatchChatSSE(upstreamModelMismatchGotModel))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, "gpt-5.4", upstreamModelMismatchGotModel, true)
}

func TestUpstreamModelMismatch_StreamChatCompletionsAsResponsesMatchPasses(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_resp_chat_stream_ok", upstreamModelMismatchChatSSE("gpt-5.4"))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: response.output_text.delta")
	require.Contains(t, rec.Body.String(), `"delta":"leak"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

func TestUpstreamModelMismatch_BufferChatCompletionsAsResponsesFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_resp_chat_json", upstreamModelMismatchChatJSON(upstreamModelMismatchGotModel))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)

	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, "gpt-5.4", upstreamModelMismatchGotModel, false)
	require.Equal(t, 4, GetOpsUpstreamModelMismatch(c).Usage.InputTokens)
}

func TestUpstreamModelMismatch_BufferChatCompletionsAsResponsesMatchPasses(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_resp_chat_json_ok", upstreamModelMismatchChatJSON("gpt-5.4"))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "leak", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// ---------- openai_gateway_grok.go ----------

func upstreamModelMismatchGrokAccount() *Account {
	return &Account{
		ID:       730,
		Name:     "grok-mismatch",
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token",
			"base_url":     "https://xai.test/v1",
			"expires_at":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
}

func newUpstreamModelMismatchGrokService(resp *http.Response) *OpenAIGatewayService {
	provider := NewGrokTokenProvider(nil, &grokUnauthorizedCacheStub{cacheMiss: true}, nil)
	return &OpenAIGatewayService{httpUpstream: &httpUpstreamStub{resp: resp}, grokTokenProvider: provider}
}

// grok 系列只记录不拦截（xAI 带日期模型名，豁免覆盖不了，真实回显未验证）：不一致时照常透传并打标 Blocked=false。
func TestUpstreamModelMismatch_ForwardGrokResponsesStreamMismatchOnlyMarks(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", nil)
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_grok","object":"response","model":"grok-3","status":"in_progress"}}`,
		"",
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"leak"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_grok","model":"grok-3","status":"completed","usage":{"input_tokens":1,"output_tokens":2}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	svc := newUpstreamModelMismatchGrokService(upstreamModelMismatchHTTPResponse("text/event-stream", "rid_grok_stream", upstreamBody))

	result, err := svc.forwardGrokResponses(context.Background(), c, upstreamModelMismatchGrokAccount(),
		[]byte(`{"model":"grok-4.3","input":"hi","stream":true}`), "grok-4.3", true, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_grok", result.ResponseID)
	require.Contains(t, rec.Body.String(), `"delta":"leak"`)
	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.False(t, mark.Blocked)
	require.Equal(t, "grok-4.3", mark.SentModel)
	require.Equal(t, "grok-3", mark.ResponseModel)
	require.True(t, mark.Stream)
}

func TestUpstreamModelMismatch_ForwardGrokResponsesStreamMatchPasses(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", nil)
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_grok_ok","object":"response","model":"grok-4.3","status":"in_progress"}}`,
		"",
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"leak"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_grok_ok","model":"grok-4.3","status":"completed","usage":{"input_tokens":1,"output_tokens":2}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	svc := newUpstreamModelMismatchGrokService(upstreamModelMismatchHTTPResponse("text/event-stream", "rid_grok_stream_ok", upstreamBody))

	result, err := svc.forwardGrokResponses(context.Background(), c, upstreamModelMismatchGrokAccount(),
		[]byte(`{"model":"grok-4.3","input":"hi","stream":true}`), "grok-4.3", true, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_grok_ok", result.ResponseID)
	require.Contains(t, rec.Body.String(), `"delta":"leak"`)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

func TestUpstreamModelMismatch_ForwardGrokResponsesNonStreamMismatchOnlyMarks(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", nil)
	svc := newUpstreamModelMismatchGrokService(upstreamModelMismatchHTTPResponse("application/json", "rid_grok_json",
		`{"id":"resp_grok_json","object":"response","model":"grok-3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"leak"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`))

	result, err := svc.forwardGrokResponses(context.Background(), c, upstreamModelMismatchGrokAccount(),
		[]byte(`{"model":"grok-4.3","input":"hi"}`), "grok-4.3", false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_grok_json", result.ResponseID)
	require.Contains(t, rec.Body.String(), "resp_grok_json")
	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.False(t, mark.Blocked)
	require.Equal(t, "grok-4.3", mark.SentModel)
	require.Equal(t, "grok-3", mark.ResponseModel)
	require.False(t, mark.Stream)
	require.Equal(t, 3, mark.Usage.InputTokens)
}

func TestUpstreamModelMismatch_ForwardGrokResponsesNonStreamMatchPasses(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", nil)
	svc := newUpstreamModelMismatchGrokService(upstreamModelMismatchHTTPResponse("application/json", "rid_grok_json_ok",
		`{"id":"resp_grok_json_ok","object":"response","model":"grok-4.3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"leak"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`))

	result, err := svc.forwardGrokResponses(context.Background(), c, upstreamModelMismatchGrokAccount(),
		[]byte(`{"model":"grok-4.3","input":"hi"}`), "grok-4.3", false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_grok_json_ok", result.ResponseID)
	require.Contains(t, rec.Body.String(), "resp_grok_json_ok")
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}
