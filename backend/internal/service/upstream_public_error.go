package service

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// UpstreamPublicError 是一次上游失败对外呈现的形态（OpenAI 错误 envelope）。
//
// 上游（含二级中转）的 message / request_id / 商家措辞一律不出网关：这里只产出固定英文文案，
// 上游原文继续由 SetOpsUpstreamError / appendOpsUpstreamError 记进 ops 错误日志。
// 只用于「本次失败确实来自上游」的写出点；网关自己的鉴权、校验、限流、余额、无可用账号等
// 文案不经过这里。
type UpstreamPublicError struct {
	// HTTPStatus 为 0 表示「不覆盖」，调用方沿用原逻辑。
	HTTPStatus int
	Type       string
	// Code 可为空。
	Code    string
	Message string
}

const (
	upstreamPublicTypeUpstreamError   = "upstream_error"
	upstreamPublicTypeInvalidRequest  = "invalid_request_error"
	upstreamPublicMessageOverloaded   = "Upstream service temporarily unavailable"
	upstreamPublicMessageContextLimit = "Your input exceeds the context window of this model. Please adjust your input and try again."
	upstreamPublicCodeModelNotFound   = "model_not_found"
	upstreamPublicCodeBioPolicy       = "bio_policy"
)

// UpstreamPublicErrorFor 把一次上游失败映射成可以安全对外返回的错误形态。
//
// 判定顺序（命中即返回）：
//  1. 上游给出策略类 code（cyber_policy / misalignment_policy_violation / bio_policy）。
//  2. 上游额度类 error.type（usage_limit_reached / usage_not_included）。
//  3. 上游 message 本身就是英文 context-window 提示：不覆盖，保留既有放行逻辑。
//  4. 上游 code 为 context_length_exceeded 但 message 不是上面那种英文（例如中文）。
//  5. 上游 401：对外 503（不让客户端以为是自己的凭证失效）。
//  6. 状态码不在 400..599（传输错误 / 断流 / 未知）：不覆盖。
//  7. 其余按状态码 1:1 保留（仅未注册的非标准码按 codexCanonicalStatus 换成 502/503/504）。
func UpstreamPublicErrorFor(upstreamStatus int, upstreamBody []byte) UpstreamPublicError {
	code := strings.TrimSpace(codexUpstreamErrorField(upstreamBody, "code").String())
	lowerCode := strings.ToLower(code)

	// 1. 策略类 code。上游状态码缺失 / 不是错误码（例如藏在 HTTP 200 SSE 里）时按 400 处理：
	// 这类拒绝是终局的，不能给客户端一个会触发自动重试的 5xx。
	switch lowerCode {
	case CodexErrCodeCyberPolicy, CodexErrCodeMisalignmentPolicy, upstreamPublicCodeBioPolicy:
		return UpstreamPublicError{
			HTTPStatus: upstreamPublicErrorStatus(upstreamStatus),
			Type:       upstreamPublicTypeUpstreamError,
			Code:       code,
			Message:    codexUnknownErrorMessage,
		}
	}

	// 2. 额度类：不带上游 message（里面常有套餐 / 账号 / 重置时间等上游内部信息）。
	switch codexUpstreamErrorType(upstreamBody) {
	case codexErrTypeUsageLimitReached, codexErrTypeUsageNotIncluded:
		return UpstreamPublicError{
			HTTPStatus: http.StatusTooManyRequests,
			Type:       codexUpstreamErrorType(upstreamBody),
			Message:    codexUnknownErrorMessage,
		}
	}

	// 3. 上游 message 本身就是英文 context-window 提示：对客户端有用，保留原样。
	if isOpenAIContextWindowError(codexUpstreamErrorField(upstreamBody, "message").String(), nil) {
		return UpstreamPublicError{}
	}

	// 4. 只有 code 命中、message 不是英文提示（中文等）：换成固定英文文案，保留 code 让 SDK 能识别。
	if lowerCode == CodexErrCodeContextLengthExceeded {
		return UpstreamPublicError{
			HTTPStatus: upstreamPublicErrorStatus(upstreamStatus),
			Type:       upstreamPublicTypeInvalidRequest,
			Code:       CodexErrCodeContextLengthExceeded,
			Message:    upstreamPublicMessageContextLimit,
		}
	}

	// 5. 上游 401 是网关侧账号鉴权问题，对外 503。
	if upstreamStatus == http.StatusUnauthorized {
		return upstreamPublicOverloaded()
	}

	// 6. 传输错误 / 断流 / 未知：这类文案本来就是网关固定文案。
	if upstreamStatus < 400 || upstreamStatus > 599 {
		return UpstreamPublicError{}
	}

	// 7. 其余：状态码 1:1。
	switch status := codexCanonicalStatus(upstreamStatus); status {
	case http.StatusBadRequest:
		out := UpstreamPublicError{
			HTTPStatus: status,
			Type:       upstreamPublicTypeInvalidRequest,
			Message:    upstreamClientMessageForStatus(http.StatusBadRequest),
		}
		if IsOpenAICompatibleModelNotFound400(upstreamBody) {
			out.Code = upstreamPublicCodeModelNotFound
		}
		return out
	case http.StatusServiceUnavailable:
		return upstreamPublicOverloaded()
	default:
		return UpstreamPublicError{
			HTTPStatus: status,
			Type:       upstreamPublicTypeUpstreamError,
			Message:    codexUnknownErrorMessage,
		}
	}
}

