package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	pubInvalidParams = "Invalid request parameters, please check the request body and try again"
	pubContextLimit  = "Your input exceeds the context window of this model. Please adjust your input and try again."
	pubOverloaded    = "Upstream service temporarily unavailable"
)

// free 站上游（a6api）真实观测到的响应体形态：中文 message + request_id。
const (
	freeBody400 = `{"error":{"message":"请求参数不正确，请检查请求体后重试。 原因：messages 字段不合法。 request_id: 202611071511107613041508268d9d6XnfZJg22","type":"invalid_request_error"}}`
	freeBody404 = `{"error":{"message":"当前模型或商家不支持请求中的能力。 原因：智能路由未找到可用商家。 request_id: 202611071511107613041508268d9d6XnfZJg22","type":"not_found_error"}}`
	freeBody403 = `{"error":{"message":"当前凭证的可用额度已用完。 request_id: 202611071511107613041508268d9d6XnfZJg22","type":"permission_error"}}`
	freeBody429 = `{"error":{"message":"请求过于频繁，请稍后重试。 request_id: 202611071511107613041508268d9d6XnfZJg22","type":"rate_limit_error"}}`
	freeBody401 = `{"error":{"message":"凭证无效或已过期。 request_id: 202611071511107613041508268d9d6XnfZJg22","type":"authentication_error"}}`
	freeBody529 = `{"error":{"message":"上游服务过载，请稍后重试。 request_id: 202611071511107613041508268d9d6XnfZJg22","type":"overloaded_error"}}`
	freeBody520 = `{"error":{"message":"上游服务暂时不可用。 原因：上游服务、网络链路或代理返回异常响应。 解决方案：建议切换固定商家。 request_id: 202611071511107613041508268d9d6XnfZJg22"}}`
)

func TestUpstreamPublicErrorFor_FreeSiteSamplesNeverLeakUpstreamText(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantType   string
		wantCode   string
		wantMsg    string
	}{
		{"400 中文参数错误", 400, freeBody400, 400, "invalid_request_error", "", pubInvalidParams},
		{"404 中文 not_found", 404, freeBody404, 404, "upstream_error", "", "Unknown error"},
		{"403 额度用尽", 403, freeBody403, 403, "upstream_error", "", "Unknown error"},
		{"429 限流", 429, freeBody429, 429, "upstream_error", "", "Unknown error"},
		{"401 凭证无效 -> 503", 401, freeBody401, 503, "upstream_error", "server_is_overloaded", pubOverloaded},
		{"529 非标准 -> 503", 529, freeBody529, 503, "upstream_error", "server_is_overloaded", pubOverloaded},
		{"520 非标准 -> 502", 520, freeBody520, 502, "upstream_error", "", "Unknown error"},
		{"522 非标准 -> 504", 522, freeBody520, 504, "upstream_error", "", "Unknown error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UpstreamPublicErrorFor(tc.status, []byte(tc.body))
			require.Equal(t, tc.wantStatus, got.HTTPStatus)
			require.Equal(t, tc.wantType, got.Type)
			require.Equal(t, tc.wantCode, got.Code)
			require.Equal(t, tc.wantMsg, got.Message)

			out := string(got.JSONBody())
			require.False(t, containsHan(out), "输出里不能有中文: %s", out)
			require.NotContains(t, out, "request_id")
			require.NotContains(t, out, "202611071511107613041508268d9d6XnfZJg22")
		})
	}
}

