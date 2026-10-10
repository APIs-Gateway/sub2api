package service

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
	invalidRequest := false
	for _, field := range []string{"code", "type"} {
		value := strings.ToLower(strings.TrimSpace(errObj.Get(field).String()))
		if openAIPoolAvailabilityCodeMatches(value, extraCodes) {
			return true
		}
		if strings.HasPrefix(value, "invalid_request") {
			invalidRequest = true
		}
	}
	// invalid_request* 会回显用户请求里的参数：文案里恰好含关键词不能当作可用性错误，
	// 否则请求会被放大重试。这类错误只认 code / type。
	if invalidRequest {
		return false
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
	for _, obj := range openAIPoolAvailabilityErrorObjects(payload) {
		if isOpenAIPoolAvailabilityErrorObject(obj, extraCodes) {
			return true
		}
	}
	return false
}

// openAIPoolAvailabilityErrorObjects 返回 payload 里可能承载错误的对象（形态见上）。
func openAIPoolAvailabilityErrorObjects(payload []byte) []gjson.Result {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return nil
	}
	objs := make([]gjson.Result, 0, 3)
	for _, path := range []string{"response.error", "error"} {
		if obj := gjson.GetBytes(payload, path); obj.IsObject() {
			objs = append(objs, obj)
		}
	}
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(payload, "type").String()), "error") &&
		!gjson.GetBytes(payload, "error").Exists() {
		objs = append(objs, gjson.ParseBytes(payload))
	}
	return objs
}

// isOpenAIPoolAvailabilityTimeoutPayload 判断可用性错误是否属于超时类（upstream_timeout /
// 「等待响应超时」）。这类错误每次尝试都可能已经等满了上游自己的超时。
func isOpenAIPoolAvailabilityTimeoutPayload(payload []byte) bool {
	for _, obj := range openAIPoolAvailabilityErrorObjects(payload) {
		for _, field := range []string{"code", "type"} {
			if strings.ToLower(strings.TrimSpace(obj.Get(field).String())) == "upstream_timeout" {
				return true
			}
		}
		if strings.Contains(obj.Get("message").String(), "等待响应超时") {
			return true
		}
	}
	return false
}

// poolAvailabilityTimeoutSameAccountRetryLimit 超时类可用性错误的同号重试上限。
const poolAvailabilityTimeoutSameAccountRetryLimit = 1

// openAIPoolAvailabilityStreamRetryLimit 流内可用性错误的同号重试上限：超时类为 1，
// 其余 0（沿用 pool_mode_retry_count）。
func openAIPoolAvailabilityStreamRetryLimit(account *Account, payload []byte) int {
	if openAIPoolAvailabilityErrorForAccount(account, payload) && isOpenAIPoolAvailabilityTimeoutPayload(payload) {
		return poolAvailabilityTimeoutSameAccountRetryLimit
	}
	return 0
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
	if failoverErr.StatusCode == http.StatusGatewayTimeout || isOpenAIPoolAvailabilityTimeoutPayload(upstreamBody) {
		failoverErr.SameAccountRetryLimit = poolAvailabilityTimeoutSameAccountRetryLimit
	}
	failoverErr.StatusCode = http.StatusBadGateway
	failoverErr.RetryableOnSameAccount = true
	return failoverErr
}

// openAIPoolStageForceCommitBytes 池账号暂存累计到该字节数时强制提交：取 first-output
// 暂存上限（8MB）的一半，避免继续累积后触发暂存溢出 failover。
const openAIPoolStageForceCommitBytes = openAIFirstOutputStageMaxBytes / 2

// openAIPoolStreamDataIsVisibleOutput 池账号口径下「用户看得见」的输出：
//   - 去掉空白后非空的 output_text.delta / output_text.done；
//   - 去掉空白后非空的 reasoning_summary_text.delta / reasoning_text.delta；
//   - 非空的 function_call_arguments.delta / custom_tool_call_input.delta；
//   - 图片生成输出：image_generation_call 的 output_item.added/done、response.image_generation_call.*
//     （含 partial_image）——图片流要和改动前一样一开始就提交，不能靠暂存扛几 MB 的 base64。
//
// 其余（空白文本、只带 encrypted_content 的 reasoning item、output_item.added /
// content_part.added 空壳、web_search_call 状态事件、audio.delta 等）都按结构事件处理。
func openAIPoolStreamDataIsVisibleOutput(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" || !gjson.Valid(trimmed) {
		return false
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = strings.TrimSpace(gjson.Get(trimmed, "type").String())
	}
	switch eventType {
	case "response.output_text.delta",
		"response.reasoning_summary_text.delta",
		"response.reasoning_text.delta":
		return strings.TrimSpace(gjson.Get(trimmed, "delta").String()) != ""
	case "response.output_text.done":
		return strings.TrimSpace(gjson.Get(trimmed, "text").String()) != ""
	case "response.function_call_arguments.delta",
		"response.custom_tool_call_input.delta":
		return gjson.Get(trimmed, "delta").String() != ""
	case "response.output_item.added", "response.output_item.done":
		return gjson.Get(trimmed, "item.type").String() == "image_generation_call"
	}
	return strings.HasPrefix(eventType, "response.image_generation_call.")
}

// openAIPoolStreamDataStartsClientOutput 池账号是否应在该事件上把暂存内容提交给客户端：
// 首个可见输出，或终止 / 失败 / error / 无类型（[DONE] 等）事件（后几类沿用原判定）。
func openAIPoolStreamDataStartsClientOutput(data, eventType string) bool {
	if openAIPoolStreamDataIsVisibleOutput(data, eventType) {
		return true
	}
	et := strings.TrimSpace(eventType)
	if et == "" || et == "error" || openAIStreamEventTypeIsTerminal(et) {
		return openAIStreamDataStartsClientOutput(data, eventType)
	}
	return false
}

// openAIPoolCommitHoldMaxWait 池账号暂存结构事件的最长时间。
//
// 心跳是 SSE 注释行，不会重置 Codex 客户端 300 秒的 stream_idle_timeout（只有解析出的
// SSE 数据事件才会）。旧逻辑在第一个结构事件就发给客户端；池账号现在要等首个可见输出，
// 因此暂存超过该期限（给 300 秒留余量）就按原顺序提交全部暂存事件，保证不会比旧逻辑
// 更早触发客户端空闲超时：旧逻辑在 T1 提交，这里最晚在 max(T1, 该期限) 提交。
const openAIPoolCommitHoldMaxWait = 200 * time.Second

// openAIPoolClientClockKey 在 gin context 里记录客户端视角的计时起点：第一次进入流处理入口
// （无论是否池账号）的时间，之后不覆盖，跨 failover 尝试共用。起点取请求开始而非首个心跳
// 字节，只会更早，更保守。
const openAIPoolClientClockKey = "openai_pool_client_clock_start"

// openAIPoolClientClockStart 返回计时起点；首次调用时以 fallback（该次尝试开始时间）落盘。
// 两个流处理入口不分账号类型都会调用，所以先在非池账号上耗掉的时间也算在内。
func openAIPoolClientClockStart(c *gin.Context, fallback time.Time) time.Time {
	if c == nil {
		return fallback
	}
	if v, ok := c.Get(openAIPoolClientClockKey); ok {
		if t, ok := v.(time.Time); ok && !t.IsZero() {
			return t
		}
	}
	c.Set(openAIPoolClientClockKey, fallback)
	return fallback
}

func openAIPoolHoldExpired(origin time.Time) bool {
	return !origin.IsZero() && time.Since(origin) >= openAIPoolCommitHoldMaxWait
}
