package service

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

const errorPassthroughServiceContextKey = "error_passthrough_service"

var (
	clientVisibleInputPathRegex  = regexp.MustCompile(`\binput\[\d+\](?:\.[A-Za-z0-9_]+)*`)
	clientVisibleCallIDRegex     = regexp.MustCompile(`\bcall_[A-Za-z0-9_-]+\b`)
	clientVisibleResponseIDRegex = regexp.MustCompile(`\brs_[A-Za-z0-9_-]+\b`)
	clientVisibleRequestIDRegex  = regexp.MustCompile(`(?i)\brequest[_-]?id\s*[:=]\s*[A-Za-z0-9._:-]+`)
)

// IsRequestShapedUpstream4xx 判定上游 4xx 是否属于「请求本身的问题」(换账号也救不了)。
// 这是 issue #16 Part B 的单一真相:既决定「默认透传哪些」,也(在 B3/A2)决定「归因到 client 的是哪些」。
// 明确排除 401/403/407(鉴权)、402(计费)、429(限流)——那些是账号/上游侧,保留 502/429 语义。
func IsRequestShapedUpstream4xx(status int) bool {
	switch status {
	case 400, 404, 408, 409, 413, 415, 416, 422:
		return true
	}
	return false
}

// upstreamErrTypeForStatus 给请求形上游 4xx 选一个客户端可理解的 errType。
func upstreamErrTypeForStatus(status int) string {
	if status == http.StatusNotFound {
		return "not_found_error"
	}
	return "invalid_request_error"
}

func upstreamClientMessageForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "Invalid request parameters, please check the request body and try again"
	case http.StatusNotFound:
		return "Requested upstream resource was not found"
	case http.StatusRequestTimeout:
		return "Upstream request timed out, please retry later"
	case http.StatusConflict:
		return "Request conflicts with upstream state, please retry or start a new request"
	case http.StatusRequestEntityTooLarge:
		return "Request body is too large, please reduce the input size and try again"
	case http.StatusUnsupportedMediaType:
		return "Unsupported request content type"
	case http.StatusRequestedRangeNotSatisfiable:
		return "Requested range is not satisfiable"
	case http.StatusUnprocessableEntity:
		return "Invalid request parameters, please check the request body and try again"
	default:
		return "Upstream rejected the request"
	}
}

func sanitizeClientVisibleUpstreamMessage(msg string) string {
	msg = strings.TrimSpace(sanitizeUpstreamErrorMessage(msg))
	if msg == "" {
		return msg
	}
	msg = clientVisibleInputPathRegex.ReplaceAllString(msg, "request input field")
	msg = clientVisibleCallIDRegex.ReplaceAllString(msg, "tool call id")
	msg = clientVisibleResponseIDRegex.ReplaceAllString(msg, "response item id")
	msg = clientVisibleRequestIDRegex.ReplaceAllString(msg, "request_id: ***")
	replacements := []struct {
		old string
		new string
	}{
		{"固定商家", "当前路由"},
		{"智能路由", "路由"},
		{"商家", "路由"},
	}
	for _, replacement := range replacements {
		msg = strings.ReplaceAll(msg, replacement.old, replacement.new)
	}
	return msg
}

// MapUpstreamErrorDefault 是「上游 status → 对外默认响应」的单一真相(规则未命中时的兜底)。
// passthrough=true 表示允许调用方使用脱敏后的上游 body message。默认规则目前不开放 body 透传。
// 非 4xx(401/403/402/429/5xx/529/未知)保留既有 502/429/503 语义,与旧 mapUpstreamError 等价。
func MapUpstreamErrorDefault(upstreamStatus int) (status int, errType, msg string, passthrough bool) {
	switch upstreamStatus {
	case 401:
		return http.StatusBadGateway, "upstream_error", "Upstream authentication failed, please contact administrator", false
	case 402:
		return http.StatusBadGateway, "upstream_error", "Upstream payment required: insufficient balance or billing issue", false
	case 403:
		return http.StatusBadGateway, "upstream_error", "Upstream access forbidden, please contact administrator", false
	case 429:
		return http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later", false
	case 529:
		return http.StatusServiceUnavailable, "overloaded_error", "Upstream service overloaded, please retry later", false
	case 500, 502, 503, 504:
		return http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable", false
	}
	if IsRequestShapedUpstream4xx(upstreamStatus) {
		return upstreamStatus, upstreamErrTypeForStatus(upstreamStatus), upstreamClientMessageForStatus(upstreamStatus), false
	}
	return http.StatusBadGateway, "upstream_error", "Upstream request failed", false
}