func TestUpstreamPublicErrorFor_DecisionOrder(t *testing.T) {
	t.Run("1 策略 code：状态码 1:1，type=upstream_error，保留 code，message=Unknown error", func(t *testing.T) {
		for _, code := range []string{"cyber_policy", "misalignment_policy_violation", "bio_policy"} {
			got := UpstreamPublicErrorFor(http.StatusBadRequest, []byte(`{"error":{"code":"`+code+`","message":"请求被安全策略拦截 request_id: x","type":"invalid_request_error"}}`))
			require.Equal(t, UpstreamPublicError{HTTPStatus: 400, Type: "upstream_error", Code: code, Message: "Unknown error"}, got, code)
		}
		got := UpstreamPublicErrorFor(http.StatusForbidden, []byte(`{"error":{"code":"cyber_policy"}}`))
		require.Equal(t, 403, got.HTTPStatus)
		got = UpstreamPublicErrorFor(520, []byte(`{"error":{"code":"cyber_policy"}}`))
		require.Equal(t, 502, got.HTTPStatus, "非标准状态码按注册表换算")
	})

	t.Run("1 策略 code 藏在 HTTP 200 SSE 里时按 400，不给客户端会触发重试的 5xx/2xx", func(t *testing.T) {
		got := UpstreamPublicErrorFor(http.StatusOK, []byte(`{"type":"response.failed","response":{"error":{"code":"cyber_policy"}}}`))
		require.Equal(t, 400, got.HTTPStatus)
		require.Equal(t, "cyber_policy", got.Code)
	})

	t.Run("1 优先于 2、3、4、5", func(t *testing.T) {
		got := UpstreamPublicErrorFor(http.StatusUnauthorized, []byte(`{"error":{"code":"cyber_policy","type":"usage_limit_reached","message":"maximum context length exceeded"}}`))
		require.Equal(t, "cyber_policy", got.Code)
		require.Equal(t, 401, got.HTTPStatus)
	})

	t.Run("2 额度类：429，保留 type，不带上游 message", func(t *testing.T) {
		for _, typ := range []string{"usage_limit_reached", "usage_not_included"} {
			got := UpstreamPublicErrorFor(http.StatusForbidden, []byte(`{"error":{"type":"`+typ+`","message":"套餐已用完，将于 2026-11-08 重置 request_id: x","plan_type":"plus","resets_at":1762600000}}`))
			require.Equal(t, UpstreamPublicError{HTTPStatus: 429, Type: typ, Message: "Unknown error"}, got, typ)
			require.NotContains(t, string(got.JSONBody()), "plan_type")
			require.NotContains(t, string(got.JSONBody()), "resets_at")
		}
	})

	t.Run("2 不看状态码：HTTP 200 里藏的额度错误也命中", func(t *testing.T) {
		got := UpstreamPublicErrorFor(http.StatusOK, []byte(`{"error":{"type":"usage_limit_reached"}}`))
		require.Equal(t, 429, got.HTTPStatus)
	})

	t.Run("3 英文 context-window 提示：不覆盖", func(t *testing.T) {
		for _, msg := range []string{
			"This model's maximum context length is 8192 tokens.",
			"Your input exceeds the context window of this model.",
			"context_length_exceeded",
		} {
			got := UpstreamPublicErrorFor(http.StatusBadRequest, []byte(`{"error":{"message":"`+msg+`","type":"invalid_request_error"}}`))
			require.Zero(t, got.HTTPStatus, msg)
		}
		// 英文提示优先于 401 / 其它状态码规则。
		require.Zero(t, UpstreamPublicErrorFor(http.StatusUnauthorized, []byte(`{"error":{"message":"maximum context length exceeded"}}`)).HTTPStatus)
		require.Zero(t, UpstreamPublicErrorFor(http.StatusBadGateway, []byte(`{"error":{"message":"maximum context length exceeded"}}`)).HTTPStatus)
	})

	t.Run("4 code=context_length_exceeded 但 message 是中文：固定英文文案 + 保留 code", func(t *testing.T) {
		got := UpstreamPublicErrorFor(http.StatusBadRequest, []byte(`{"error":{"code":"context_length_exceeded","message":"上下文超长，请缩短输入 request_id: x","type":"invalid_request_error"}}`))
		require.Equal(t, UpstreamPublicError{HTTPStatus: 400, Type: "invalid_request_error", Code: "context_length_exceeded", Message: pubContextLimit}, got)

		// 状态码 1:1（经 codexCanonicalStatus）。
		require.Equal(t, 413, UpstreamPublicErrorFor(http.StatusRequestEntityTooLarge, []byte(`{"error":{"code":"context_length_exceeded","message":"太长了"}}`)).HTTPStatus)
		require.Equal(t, 502, UpstreamPublicErrorFor(520, []byte(`{"error":{"code":"context_length_exceeded","message":"太长了"}}`)).HTTPStatus)
		// 状态码缺失时按 400。
		require.Equal(t, 400, UpstreamPublicErrorFor(0, []byte(`{"error":{"code":"context_length_exceeded","message":"太长了"}}`)).HTTPStatus)
	})

	t.Run("5 上游 401 -> 503 server_is_overloaded", func(t *testing.T) {
		got := UpstreamPublicErrorFor(http.StatusUnauthorized, nil)
		require.Equal(t, UpstreamPublicError{HTTPStatus: 503, Type: "upstream_error", Code: "server_is_overloaded", Message: pubOverloaded}, got)
	})

	t.Run("6 状态码不在 400..599：不覆盖", func(t *testing.T) {
		for _, status := range []int{0, -1, 200, 302, 399, 600} {
			require.Zero(t, UpstreamPublicErrorFor(status, []byte(freeBody520)).HTTPStatus, status)
			require.Zero(t, UpstreamPublicErrorFor(status, nil).HTTPStatus, status)
		}
	})

	t.Run("7 400：固定文案；model_not_found 带 code", func(t *testing.T) {
		got := UpstreamPublicErrorFor(http.StatusBadRequest, nil)
		require.Equal(t, UpstreamPublicError{HTTPStatus: 400, Type: "invalid_request_error", Message: pubInvalidParams}, got)

		got = UpstreamPublicErrorFor(http.StatusBadRequest, []byte(`{"error":{"code":"model_not_found","message":"当前模型不存在 request_id: x"}}`))
		require.Equal(t, UpstreamPublicError{HTTPStatus: 400, Type: "invalid_request_error", Code: "model_not_found", Message: pubInvalidParams}, got)

		// 没有 code、只有英文 message 的 model-not-found 也识别。
		got = UpstreamPublicErrorFor(http.StatusBadRequest, []byte(`{"error":{"message":"The model gpt-x does not exist: model not found"}}`))
		require.Equal(t, "model_not_found", got.Code)

		// 结构化 code 不是 model_not_found 时不带 code。
		got = UpstreamPublicErrorFor(http.StatusBadRequest, []byte(`{"error":{"code":"invalid_value","message":"model not found"}}`))
		require.Empty(t, got.Code)
	})

	t.Run("7 503 / 529：server_is_overloaded", func(t *testing.T) {
		for _, status := range []int{503, 529} {
			got := UpstreamPublicErrorFor(status, []byte(freeBody529))
			require.Equal(t, UpstreamPublicError{HTTPStatus: 503, Type: "upstream_error", Code: "server_is_overloaded", Message: pubOverloaded}, got, status)
		}
	})

	t.Run("7 其余状态码 1:1，Unknown error", func(t *testing.T) {
		for _, status := range []int{402, 403, 404, 408, 409, 413, 422, 429, 500, 502, 504} {
			got := UpstreamPublicErrorFor(status, []byte(freeBody520))
			require.Equal(t, UpstreamPublicError{HTTPStatus: status, Type: "upstream_error", Message: "Unknown error"}, got, status)
		}
		for status, want := range map[int]int{520: 502, 521: 502, 522: 504, 524: 504, 525: 502} {
			require.Equal(t, want, UpstreamPublicErrorFor(status, nil).HTTPStatus, status)
		}
	})

	t.Run("上游 body 不是 JSON（HTML / 纯文本）也不会泄露", func(t *testing.T) {
		got := UpstreamPublicErrorFor(520, []byte(`<html>520: 未知错误 request_id: abc</html>`))
		require.Equal(t, UpstreamPublicError{HTTPStatus: 502, Type: "upstream_error", Message: "Unknown error"}, got)
	})

	t.Run("response.failed 事件形态（error 嵌在 response.error 里）同样识别", func(t *testing.T) {
		got := UpstreamPublicErrorFor(http.StatusBadRequest, []byte(`{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"太长了"}}}`))
		require.Equal(t, "context_length_exceeded", got.Code)
		require.Equal(t, pubContextLimit, got.Message)
	})
}

