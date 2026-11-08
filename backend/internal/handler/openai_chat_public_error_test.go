//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// free 站上游（a6api）的真实形态：中文 message + 上游 request_id。
// Chat Completions 上来自上游的错误不能把这些原样返回给客户端。
const (
	chatPubRequestID  = "202611071511107613041508268d9d6XnfZJg22"
	chatPubBody400    = `{"error":{"message":"请求参数不正确，请检查请求体后重试。 原因：messages 字段不合法。 request_id: ` + chatPubRequestID + `","type":"invalid_request_error"}}`
	chatPubBody404    = `{"error":{"message":"当前模型或商家不支持请求中的能力。 原因：智能路由未找到可用商家。 request_id: ` + chatPubRequestID + `","type":"not_found_error"}}`
	chatPubBody403    = `{"error":{"message":"当前凭证的可用额度已用完。 request_id: ` + chatPubRequestID + `","type":"permission_error"}}`
	chatPubBody401    = `{"error":{"message":"凭证无效或已过期。 request_id: ` + chatPubRequestID + `","type":"authentication_error"}}`
	chatPubBodyCtx    = `{"error":{"message":"请求内容过大，当前模型或商家无法处理。 原因：本次上下文、附件或输出预算超过了可处理范围。 request_id: ` + chatPubRequestID + `","type":"invalid_request_error","code":"context_length_exceeded"}}`
	chatPubInvalidMsg = "Invalid request parameters, please check the request body and try again"
	chatPubCtxMsg     = "Your input exceeds the context window of this model. Please adjust your input and try again."
)

func chatPubHasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// requireNoUpstreamText 断言对外内容里没有任何上游原文痕迹。
func requireNoUpstreamText(t *testing.T, body string) {
	t.Helper()
	require.False(t, chatPubHasHan(body), "对外内容里不能有上游中文: %s", body)
	require.NotContains(t, body, "request_id")
	require.NotContains(t, body, chatPubRequestID)
	require.NotContains(t, body, "商家")
}

type chatPubUpstream struct {
	service.HTTPUpstream
	mu          sync.Mutex
	calls       int
	status      int
	contentType string
	body        string
}

func (u *chatPubUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	ct := u.contentType
	if ct == "" {
		ct = "application/json"
	}
	return &http.Response{
		StatusCode: u.status,
		Header:     http.Header{"Content-Type": []string{ct}, "X-Request-Id": []string{"upstream-" + chatPubRequestID}},
		Body:       io.NopCloser(strings.NewReader(u.body)),
	}, nil
}

