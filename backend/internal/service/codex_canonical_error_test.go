package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// upstreamChineseFailedEvent 是线上真实观测到的形态：上游商家在 SSE 里发中文
// response.failed，Codex 把 error.message 原样拼进
// "stream disconnected before completion: ..." 显示给用户。
const upstreamChineseFailedEvent = `{"type":"response.failed","response":{"id":"resp_x","status":"failed","error":{"message":"上游服务暂时不可用。 原因：上游服务、网络链路或代理返回异常响应。 解决方案：请稍后重试。如当前使用智能路由，请先重试；若仍失败，建议切换固定商家。 request_id: 202609050643287842389368268d9d6tyKkIGtR"}}}`

func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func newResponsesTestContext(t *testing.T, path string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, rec
}

func TestCodexCanonicalErrorFor_HTTPStatusMapping(t *testing.T) {
	cases := []struct {
		name           string
		upstreamStatus int
		upstreamBody   string
		wantStatus     int
		wantBody       string
	}{
		{"上游 401 必须改写，否则 Codex 会去刷新用户自己的 token", 401, "", 503, `{"error":{"code":"server_is_overloaded","type":"upstream_error"}}`},
		{"402 保留状态码", 402, "", 402, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"403 保留状态码", 403, "", 403, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"404 保留状态码", 404, "", 404, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"408 保留状态码", 408, "", 408, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"422 保留状态码", 422, "", 422, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"429 非额度类走 Codex 的 retry-limit 文案", 429, `{"error":{"message":"慢一点"}}`, 429, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"500 保留，Codex 有硬编码的高需求文案", 500, "", 500, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"502 保留状态码", 502, "", 502, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"503 带 server_is_overloaded 才能命中官方文案", 503, "", 503, `{"error":{"code":"server_is_overloaded","type":"upstream_error"}}`},
		{"504 保留状态码", 504, "", 504, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"529 非标准状态码，语义等价于 503", 529, "", 503, `{"error":{"code":"server_is_overloaded","type":"upstream_error"}}`},
		{"520 非标准状态码，Codex 渲染不出 reason phrase", 520, "", 502, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"522 非标准状态码，语义等价于 504", 522, "", 504, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"无上游状态码时兜底 429", 0, "", 429, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"未知上游状态码兜底 429", -1, "", 429, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
		{"429 usage_not_included", 429, `{"error":{"type":"usage_not_included"}}`, 429, `{"error":{"type":"usage_not_included"}}`},
		{"上游中文 response.failed 也被彻底替换", 0, upstreamChineseFailedEvent, 429, `{"error":{"message":"Unknown error","type":"upstream_error"}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CodexCanonicalErrorFor(tc.upstreamStatus, []byte(tc.upstreamBody))
			require.Equal(t, tc.wantStatus, got.HTTPStatus)
			require.JSONEq(t, tc.wantBody, string(got.Body))
		})
	}
}

func TestCodexCanonicalErrorFor_HTTPNotOverridden(t *testing.T) {
	t.Run("400 由 Codex 原样打印 body，协议上没有官方文案可用", func(t *testing.T) {
		got := CodexCanonicalErrorFor(http.StatusBadRequest, nil)
		require.Zero(t, got.HTTPStatus)
		require.Nil(t, got.Body)
	})

	t.Run("cyber_policy 是 Codex 原生 code，保持现状", func(t *testing.T) {
		got := CodexCanonicalErrorFor(http.StatusBadRequest, []byte(`{"error":{"code":"cyber_policy","message":"blocked"}}`))
		require.Zero(t, got.HTTPStatus)
		require.Empty(t, got.SSEErrCode)
	})

	t.Run("misalignment_policy_violation 同样保持现状", func(t *testing.T) {
		got := CodexCanonicalErrorFor(http.StatusForbidden, []byte(`{"error":{"code":"misalignment_policy_violation"}}`))
		require.Zero(t, got.HTTPStatus)
		require.Empty(t, got.SSEErrCode)
	})

	t.Run("上下文超长的英文提示比 Unknown error 有用，放行", func(t *testing.T) {
		body := []byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"upstream_error"}}`)
		got := CodexCanonicalErrorFor(http.StatusBadGateway, body)
		require.Zero(t, got.HTTPStatus)
		require.Nil(t, got.Body)
		// 流内仍然要换成 Codex 认得的 code，走的是它自己的官方文案。
		require.Equal(t, CodexErrCodeContextLengthExceeded, got.SSEErrCode)
	})

	t.Run("只有 code 命中、message 是中文时不放行", func(t *testing.T) {
		body := []byte(`{"error":{"code":"context_length_exceeded","message":"上下文超长，请缩短输入"}}`)
		got := CodexCanonicalErrorFor(http.StatusBadGateway, body)
		require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
		require.False(t, containsHan(string(got.Body)))
	})
}