func TestUpstreamPublicError_JSONBody(t *testing.T) {
	t.Run("code 为空时省略", func(t *testing.T) {
		body := UpstreamPublicError{HTTPStatus: 502, Type: "upstream_error", Message: "Unknown error"}.JSONBody()
		require.JSONEq(t, `{"error":{"message":"Unknown error","type":"upstream_error"}}`, string(body))
		require.False(t, gjson.GetBytes(body, "error.code").Exists())
	})

	t.Run("code 非空时带出", func(t *testing.T) {
		body := UpstreamPublicError{HTTPStatus: 400, Type: "invalid_request_error", Code: "model_not_found", Message: pubInvalidParams}.JSONBody()
		require.JSONEq(t, `{"error":{"message":"`+pubInvalidParams+`","type":"invalid_request_error","code":"model_not_found"}}`, string(body))
	})
}

// 非 failover 的上游 HTTP 错误经 handleCompatErrorResponse 写出：Chat Completions 与 Anthropic Messages
// 都走同一张映射表，只是错误 envelope 不同；上游中文原文和 request_id 一个字都不能出现。
func runCompatUpstreamError(t *testing.T, anthropic bool, status int, body string) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	path := "/v1/chat/completions"
	if anthropic {
		path = "/v1/messages"
	}
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	account := &Account{ID: 7, Name: "free-a6", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	svc := &OpenAIGatewayService{}
	var err error
	if anthropic {
		_, err = svc.handleAnthropicErrorResponse(resp, c, account)
	} else {
		_, err = svc.handleChatCompletionsErrorResponse(resp, c, account)
	}
	require.Error(t, err)
	return rec.Code, rec.Body.String()
}