// ResolveUpstreamErrorResponse 是所有入口共享的「上游错误 → 对外响应」策略(issue #16 Part B)。
// 顺序:① OpenAI 静默拒绝特判 → ② 记录真实上游状态(ops/A2 归因依赖)→ ③ 透传规则命中按规则
// → ④ 默认映射(请求形 4xx 保留状态码但不默认暴露上游 body)。各入口拿到 (status, errType, message) 后用自己的平台 writer 写出。
func ResolveUpstreamErrorResponse(c *gin.Context, platform string, upstreamStatus int, responseBody []byte) (status int, errType, message string) {
	// ① 静默拒绝:保留既有 502 + 客户端友好文案,不进透传。
	if IsOpenAISilentRefusalErrorBody(responseBody) {
		SetOpsUpstreamError(c, upstreamStatus, OpenAISilentRefusalClientMessage(), "")
		return http.StatusBadGateway, "upstream_error", OpenAISilentRefusalClientMessage()
	}

	upstreamMsg := ExtractUpstreamErrorMessage(responseBody)
	// ② 记录真实上游状态码,便于 ops 错误日志捕获(A2 归因据此区分 client-caused upstream 4xx)。
	SetOpsUpstreamError(c, upstreamStatus, upstreamMsg, "")

	// ③ 透传规则命中则按规则改写(code/body/custom/skip)。
	if svc := getBoundErrorPassthroughService(c); svc != nil && len(responseBody) > 0 {
		if rule := svc.MatchRule(platform, upstreamStatus, responseBody); rule != nil {
			respCode := upstreamStatus
			if !rule.PassthroughCode && rule.ResponseCode != nil {
				respCode = *rule.ResponseCode
			}
			msg := sanitizeClientVisibleUpstreamMessage(upstreamMsg)
			if !rule.PassthroughBody && rule.CustomMessage != nil {
				msg = *rule.CustomMessage
			}
			if rule.SkipMonitoring {
				c.Set(OpsSkipPassthroughKey, true)
			}
			return respCode, "upstream_error", msg
		}
	}

	// ④ 默认映射:请求形 4xx 保留真实状态码 + 安全文案,其余保留 502/429/503。
	status, errType, message, passthrough := MapUpstreamErrorDefault(upstreamStatus)
	if passthrough {
		message = sanitizeClientVisibleUpstreamMessage(upstreamMsg)
		if strings.TrimSpace(message) == "" {
			message = "Upstream rejected the request"
		}
	}
	return status, errType, message
}

// BindErrorPassthroughService 将错误透传服务绑定到请求上下文，供 service 层在非 failover 场景下复用规则。
func BindErrorPassthroughService(c *gin.Context, svc *ErrorPassthroughService) {
	if c == nil || svc == nil {
		return
	}
	c.Set(errorPassthroughServiceContextKey, svc)
}

func getBoundErrorPassthroughService(c *gin.Context) *ErrorPassthroughService {
	if c == nil {
		return nil
	}
	v, ok := c.Get(errorPassthroughServiceContextKey)
	if !ok {
		return nil
	}
	svc, ok := v.(*ErrorPassthroughService)
	if !ok {
		return nil
	}
	return svc
}

// applyErrorPassthroughRule 按规则改写错误响应；未命中时返回默认响应参数。
func applyErrorPassthroughRule(
	c *gin.Context,
	platform string,
	upstreamStatus int,
	responseBody []byte,
	defaultStatus int,
	defaultErrType string,
	defaultErrMsg string,
) (status int, errType string, errMsg string, matched bool) {
	status = defaultStatus
	errType = defaultErrType
	errMsg = defaultErrMsg

	svc := getBoundErrorPassthroughService(c)
	if svc == nil {
		return status, errType, errMsg, false
	}

	rule := svc.MatchRule(platform, upstreamStatus, responseBody)
	if rule == nil {
		return status, errType, errMsg, false
	}

	status = upstreamStatus
	if !rule.PassthroughCode && rule.ResponseCode != nil {
		status = *rule.ResponseCode
	}

	errMsg = sanitizeClientVisibleUpstreamMessage(ExtractUpstreamErrorMessage(responseBody))
	if !rule.PassthroughBody && rule.CustomMessage != nil {
		errMsg = *rule.CustomMessage
	}

	// 命中 skip_monitoring 时在 context 中标记，供 ops_error_logger 跳过记录。
	if rule.SkipMonitoring {
		c.Set(OpsSkipPassthroughKey, true)
	}

	// 与现有 failover 场景保持一致：命中规则时统一返回 upstream_error。
	errType = "upstream_error"
	return status, errType, errMsg, true
}