func TestCodexCanonicalErrorFor_UsageLimitReachedKeepsOnlyCodexFields(t *testing.T) {
	body := `{"error":{"type":"usage_limit_reached","plan_type":"pro","resets_at":1788600000,"message":"额度已用尽，请联系商家"}}`
	got := CodexCanonicalErrorFor(http.StatusTooManyRequests, []byte(body))

	require.Equal(t, http.StatusTooManyRequests, got.HTTPStatus)
	require.Equal(t, "usage_limit_reached", gjson.GetBytes(got.Body, "error.type").String())
	require.Equal(t, "pro", gjson.GetBytes(got.Body, "error.plan_type").String())
	require.Equal(t, int64(1788600000), gjson.GetBytes(got.Body, "error.resets_at").Int())
	require.False(t, gjson.GetBytes(got.Body, "error.message").Exists(), "上游 message 不能出现在对外响应里")
	require.False(t, containsHan(string(got.Body)))
}

func TestCodexCanonicalErrorFor_UsageLimitReachedAcceptsStringResetsAt(t *testing.T) {
	// 部分上游把 resets_at 序列化成字符串；Codex 期望的是 Unix 秒整数。
	body := `{"error":{"type":"usage_limit_reached","resets_at":"1788600000"}}`
	got := CodexCanonicalErrorFor(http.StatusTooManyRequests, []byte(body))
	require.Equal(t, int64(1788600000), gjson.GetBytes(got.Body, "error.resets_at").Int())
	require.False(t, gjson.GetBytes(got.Body, "error.plan_type").Exists())
}

func TestCodexCanonicalErrorFor_SSEErrCodeMapping(t *testing.T) {
	cases := []struct {
		name           string
		upstreamStatus int
		upstreamBody   string
		want           string
	}{
		{"上下文超长", 400, `{"error":{"message":"maximum context length exceeded"}}`, CodexErrCodeContextLengthExceeded},
		{"上游直接给 context_length_exceeded", 400, `{"error":{"code":"context_length_exceeded"}}`, CodexErrCodeContextLengthExceeded},
		{"额度用尽映射到 insufficient_quota", 429, `{"error":{"type":"usage_limit_reached"}}`, CodexErrCodeInsufficientQuota},
		{"usage_not_included", 429, `{"error":{"type":"usage_not_included"}}`, CodexErrCodeUsageNotIncluded},
		{"insufficient_quota 原样保留", 429, `{"error":{"code":"insufficient_quota"}}`, CodexErrCodeInsufficientQuota},
		{"请求形 4xx", 422, "", CodexErrCodeInvalidPrompt},
		{"429 非额度类", 429, "", CodexErrCodeServerOverloaded},
		{"5xx", 502, "", CodexErrCodeServerOverloaded},
		{"传输错误没有状态码", 0, "", CodexErrCodeServerOverloaded},
		{"上游中文 response.failed", 0, upstreamChineseFailedEvent, CodexErrCodeServerOverloaded},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, CodexCanonicalErrorFor(tc.upstreamStatus, []byte(tc.upstreamBody)).SSEErrCode)
		})
	}
}