func (u *chatPubUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// chatPubAccounts 返回两个 apikey 账号。viaResponses=false 走 CC 直转（free 站的账号形态），
// true 走 Responses↔Chat 转换路径。
func chatPubAccounts(viaResponses bool) []service.Account {
	extra := map[string]any(nil)
	if viaResponses {
		extra = map[string]any{"openai_responses_supported": true}
	}
	mk := func(id int64, name string, priority int) service.Account {
		return service.Account{
			ID: id, Name: name, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Priority: priority, Extra: extra,
			Credentials: map[string]any{"api_key": "sk-test", "openai_capabilities": []any{"chat_completions", "embeddings"}},
		}
	}
	return []service.Account{mk(1, "chat-pub-1", 0), mk(2, "chat-pub-2", 1)}
}

type chatPubResult struct {
	status int
	body   string
	calls  int
}

func runChatPublicErrorRequest(t *testing.T, path string, viaResponses bool, upstream *chatPubUpstream, reqBody string) chatPubResult {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := openAIImagesFailoverAccountRepo{accounts: chatPubAccounts(viaResponses)}
	h := newOpenAIResponsesFailoverTestHandlerWithRepo(t, upstream, repo)
	c, rec := newOpenAIFailoverTestContext(t, context.Background(), path, reqBody, false)
	if strings.HasSuffix(path, "/responses") {
		h.Responses(c)
	} else {
		h.ChatCompletions(c)
	}
	return chatPubResult{status: rec.Code, body: rec.Body.String(), calls: upstream.callCount()}
}

const (
	chatPubReqNonStream = `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`
	chatPubReqStream    = `{"model":"gpt-5.1","stream":true,"messages":[{"role":"user","content":"hello"}]}`
)

// 非流式 + 流式请求、CC 直转 + Responses 转换两条路径：上游返回 HTTP 错误时，
// 对外都只给固定形态（状态码 1:1，type / code / message 来自 UpstreamPublicErrorFor）。
func TestOpenAIGatewayHandlerChatCompletions_UpstreamHTTPErrorsArePublicShaped(t *testing.T) {
	cases := []struct {
		name       string
		upstream   int
		body       string
		wantStatus int
		wantType   string
		wantCode   string
		wantMsg    string
		failover   bool // 预期会在两个账号间切号后用尽
	}{
		{"400 透传中文参数错误", 400, chatPubBody400, 400, "invalid_request_error", "", chatPubInvalidMsg, false},
		{"400 code=context_length_exceeded 且 message 是中文", 400, chatPubBodyCtx, 400, "invalid_request_error", "context_length_exceeded", chatPubCtxMsg, false},
		{"404 not_found", 404, chatPubBody404, 404, "upstream_error", "", "Unknown error", false},
		{"403 failover 用尽", 403, chatPubBody403, 403, "upstream_error", "", "Unknown error", true},
		{"401 failover 用尽 -> 503", 401, chatPubBody401, 503, "upstream_error", "server_is_overloaded", "Upstream service temporarily unavailable", true},
	}
	for _, viaResponses := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, tc := range cases {
				name := tc.name
				if viaResponses {
					name += "/responses-convert"
				} else {
					name += "/raw-cc"
				}
				reqBody := chatPubReqNonStream
				if stream {
					name += "/stream"
					reqBody = chatPubReqStream
				} else {
					name += "/non-stream"
				}
				t.Run(name, func(t *testing.T) {
					upstream := &chatPubUpstream{status: tc.upstream, body: tc.body}
					res := runChatPublicErrorRequest(t, "/v1/chat/completions", viaResponses, upstream, reqBody)

					require.Equal(t, tc.wantStatus, res.status, res.body)
					requireNoUpstreamText(t, res.body)
					require.Equal(t, tc.wantType, gjson.Get(res.body, "error.type").String(), res.body)
					require.Equal(t, tc.wantCode, gjson.Get(res.body, "error.code").String(), res.body)
					require.Equal(t, tc.wantMsg, gjson.Get(res.body, "error.message").String(), res.body)
					if tc.failover {
						require.Equal(t, 2, res.calls, "两个账号都应被尝试过")
					} else {
						require.Equal(t, 1, res.calls)
					}
				})
			}
		}
	}
}

// bare 路由（/chat/completions）上同样生效：FullPath 为空时不依赖路由前缀。
func TestOpenAIGatewayHandlerChatCompletions_BareRouteUpstream400IsPublicShaped(t *testing.T) {
	upstream := &chatPubUpstream{status: 400, body: chatPubBody400}
	res := runChatPublicErrorRequest(t, "/chat/completions", false, upstream, chatPubReqNonStream)
	require.Equal(t, 400, res.status, res.body)
	require.Equal(t, chatPubInvalidMsg, gjson.Get(res.body, "error.message").String())
	requireNoUpstreamText(t, res.body)
}

