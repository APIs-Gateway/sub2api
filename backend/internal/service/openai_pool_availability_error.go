package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// 池模式（pool_mode）账号的上游本身是商家号池/聚合网关（如 a6）。号池侧「当前没有
// 可用商家 / 链路异常 / 等待超时」属于可用性错误：它与请求内容无关，换一次尝试（同号
// 池内重试、或换号、或换组）就可能成功，不应把失败帧原样交给客户端。
//
// 这类错误有结构化错误码，优先按 error.code / error.type 匹配；中文文案只作兜底。
// 匹配只看 error 对象里的 code / type / message 字段，不对整个 body 做子串匹配。

// credentials 键：账号级追加的可用性错误码（字符串数组），与内置默认列表取并集。
const poolModeAvailabilityErrorCodesCredentialKey = "pool_mode_availability_error_codes"

// defaultOpenAIPoolAvailabilityErrorCodes 内置的可用性错误码（小写精确匹配）。
var defaultOpenAIPoolAvailabilityErrorCodes = []string{
	"smart_route_stream_commit_failure",
	"smart_route_all_candidates_failed",
	"upstream_unavailable",
	"upstream_timeout",
}

// openAIPoolAvailabilityMessageKeywords 无结构化 code 时的中文文案兜底。
var openAIPoolAvailabilityMessageKeywords = []string{
	"流式响应中途失败",
	"智能路由候选均请求失败",
	"服务暂时不可用",
	"等待响应超时",
}

// GetPoolModeAvailabilityErrorCodes 返回账号自定义的可用性错误码（小写、去重、去空）。
// 未配置或类型不符返回 nil；返回值只是对内置默认列表的补充。
func (a *Account) GetPoolModeAvailabilityErrorCodes() []string {
	if a == nil || a.Credentials == nil {
		return nil
	}
	raw, ok := a.Credentials[poolModeAvailabilityErrorCodesCredentialKey]
	if !ok || raw == nil {
		return nil
	}
	var items []string
	switch v := raw.(type) {
	case []string:
		items = v
	case []any:
		items = make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				items = append(items, s)
			}
		}
	default:
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	codes := make([]string, 0, len(items))
	for _, item := range items {
		code := strings.ToLower(strings.TrimSpace(item))
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return nil
	}
	return codes
}

func openAIPoolAvailabilityCodeMatches(value string, extraCodes []string) bool {
	if value == "" {
		return false
	}
	for _, code := range defaultOpenAIPoolAvailabilityErrorCodes {
		if value == code {
			return true
		}
	}
	for _, code := range extraCodes {
		if value == code {
			return true
		}
	}
	return false
}

// isOpenAIPoolAvailabilityErrorObject 判断单个 error 对象是否为池商家可用性错误。
func isOpenAIPoolAvailabilityErrorObject(errObj gjson.Result, extraCodes []string) bool {
	if !errObj.IsObject() {
		return false
	}
	for _, field := range []string{"code", "type"} {
		value := strings.ToLower(strings.TrimSpace(errObj.Get(field).String()))
		if openAIPoolAvailabilityCodeMatches(value, extraCodes) {
			return true
		}
	}
	message := errObj.Get("message").String()
	if message == "" {
		return false
	}
	for _, keyword := range openAIPoolAvailabilityMessageKeywords {
		if strings.Contains(message, keyword) {
			return true
		}
	}
	return false
}

// isOpenAIPoolAvailabilityErrorPayload 判断流内 response.failed / error 事件或 HTTP 错误体
// 是否为池商家可用性错误。payload 形态：
//   - {"type":"response.failed","response":{"error":{...}}}
//   - {"error":{...}}（HTTP 错误体、error 事件）
//   - {"type":"error","code":"...","message":"..."}（扁平 error 事件）
func isOpenAIPoolAvailabilityErrorPayload(payload []byte, extraCodes []string) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	for _, path := range []string{"response.error", "error"} {
		if isOpenAIPoolAvailabilityErrorObject(gjson.GetBytes(payload, path), extraCodes) {
			return true
		}
	}
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(payload, "type").String()), "error") &&
		!gjson.GetBytes(payload, "error").Exists() {
		return isOpenAIPoolAvailabilityErrorObject(gjson.ParseBytes(payload), extraCodes)
	}
	return false
}

// openAIPoolAvailabilityErrorForAccount 只对池模式账号生效；非池账号恒为 false，行为不变。
func openAIPoolAvailabilityErrorForAccount(account *Account, payload []byte) bool {
	if account == nil || !account.IsPoolMode() {
		return false
	}
	return isOpenAIPoolAvailabilityErrorPayload(payload, account.GetPoolModeAvailabilityErrorCodes())
}

// openAIPoolAvailabilityHTTPErrorForAccount 判断池账号收到的 HTTP 错误响应是否为可用性错误。
// a6 会用 400/403/404/502/504 携带这类错误体，其中 400/403/404 默认不会 failover。
func openAIPoolAvailabilityHTTPErrorForAccount(account *Account, statusCode int, body []byte) bool {
	switch statusCode {
	case http.StatusBadRequest,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusBadGateway,
		http.StatusGatewayTimeout:
	default:
		return false
	}
	return openAIPoolAvailabilityErrorForAccount(account, body)
}

// applyOpenAIPoolAvailabilityFailover 把命中的 HTTP failover 错误规范化：
// 状态码统一为 502（耗尽后客户端拿到通用 502 文案，不透出上游原文；链分类判为可回退），
// 并允许池内同号重试。ops 里的原始上游状态在构造 failover 错误之前已经写入，不受影响。
// 不命中（含非池账号）原样返回。
func applyOpenAIPoolAvailabilityFailover(account *Account, failoverErr *UpstreamFailoverError, upstreamBody []byte) *UpstreamFailoverError {
	if failoverErr == nil || !openAIPoolAvailabilityHTTPErrorForAccount(account, failoverErr.StatusCode, upstreamBody) {
		return failoverErr
	}
	failoverErr.StatusCode = http.StatusBadGateway
	failoverErr.RetryableOnSameAccount = true
	return failoverErr
}