func TestHandleChatCompletionsErrorResponse_WritesPublicShapeForNonFailoverUpstreamErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantType   string
		wantCode   string
		wantMsg    string
	}{
		{"400", 400, freeBody400, 400, "invalid_request_error", "", pubInvalidParams},
		{"400 code=context_length_exceeded 中文", 400, `{"error":{"message":"请求内容过大，当前模型或商家无法处理。 request_id: 202611071511107613041508268d9d6XnfZJg22","code":"context_length_exceeded"}}`, 400, "invalid_request_error", "context_length_exceeded", pubContextLimit},
		{"404", 404, freeBody404, 404, "upstream_error", "", "Unknown error"},
		{"422", 422, freeBody400, 422, "upstream_error", "", "Unknown error"},
		{"cyber_policy", 400, `{"error":{"code":"cyber_policy","message":"请求被安全策略拦截 request_id: 202611071511107613041508268d9d6XnfZJg22"}}`, 400, "upstream_error", "cyber_policy", "Unknown error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runCompatUpstreamError(t, false, tc.status, tc.body)
			require.Equal(t, tc.wantStatus, code, out)
			require.Equal(t, tc.wantType, gjson.Get(out, "error.type").String(), out)
			require.Equal(t, tc.wantCode, gjson.Get(out, "error.code").String(), out)
			require.Equal(t, tc.wantMsg, gjson.Get(out, "error.message").String(), out)
			require.False(t, containsHan(out), out)
			require.NotContains(t, out, "request_id")
		})
	}

	t.Run("英文 context-window 提示按设计放行", func(t *testing.T) {
		code, out := runCompatUpstreamError(t, false, 400, `{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"invalid_request_error"}}`)
		require.Equal(t, 400, code)
		require.Contains(t, gjson.Get(out, "error.message").String(), "maximum context length")
	})
}

func TestHandleAnthropicErrorResponse_WritesPublicAnthropicEnvelope(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantType   string
		wantMsg    string
	}{
		{"400", 400, freeBody400, 400, "invalid_request_error", pubInvalidParams},
		{"400 code=context_length_exceeded 中文", 400, `{"error":{"message":"请求内容过大，当前模型或商家无法处理。 request_id: 202611071511107613041508268d9d6XnfZJg22","code":"context_length_exceeded"}}`, 400, "invalid_request_error", pubContextLimit},
		{"404", 404, freeBody404, 404, "not_found_error", "Unknown error"},
		{"422", 422, freeBody400, 422, "api_error", "Unknown error"},
		{"cyber_policy", 400, `{"error":{"code":"cyber_policy","message":"请求被安全策略拦截 request_id: 202611071511107613041508268d9d6XnfZJg22"}}`, 400, "invalid_request_error", "Unknown error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runCompatUpstreamError(t, true, tc.status, tc.body)
			require.Equal(t, tc.wantStatus, code, out)
			require.Equal(t, "error", gjson.Get(out, "type").String(), out)
			require.Equal(t, tc.wantType, gjson.Get(out, "error.type").String(), out)
			require.Equal(t, tc.wantMsg, gjson.Get(out, "error.message").String(), out)
			require.False(t, gjson.Get(out, "error.code").Exists(), "Anthropic envelope 没有 error.code: %s", out)
			require.False(t, containsHan(out), out)
			require.NotContains(t, out, "request_id")
		})
	}

	t.Run("英文 context-window 提示按设计放行（Anthropic 格式）", func(t *testing.T) {
		code, out := runCompatUpstreamError(t, true, 400, `{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"invalid_request_error"}}`)
		require.Equal(t, 400, code)
		require.Equal(t, "error", gjson.Get(out, "type").String())
		require.Contains(t, gjson.Get(out, "error.message").String(), "maximum context length")
	})
}

func TestAnthropicPublicErrorBody_TypeMapping(t *testing.T) {
	for status, want := range map[int]string{
		400: "invalid_request_error", 404: "not_found_error", 429: "rate_limit_error",
		503: "overloaded_error", 529: "overloaded_error",
		401: "api_error", 403: "api_error", 413: "api_error", 500: "api_error", 502: "api_error", 504: "api_error",
	} {
		body := anthropicPublicErrorBody(UpstreamPublicError{HTTPStatus: status, Type: "upstream_error", Code: "x", Message: "Unknown error"})
		require.JSONEq(t, `{"type":"error","error":{"type":"`+want+`","message":"Unknown error"}}`, string(body), status)
	}
	// 上游 401 -> 503 -> overloaded_error；上游 529 -> 503 -> overloaded_error。
	require.Equal(t, "overloaded_error", gjson.GetBytes(anthropicPublicErrorBody(UpstreamPublicErrorFor(401, nil)), "error.type").String())
	require.Equal(t, "overloaded_error", gjson.GetBytes(anthropicPublicErrorBody(UpstreamPublicErrorFor(529, nil)), "error.type").String())
}