func TestCodexCanonicalResponsesError_OnlyAppliesToResponsesRoutes(t *testing.T) {
	t.Run("responses 路由生效", func(t *testing.T) {
		c, _ := newResponsesTestContext(t, "/v1/responses")
		got := CodexCanonicalResponsesError(c, http.StatusBadGateway, nil)
		require.NotNil(t, got)
		require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	})

	t.Run("chat completions 路由不生效", func(t *testing.T) {
		c, _ := newResponsesTestContext(t, "/v1/chat/completions")
		require.Nil(t, CodexCanonicalResponsesError(c, http.StatusBadGateway, nil))
	})

	t.Run("不覆盖的错误类型返回 nil", func(t *testing.T) {
		c, _ := newResponsesTestContext(t, "/v1/responses")
		require.Nil(t, CodexCanonicalResponsesError(c, http.StatusBadRequest, nil))
	})
}

func TestCodexCanonicalErrorForContext_UsesRecordedUpstreamStatus(t *testing.T) {
	c, _ := newResponsesTestContext(t, "/v1/responses")
	require.False(t, HasCodexCanonicalUpstream(c))

	// handler 拿到的 fallback 已经被 MapUpstreamErrorDefault 归一成 502，
	// 归一化映射必须用归一之前的真实上游状态码。
	SetCodexCanonicalUpstream(c, http.StatusNotFound, nil)
	require.True(t, HasCodexCanonicalUpstream(c))
	require.Equal(t, http.StatusNotFound, CodexCanonicalErrorForContext(c, http.StatusBadGateway).HTTPStatus)

	SetCodexCanonicalUpstream(c, 0, nil)
	require.Equal(t, http.StatusBadGateway, CodexCanonicalErrorForContext(c, http.StatusBadGateway).HTTPStatus)
}

func TestSanitizeOpenAIResponseFailedEventForClient_RewritesUpstreamMessageOnResponsesRoutes(t *testing.T) {
	c, _ := newResponsesTestContext(t, "/v1/responses")

	updated, sanitized := sanitizeOpenAIResponseFailedEventForClient(c, []byte(upstreamChineseFailedEvent), "response.failed", true)
	require.True(t, sanitized)
	require.Equal(t, CodexErrCodeServerOverloaded, gjson.GetBytes(updated, "response.error.code").String())
	require.False(t, gjson.GetBytes(updated, "response.error.message").Exists())
	require.False(t, containsHan(string(updated)), "上游中文不能出现在下发给 Codex 的事件里")
	require.NotContains(t, string(updated), "request_id")
}

func TestSanitizeOpenAIResponseFailedEventForClient_NonResponsesRouteKeepsLegacyBehaviour(t *testing.T) {
	c, _ := newResponsesTestContext(t, "/v1/chat/completions")

	updated, _ := sanitizeOpenAIResponseFailedEventForClient(c, []byte(upstreamChineseFailedEvent), "response.failed", true)
	require.Contains(t, string(updated), "上游服务暂时不可用")
}

func TestSanitizeOpenAIResponseFailedEventForClient_KeepsCyberPolicyEventIntact(t *testing.T) {
	c, _ := newResponsesTestContext(t, "/v1/responses")
	payload := `{"type":"response.failed","response":{"id":"resp_1","error":{"code":"cyber_policy","message":"blocked by policy"}}}`

	updated, _ := sanitizeOpenAIResponseFailedEventForClient(c, []byte(payload), "response.failed", true)
	require.Equal(t, "cyber_policy", gjson.GetBytes(updated, "response.error.code").String())
	require.Equal(t, "blocked by policy", gjson.GetBytes(updated, "response.error.message").String())
}