// 流已经开始后，上游推来的 error 事件：写给客户端的是固定形态，上游原文只留在 ops 日志。
func TestOpenAIGatewayHandlerChatCompletions_MidStreamUpstreamErrorEventIsPublicShaped(t *testing.T) {
	// 只给 role 起始块、不带语义内容：本测试的 harness 没有计费服务，已交付的语义输出会进入 RecordUsage。
	const chunk = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-5.1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n"

	t.Run("raw-cc：error 帧被改写", func(t *testing.T) {
		sse := chunk +
			`data: {"error":{"message":"请求参数不正确，请检查请求体后重试。 request_id: ` + chatPubRequestID + `","type":"invalid_request_error"}}` + "\n\n"
		upstream := &chatPubUpstream{status: 200, contentType: "text/event-stream", body: sse}
		res := runChatPublicErrorRequest(t, "/v1/chat/completions", false, upstream, chatPubReqStream)

		requireNoUpstreamText(t, res.body)
		frame := lastSSEDataFrame(res.body)
		require.Equal(t, "invalid_request_error", gjson.Get(frame, "error.type").String(), res.body)
		require.Equal(t, chatPubInvalidMsg, gjson.Get(frame, "error.message").String(), res.body)
	})

	t.Run("raw-cc：策略 code（misalignment_policy_violation）保留，message=Unknown error", func(t *testing.T) {
		sse := chunk +
			`event: error` + "\n" +
			`data: {"error":{"message":"请求被安全策略拦截 request_id: ` + chatPubRequestID + `","type":"invalid_request_error","code":"misalignment_policy_violation"}}` + "\n\n"
		upstream := &chatPubUpstream{status: 200, contentType: "text/event-stream", body: sse}
		res := runChatPublicErrorRequest(t, "/v1/chat/completions", false, upstream, chatPubReqStream)

		requireNoUpstreamText(t, res.body)
		require.Contains(t, res.body, "event: error", "event 行原样保留")
		frame := lastSSEDataFrame(res.body)
		require.Equal(t, "misalignment_policy_violation", gjson.Get(frame, "error.code").String(), res.body)
		require.Equal(t, "upstream_error", gjson.Get(frame, "error.type").String(), res.body)
		require.Equal(t, "Unknown error", gjson.Get(frame, "error.message").String(), res.body)
	})

	t.Run("raw-cc：code=context_length_exceeded 且 message 是中文", func(t *testing.T) {
		sse := chunk + `data: ` + chatPubBodyCtx + "\n\n"
		upstream := &chatPubUpstream{status: 200, contentType: "text/event-stream", body: sse}
		res := runChatPublicErrorRequest(t, "/v1/chat/completions", false, upstream, chatPubReqStream)

		requireNoUpstreamText(t, res.body)
		frame := lastSSEDataFrame(res.body)
		require.Equal(t, "context_length_exceeded", gjson.Get(frame, "error.code").String(), res.body)
		require.Equal(t, chatPubCtxMsg, gjson.Get(frame, "error.message").String(), res.body)
	})

	t.Run("responses-convert：error 事件被改写", func(t *testing.T) {
		sse := "event: response.created\n" +
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.1","status":"in_progress"}}` + "\n\n" +
			"event: error\n" +
			`data: {"type":"error","error":{"message":"请求参数不正确，请检查请求体后重试。 request_id: ` + chatPubRequestID + `","type":"invalid_request_error"}}` + "\n\n"
		upstream := &chatPubUpstream{status: 200, contentType: "text/event-stream", body: sse}
		res := runChatPublicErrorRequest(t, "/v1/chat/completions", true, upstream, chatPubReqStream)

		requireNoUpstreamText(t, res.body)
		frame := lastSSEDataFrame(res.body)
		if frame == "" { // 内容尚未输出时按 JSON 错误写出
			frame = res.body
		}
		require.Equal(t, "invalid_request_error", gjson.Get(frame, "error.type").String(), res.body)
		require.Equal(t, chatPubInvalidMsg, gjson.Get(frame, "error.message").String(), res.body)
	})

	t.Run("responses-convert：response.failed 事件的策略 code（misalignment_policy_violation；cyber_policy 会触发风控记录，需要计费服务 harness，由 service 层 cyber 用例覆盖）", func(t *testing.T) {
		sse := "event: response.created\n" +
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.1","status":"in_progress"}}` + "\n\n" +
			"event: response.failed\n" +
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"misalignment_policy_violation","message":"请求被安全策略拦截 request_id: ` + chatPubRequestID + `"}}}` + "\n\n"
		upstream := &chatPubUpstream{status: 200, contentType: "text/event-stream", body: sse}
		res := runChatPublicErrorRequest(t, "/v1/chat/completions", true, upstream, chatPubReqStream)

		requireNoUpstreamText(t, res.body)
		frame := lastSSEDataFrame(res.body)
		if frame == "" {
			frame = res.body
		}
		require.Equal(t, "misalignment_policy_violation", gjson.Get(frame, "error.code").String(), res.body)
		require.Equal(t, "Unknown error", gjson.Get(frame, "error.message").String(), res.body)
	})
}

// lastSSEDataFrame 返回响应体里最后一个 JSON 形态的 data 帧（跳过 [DONE]）；没有则返回空串。
func lastSSEDataFrame(body string) string {
	last := ""
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if gjson.Valid(data) && gjson.Get(data, "error").Exists() {
			last = data
		}
	}
	return last
}

// handleChatFailoverExhausted 直接的单元覆盖：流式 / 非流式写出，以及保留网关自己文案的例外。
func TestHandleChatFailoverExhausted(t *testing.T) {
	t.Run("非流式：403 -> 403 Unknown error", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		(&OpenAIGatewayHandler{}).handleChatFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusForbidden, ResponseBody: []byte(chatPubBody403),
		}, false)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.JSONEq(t, `{"error":{"message":"Unknown error","type":"upstream_error"}}`, rec.Body.String())
	})

	t.Run("流式已开始：流内 error 事件带同样的 type/code/message", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		(&OpenAIGatewayHandler{}).handleChatFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadRequest, ResponseBody: []byte(`{"error":{"code":"model_not_found","message":"当前模型不存在 request_id: x"}}`),
		}, true)
		body := rec.Body.String()
		require.Contains(t, body, "event: error")
		requireNoUpstreamText(t, body)
		frame := lastSSEDataFrame(body)
		require.Equal(t, "invalid_request_error", gjson.Get(frame, "error.type").String())
		require.Equal(t, "model_not_found", gjson.Get(frame, "error.code").String())
		require.Equal(t, chatPubInvalidMsg, gjson.Get(frame, "error.message").String())
	})

	t.Run("状态码 1:1，非标准码按注册表换算", func(t *testing.T) {
		for status, want := range map[int]int{402: 402, 404: 404, 429: 429, 500: 500, 502: 502, 504: 504, 520: 502, 522: 504, 529: 503} {
			c, rec := newCodexTestCtx("/v1/chat/completions")
			(&OpenAIGatewayHandler{}).handleChatFailoverExhausted(c, &service.UpstreamFailoverError{
				StatusCode: status, ResponseBody: []byte(chatPubBody404),
			}, false)
			require.Equal(t, want, rec.Code, status)
			requireNoUpstreamText(t, rec.Body.String())
		}
	})

	t.Run("网关合成的 failover 保留网关自己的文案：请求体过大", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		(&OpenAIGatewayHandler{}).handleChatFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusRequestEntityTooLarge, ResponseBody: []byte(chatPubBody400),
		}, false)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		require.Equal(t, service.OpenAIRequestBodyTooLargeClientMessage, gjson.Get(rec.Body.String(), "error.message").String())
	})

	t.Run("网关合成的 failover 保留网关自己的文案：上游模型不一致拦截", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		(&OpenAIGatewayHandler{}).handleChatFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:   http.StatusBadGateway,
			ResponseBody: []byte(`{"error":{"type":"upstream_error","code":"upstream_model_mismatch","message":"x"}}`),
		}, false)
		// 原路径：502 + 既有固定文案，不变成 "Unknown error"。
		require.Equal(t, http.StatusBadGateway, rec.Code)
		require.Equal(t, "Upstream service temporarily unavailable", gjson.Get(rec.Body.String(), "error.message").String())
	})

	t.Run("网关合成的 failover 保留网关自己的文案：静默拒绝", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		body := []byte(`{"error":{"type":"upstream_error","code":"openai_silent_refusal","message":"x"}}`)
		require.True(t, service.IsOpenAISilentRefusalErrorBody(body), "测试前提：这是静默拒绝的 failover 体")
		(&OpenAIGatewayHandler{}).handleChatFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway, ResponseBody: body,
		}, false)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		require.Equal(t, service.OpenAISilentRefusalClientMessage(), gjson.Get(rec.Body.String(), "error.message").String())
	})

	t.Run("状态码不在 400..599：沿用原路径（固定网关文案）", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		(&OpenAIGatewayHandler{}).handleChatFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: 0, ResponseBody: []byte(chatPubBody404),
		}, false)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		requireNoUpstreamText(t, rec.Body.String())
	})
}

// 网关自己的错误文案原样保留：不能因为 Chat Completions 的归一化被换成 "Unknown error"。
func TestOpenAIGatewayHandlerChatCompletions_GatewayOwnErrorsKeepTheirMessages(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantType   string
		wantMsg    string
	}{
		{"请求体为空", ``, 400, "invalid_request_error", "Request body is empty"},
		{"缺 model", `{"messages":[{"role":"user","content":"hi"}]}`, 400, "invalid_request_error", "model is required"},
		{"stream 类型不对", `{"model":"gpt-5.1","stream":"yes","messages":[{"role":"user","content":"hi"}]}`, 400, "invalid_request_error", invalidStreamFieldTypeMessage},
		{"不是合法 JSON", `{"model":`, 400, "invalid_request_error", "Failed to parse request body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &chatPubUpstream{status: 400, body: chatPubBody400}
			res := runChatPublicErrorRequest(t, "/v1/chat/completions", false, upstream, tc.body)
			require.Equal(t, tc.wantStatus, res.status, res.body)
			require.Equal(t, tc.wantType, gjson.Get(res.body, "error.type").String(), res.body)
			require.Equal(t, tc.wantMsg, gjson.Get(res.body, "error.message").String(), res.body)
			require.Zero(t, res.calls, "网关自己拒绝的请求不会打到上游")
		})
	}

	t.Run("没有可用账号：网关自己的文案", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		upstream := &chatPubUpstream{status: 400, body: chatPubBody400}
		h := newOpenAIResponsesFailoverTestHandlerWithRepo(t, upstream, openAIImagesFailoverAccountRepo{})
		c, rec := newOpenAIFailoverTestContext(t, context.Background(), "/v1/chat/completions", chatPubReqNonStream, false)
		h.ChatCompletions(c)
		require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "Unknown error")
		require.Contains(t, rec.Body.String(), "Service temporarily unavailable")
		require.Zero(t, upstream.callCount())
	})

	t.Run("网关自己的 401（API key 无效）", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatPubReqNonStream))
		(&OpenAIGatewayHandler{}).ChatCompletions(c)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Equal(t, "authentication_error", gjson.Get(rec.Body.String(), "error.type").String())
		require.Equal(t, "Invalid API key", gjson.Get(rec.Body.String(), "error.message").String())
	})
}

// /responses：上游 400 的中文原文（以及 request_id）被固定英文替换，状态码仍是 400。
func TestOpenAIGatewayHandlerResponses_Upstream400IsPublicShaped(t *testing.T) {
	respBody := `{"model":"gpt-5.1","stream":false,"input":"hello"}`
	cases := []struct {
		name     string
		body     string
		wantMsg  string
		wantCode string
	}{
		{"中文参数错误", chatPubBody400, chatPubInvalidMsg, ""},
		{"code=context_length_exceeded 的中文 message（以前原样透传）", chatPubBodyCtx, chatPubCtxMsg, "context_length_exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &chatPubUpstream{status: 400, body: tc.body}
			res := runChatPublicErrorRequest(t, "/v1/responses", false, upstream, respBody)
			require.Equal(t, 400, res.status, res.body)
			requireNoUpstreamText(t, res.body)
			require.Equal(t, tc.wantMsg, gjson.Get(res.body, "error.message").String(), res.body)
			require.Equal(t, tc.wantCode, gjson.Get(res.body, "error.code").String(), res.body)
		})
	}

	t.Run("failover 用尽后的 400 model_not_found 保留 code，不再带上游（脱敏后的）中文", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/responses")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:   http.StatusBadRequest,
			ResponseBody: []byte(`{"error":{"code":"model_not_found","message":"当前模型或商家不支持 request_id: ` + chatPubRequestID + `"}}`),
		}, false)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		requireNoUpstreamText(t, rec.Body.String())
		require.Equal(t, "model_not_found", gjson.Get(rec.Body.String(), "error.code").String())
		require.Equal(t, chatPubInvalidMsg, gjson.Get(rec.Body.String(), "error.message").String())
	})

	t.Run("/responses 的英文 context-window 提示仍然放行", func(t *testing.T) {
		upstream := &chatPubUpstream{status: 400, body: `{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"invalid_request_error"}}`}
		res := runChatPublicErrorRequest(t, "/v1/responses", false, upstream, respBody)
		require.Equal(t, 400, res.status, res.body)
		require.Contains(t, gjson.Get(res.body, "error.message").String(), "maximum context length")
	})
}