func upstreamPublicOverloaded() UpstreamPublicError {
	return UpstreamPublicError{
		HTTPStatus: http.StatusServiceUnavailable,
		Type:       upstreamPublicTypeUpstreamError,
		Code:       CodexErrCodeServerOverloaded,
		Message:    upstreamPublicMessageOverloaded,
	}
}

// upstreamPublicErrorStatus 给规则 1 / 4 取状态码：错误码区间内 1:1（非标准码按注册表换算），
// 区间外（例如 HTTP 200 SSE 里携带的错误）按 400。
func upstreamPublicErrorStatus(upstreamStatus int) int {
	if upstreamStatus < 400 || upstreamStatus > 599 {
		return http.StatusBadRequest
	}
	return codexCanonicalStatus(upstreamStatus)
}

type upstreamPublicErrorEnvelope struct {
	Error upstreamPublicErrorPayload `json:"error"`
}

type upstreamPublicErrorPayload struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// JSONBody 渲染成 {"error":{"message":..,"type":..,"code":..}}，code 为空时省略。
func (e UpstreamPublicError) JSONBody() []byte {
	body, err := json.Marshal(upstreamPublicErrorEnvelope{Error: upstreamPublicErrorPayload{
		Message: e.Message,
		Type:    e.Type,
		Code:    e.Code,
	}})
	if err != nil {
		return []byte(`{"error":{"message":"` + codexUnknownErrorMessage + `","type":"` + upstreamPublicTypeUpstreamError + `"}}`)
	}
	return body
}

// compatPublicErrorWriter 把 UpstreamPublicError 按某个对外协议的错误格式写出（非流式）。
type compatPublicErrorWriter func(c *gin.Context, pub UpstreamPublicError)

// writeCompatPublicUpstreamError 在 writer 非 nil 时用 UpstreamPublicErrorFor 写出上游失败，返回是否已写出。
// 返回 false（未启用，或该类错误按设计不覆盖）时调用方沿用原有写出逻辑。
func writeCompatPublicUpstreamError(c *gin.Context, writer compatPublicErrorWriter, upstreamStatus int, upstreamBody []byte) bool {
	if writer == nil {
		return false
	}
	pub := UpstreamPublicErrorFor(upstreamStatus, upstreamBody)
	if pub.HTTPStatus <= 0 {
		return false
	}
	writer(c, pub)
	return true
}

// anthropicErrorTypeForPublicStatus 给 Anthropic 错误 envelope 选 error.type。
func anthropicErrorTypeForPublicStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable, 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// anthropicPublicErrorBody 渲染 {"type":"error","error":{"type":..,"message":..}}。
// Anthropic 协议没有 error.code，UpstreamPublicError.Code 不写出。
func anthropicPublicErrorBody(pub UpstreamPublicError) []byte {
	body, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    anthropicErrorTypeForPublicStatus(pub.HTTPStatus),
			"message": pub.Message,
		},
	})
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"api_error","message":"` + codexUnknownErrorMessage + `"}}`)
	}
	return body
}

// writeAnthropicPublicError 把 UpstreamPublicError 按 Anthropic 错误格式写出（非流式）。
func writeAnthropicPublicError(c *gin.Context, pub UpstreamPublicError) {
	MarkResponseCommitted(c)
	c.Data(pub.HTTPStatus, "application/json; charset=utf-8", anthropicPublicErrorBody(pub))
}