// 容量降载（server_is_overloaded / slow_down）会让 Codex 就地终止会话，所以转发前
// 已被改写成可重试的 server_error。Codex 归一化不能把它拉回致命码，但仍要抹掉上游
// message，否则中文原文会被 Codex 拼进 "stream disconnected before completion:"。
func TestSanitizeOpenAIResponseFailedEventForClient_KeepsCapacityShedRetryableCode(t *testing.T) {
	c, _ := newResponsesTestContext(t, "/v1/responses")
	payload := `{"type":"response.failed","response":{"id":"resp_1","error":{"code":"server_is_overloaded","message":"上游容量不足，请稍后重试"}}}`

	updated, sanitized := sanitizeOpenAIResponseFailedEventForClient(c, []byte(payload), "response.failed", true)
	require.True(t, sanitized)
	require.Equal(t, openAICapacityShedRetryableClientCode, gjson.GetBytes(updated, "response.error.code").String())
	require.False(t, gjson.GetBytes(updated, "response.error.message").Exists())
	require.False(t, containsHan(string(updated)))
}

func TestInboundIsResponses(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/responses", true},
		{"/v1/responses/compact", true},
		{"/responses", true},
		{"/responses/compact", true},
		{"/backend-api/codex/responses", true},
		{"/backend-api/codex/responses/compact", true},
		{"/v1/chat/completions", false},
		{"/v1/messages", false},
		{"/", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			c, _ := newResponsesTestContext(t, tc.path)
			require.Equal(t, tc.want, InboundIsResponses(c))
		})
	}

	require.False(t, InboundIsResponses(nil))
}

func TestCodexResponsesFailedEventData(t *testing.T) {
	c, _ := newResponsesTestContext(t, "/v1/responses")
	data := codexResponsesFailedEventData(c, CodexErrCodeServerOverloaded)

	// Codex 只解析带顶层 "type" 的 data 行，且只把 response.failed 之类当作终止事件。
	require.Equal(t, "response.failed", gjson.Get(data, "type").String())
	require.Equal(t, "failed", gjson.Get(data, "response.status").String())
	require.Equal(t, CodexErrCodeServerOverloaded, gjson.Get(data, "response.error.code").String())
	require.True(t, gjson.Get(data, "response.output").IsArray())
}

func TestCodexCanonicalUpstreamHintHandlesMissingContext(t *testing.T) {
	require.NotPanics(t, func() { SetCodexCanonicalUpstream(nil, http.StatusBadGateway, nil) })
	require.False(t, HasCodexCanonicalUpstream(nil))

	// 没记录过上游错误时退回调用方给的状态码。
	c, _ := newResponsesTestContext(t, "/v1/responses")
	require.Equal(t, http.StatusNotFound, CodexCanonicalErrorForContext(c, http.StatusNotFound).HTTPStatus)
}

func TestCodexSynthesizedResponseID_ReusesServerRequestID(t *testing.T) {
	// 合成的 response id 复用服务端 request_id，方便把客户端看到的报错关联回日志。
	c, _ := newResponsesTestContext(t, "/v1/responses")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "20260905-abc-def"))

	require.Equal(t, "resp_20260905abcdef", codexSynthesizedResponseID(c))
}

func TestRewriteOpenAIResponseFailedErrorForCodex_TopLevelErrorPayload(t *testing.T) {
	// 有的上游把错误放在顶层 error 而不是 response.error 下。
	payload := []byte(`{"type":"response.failed","error":{"message":"上游服务暂时不可用"}}`)

	updated, ok := rewriteOpenAIResponseFailedErrorForCodex(payload, CodexErrCodeServerOverloaded)
	require.True(t, ok)
	require.Equal(t, CodexErrCodeServerOverloaded, gjson.GetBytes(updated, "error.code").String())
	require.False(t, containsHan(string(updated)))

	unchanged, ok := rewriteOpenAIResponseFailedErrorForCodex(payload, "")
	require.False(t, ok)
	require.Equal(t, payload, unchanged)
}

func TestOpenAIHandleErrorResponse_ResponsesRouteReplacesUpstreamBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte(upstreamChineseFailedEvent))),
	}
	account := &Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	_, err := (&OpenAIGatewayService{}).handleErrorResponse(context.Background(), resp, c, account, nil)

	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "Unknown error", gjson.Get(rec.Body.String(), "error.message").String())
	require.False(t, containsHan(rec.Body.String()), "上游中文不能出现在对外响应里")
}
