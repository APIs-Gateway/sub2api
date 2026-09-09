package service

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// upstreamRequestIDHeaderNames 上游 request id 的兼容头名，按优先级取第一个非空。
// 第三方中转不一定发 x-request-id：one-api / new-api 系（a6api、rivoapi）发 x-oneapi-request-id，
// rix-api 系（platform.ephone.chat）发 x-rixapi-request-id，Bedrock 发 x-amzn-requestid，
// 过 Cloudflare 的至少有 cf-ray。取不到就无法向厂商追责。
var upstreamRequestIDHeaderNames = []string{
	"x-request-id",
	"x-oneapi-request-id",
	"x-rixapi-request-id",
	"x-amzn-requestid",
	"cf-ray",
}

// upstreamRequestIDFromHeader 从上游响应头取 request id（兼容多家中转的头名），nil / 都为空返回 ""。
// 只用于读上游响应；客户端请求头 / 回写客户端的透传头不经此函数。
func upstreamRequestIDFromHeader(h http.Header) string {
	if h == nil {
		return ""
	}
	for _, name := range upstreamRequestIDHeaderNames {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

const (
	// upstreamRequestIDFromErrorBodyMaxBytes 超过此长度的错误体不解析（避免对大响应体做 JSON 扫描）。
	upstreamRequestIDFromErrorBodyMaxBytes = 64 * 1024
	// upstreamRequestIDMaxBytes 从错误体提取的 request id 截断长度。
	upstreamRequestIDMaxBytes = 128
)

// upstreamRequestIDInMessageRe 兜底：New API 系中转把 request id 拼在 message 末尾
// （"... request_id: 2026091312450379..."）。
var upstreamRequestIDInMessageRe = regexp.MustCompile(`request_id:\s*([A-Za-z0-9_.-]{8,})`)

// upstreamRequestIDFromErrorBody 从上游错误 JSON 体里兜底取 request id：依次 error.request_id、
// 顶层 request_id（字符串且非空），都没有再用正则在 error.message 里找 "request_id: xxx"。
// a6 这类 New API 中转响应头里没有 request id，只在错误体里带；头里取不到时用这里的值。
// 非 JSON / 空串 / 超过 64KB 返回 ""；返回值截到 128 字节。
func upstreamRequestIDFromErrorBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > upstreamRequestIDFromErrorBodyMaxBytes || !gjson.Valid(body) {
		return ""
	}
	for _, path := range []string{"error.request_id", "request_id"} {
		if r := gjson.Get(body, path); r.Type == gjson.String {
			if v := strings.TrimSpace(r.String()); v != "" {
				return truncateString(v, upstreamRequestIDMaxBytes)
			}
		}
	}
	if m := upstreamRequestIDInMessageRe.FindStringSubmatch(gjson.Get(body, "error.message").String()); len(m) == 2 {
		return truncateString(m[1], upstreamRequestIDMaxBytes)
	}
	return ""
}

// opsUpstreamHeaderFingerprintNames 记进 ops 事件的上游响应头白名单：用于识别中转实现与追责
// （server / via / new-api 版本 / 各家 request id / cf-ray），不含任何凭证或 cookie。
var opsUpstreamHeaderFingerprintNames = []string{
	"server",
	"x-new-api-version",
	"cf-ray",
	"x-oneapi-request-id",
	"x-rixapi-request-id",
	"x-request-id",
	"x-amzn-requestid",
	"via",
}

const opsUpstreamHeaderFingerprintValueMaxBytes = 128

// opsUpstreamHeaderFingerprint 按白名单摘取非空上游响应头（值 TrimSpace 并截到 128 字节），
// 一个都没有 / nil 返回 nil，序列化时 omitempty 不占字段。
func opsUpstreamHeaderFingerprint(h http.Header) map[string]string {
	if h == nil {
		return nil
	}
	var out map[string]string
	for _, name := range opsUpstreamHeaderFingerprintNames {
		v := strings.TrimSpace(h.Get(name))
		if v == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(opsUpstreamHeaderFingerprintNames))
		}
		out[name] = truncateString(v, opsUpstreamHeaderFingerprintValueMaxBytes)
	}
	return out
}

// Gin context keys used by Ops error logger for capturing upstream error details.
// These keys are set by gateway services and consumed by handler/ops_error_logger.go.
const (
	OpsUpstreamStatusCodeKey   = "ops_upstream_status_code"
	OpsUpstreamErrorMessageKey = "ops_upstream_error_message"
	OpsUpstreamErrorDetailKey  = "ops_upstream_error_detail"
	OpsUpstreamErrorsKey       = "ops_upstream_errors"

	// Optional stage latencies (milliseconds) for troubleshooting and alerting.
	OpsAuthLatencyMsKey      = "ops_auth_latency_ms"
	OpsRoutingLatencyMsKey   = "ops_routing_latency_ms"
	OpsUpstreamLatencyMsKey  = "ops_upstream_latency_ms"
	OpsResponseLatencyMsKey  = "ops_response_latency_ms"
	OpsTimeToFirstTokenMsKey = "ops_time_to_first_token_ms"
	// OpenAI WS 关键观测字段
	OpsOpenAIWSQueueWaitMsKey = "ops_openai_ws_queue_wait_ms"
	OpsOpenAIWSConnPickMsKey  = "ops_openai_ws_conn_pick_ms"
	OpsOpenAIWSConnReusedKey  = "ops_openai_ws_conn_reused"
	OpsOpenAIWSConnIDKey      = "ops_openai_ws_conn_id"

	// OpsSkipPassthroughKey 由 applyErrorPassthroughRule 在命中 skip_monitoring=true 的规则时设置。
	// ops_error_logger 中间件检查此 key，为 true 时跳过错误记录。
	OpsSkipPassthroughKey = "ops_skip_passthrough"

	// OpsStreamErrorKey 保存 handleStreamingAwareError 在「响应已固化为 HTTP 200 的 SSE 流」
	// 上就地(in-band)补发错误帧时记录的 OpsStreamError。因为 wire 状态码停留在 200，
	// ops_error_logger 的 status>=400 采集路径永远不会触发，这类流内失败
	//（例如等待并发槽位超时后回退的限流、Wait 后二次计费校验失败）本会在错误看板里隐形。
	OpsStreamErrorKey = "ops_stream_error"

	// Client-side configuration denials should remain visible in ops_error_logs,
	// but should be excluded from SLA/error-rate calculations.
	// ResponseCommittedKey 由 handleErrorResponse 系列函数在写完 HTTP 错误响应后设置。
	// ensureForwardErrorResponse 检查此 key，为 true 时跳过兜底写入，避免在已完成的 JSON 后追加 SSE。
	ResponseCommittedKey = "response_committed"

	OpsClientBusinessLimitedKey                           = "ops_client_business_limited"
	OpsClientBusinessLimitedReasonKey                     = "ops_client_business_limited_reason"
	OpsClientBusinessLimitedReasonIPRestriction           = "api_key_ip_restriction"
	OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable  = "api_key_group_unavailable"
	OpsClientBusinessLimitedReasonAPIKeyGroupUnassigned   = "api_key_group_unassigned"
	OpsClientBusinessLimitedReasonLocalFeatureGate        = "local_feature_gate"
	OpsClientBusinessLimitedReasonLocalPolicyDenied       = "local_policy_denied"
	OpsClientBusinessLimitedReasonLocalModelConfiguration = "local_model_configuration"
)

func MarkResponseCommitted(c *gin.Context) { c.Set(ResponseCommittedKey, true) }

func IsResponseCommitted(c *gin.Context) bool {
	v, ok := c.Get(ResponseCommittedKey)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func SetOpsLatencyMs(c *gin.Context, key string, value int64) {
	if c == nil || strings.TrimSpace(key) == "" || value < 0 {
		return
	}
	c.Set(key, value)
}

func MarkOpsClientBusinessLimited(c *gin.Context, reason string) {
	if c == nil {
		return
	}
	c.Set(OpsClientBusinessLimitedKey, true)
	if reason = strings.TrimSpace(reason); reason != "" {
		c.Set(OpsClientBusinessLimitedReasonKey, reason)
	}
}

func HasOpsClientBusinessLimited(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsClientBusinessLimitedKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

func OpsClientBusinessLimitedReason(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, ok := c.Get(OpsClientBusinessLimitedReasonKey)
	if !ok {
		return ""
	}
	reason, _ := v.(string)
	return strings.TrimSpace(reason)
}

// OpsStreamError 描述承载在 2xx 响应上的带内错误：网关在响应状态已固化为 200 之后
// 就地以 SSE error 帧返回的错误（并发限流回退、Wait 后二次计费校验失败、流开始后才无
// 可用账号等），以及上游 2xx 正文或事件里携带的错误结果（NonStream 标记非流式正文）。
// 由于 HTTP 状态码停留在 2xx，ops_error_logger 中间件在 status<400 分支消费该标记并
// 补记错误日志；标记方是 handler.handleStreamingAwareError 或各 service 的带内检测。
type OpsStreamError struct {
	// ErrType 是写入 SSE 帧的对客错误类型（如 rate_limit_error / upstream_error / api_error）。
	ErrType string
	// Code 是可选的稳定错误分类；用于既保留通用 OpenAI error.type，又向客户端和 Ops
	// 暴露可编程判断的细分类（如 upstream_http2_stream_error）。
	Code string
	// Message 是写入 SSE 帧的对客错误消息。
	Message string
	// IntendedStatus 是流若未固化本应返回的 HTTP 状态码（如并发限流的 429）。
	// 默认仅用于错误分级；CountTowardsSLA 或 RequestScoped 为 true 时也作为 Ops 的逻辑状态码。
	IntendedStatus int
	// CountTowardsSLA 表示虽然 wire 状态已固化为 200，请求在应用语义上仍然失败，
	// Ops 应使用 IntendedStatus 计入错误率/SLA。
	CountTowardsSLA bool
	// RequestScoped 表示该带内失败是请求级结果（如上游内容策略截停），与本请求此前的
	// 上游尝试无关：分类不受上游错误上下文影响、不落库上游归因、不继承透传规则的
	// skip_monitoring，按业务限制计，落库状态取 IntendedStatus 以便进入错误列表。
	// 本请求此前的上游尝试（若有）仍按恢复行另记一条。
	RequestScoped bool
	// NonStream 表示带内信号来自非流式 2xx 响应体，落库 stream=false。
	NonStream bool
	// UpstreamAttributed 表示该带内失败本身就是上游失败（如 2xx 正文里的错误信封、空响应），
	// 标记方已写入上游错误上下文：即使存在上游错误上下文，也按带内错误落库并带上上游归因，
	// 而不是记成「Recovered upstream error」恢复行。
	UpstreamAttributed bool
}

// MarkOpsStreamError 记录一次就地 SSE 错误，供 ops 日志采集。
// 采用「首个标记生效」策略：同一请求若先后补发多帧（如上游透传错误后又追加通用兜底帧），
// 保留最先记录的根因错误，而不是被后续的 "Upstream request failed" 覆盖。
func MarkOpsStreamError(c *gin.Context, errType, message string, intendedStatus int) {
	markOpsStreamError(c, OpsStreamError{
		ErrType:        errType,
		Message:        message,
		IntendedStatus: intendedStatus,
	})
}

// MarkOpsStreamFailure records an in-band stream error that represents a failed
// request and therefore must count towards Ops error rate/SLA despite HTTP 200
// already being committed on the wire.
func MarkOpsStreamFailure(c *gin.Context, errType, code, message string, intendedStatus int) {
	markOpsStreamError(c, OpsStreamError{
		ErrType:         errType,
		Code:            code,
		Message:         message,
		IntendedStatus:  intendedStatus,
		CountTowardsSLA: true,
	})
}

// MarkOpsStreamErrorValue 以完整的 OpsStreamError 记录一次带内错误，供需要
// RequestScoped / NonStream / UpstreamAttributed 等附加语义的调用方使用；首个标记生效的规则不变。
func MarkOpsStreamErrorValue(c *gin.Context, streamErr OpsStreamError) {
	markOpsStreamError(c, streamErr)
}

func markOpsStreamError(c *gin.Context, streamErr OpsStreamError) {
	if c == nil {
		return
	}
	if _, exists := c.Get(OpsStreamErrorKey); exists {
		return
	}
	streamErr.ErrType = strings.TrimSpace(streamErr.ErrType)
	streamErr.Code = strings.TrimSpace(streamErr.Code)
	streamErr.Message = strings.TrimSpace(streamErr.Message)
	c.Set(OpsStreamErrorKey, streamErr)
}

// GetOpsStreamError 返回本请求记录的就地 SSE 错误（若有）。
func GetOpsStreamError(c *gin.Context) (OpsStreamError, bool) {
	if c == nil {
		return OpsStreamError{}, false
	}
	v, ok := c.Get(OpsStreamErrorKey)
	if !ok {
		return OpsStreamError{}, false
	}
	se, ok := v.(OpsStreamError)
	return se, ok
}

// SetOpsUpstreamError is the exported wrapper for setOpsUpstreamError, used by
// handler-layer code (e.g. failover-exhausted paths) that needs to record the
// original upstream status code before mapping it to a client-facing code.
func SetOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	setOpsUpstreamError(c, upstreamStatusCode, upstreamMessage, upstreamDetail)
}

func setOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	if c == nil {
		return
	}
	if upstreamStatusCode > 0 {
		c.Set(OpsUpstreamStatusCodeKey, upstreamStatusCode)
	}
	if msg := strings.TrimSpace(upstreamMessage); msg != "" {
		c.Set(OpsUpstreamErrorMessageKey, msg)
	}
	if detail := strings.TrimSpace(upstreamDetail); detail != "" {
		c.Set(OpsUpstreamErrorDetailKey, detail)
	}
}

// OpsUpstreamErrorEvent describes one upstream error attempt during a single gateway request.
// It is stored in ops_error_logs.upstream_errors as a JSON array.
type OpsUpstreamErrorEvent struct {
	AtUnixMs int64 `json:"at_unix_ms,omitempty"`

	// Passthrough 表示本次请求是否命中“原样透传（仅替换认证）”分支。
	// 该字段用于排障与灰度评估；存入 JSON，不涉及 DB schema 变更。
	Passthrough bool `json:"passthrough,omitempty"`

	// Context
	Platform    string `json:"platform,omitempty"`
	AccountID   int64  `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`

	// Outcome
	UpstreamStatusCode int    `json:"upstream_status_code,omitempty"`
	UpstreamRequestID  string `json:"upstream_request_id,omitempty"`

	// UpstreamHeaders 上游响应头指纹（白名单：server / x-new-api-version / cf-ray / 各家 request id / via），
	// 用于识别中转实现与向厂商追责。目前只在上游模型不一致事件里填充。
	UpstreamHeaders map[string]string `json:"upstream_headers,omitempty"`

	// UpstreamURL is the actual upstream URL that was called (host + path, query/fragment stripped).
	// Helps debug 404/routing errors by showing which endpoint was targeted.
	UpstreamURL string `json:"upstream_url,omitempty"`

	// Best-effort upstream response capture (sanitized+trimmed).
	UpstreamResponseBody string `json:"upstream_response_body,omitempty"`

	// Kind: http_error | request_error | retry_exhausted | failover
	Kind string `json:"kind,omitempty"`

	// FailoverRecovered is set when this upstream error was covered by a later
	// successful attempt in the same client request.
	FailoverRecovered bool `json:"failover_recovered"`

	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func appendOpsUpstreamError(c *gin.Context, ev OpsUpstreamErrorEvent) {
	if c == nil {
		return
	}
	if ev.AtUnixMs <= 0 {
		ev.AtUnixMs = time.Now().UnixMilli()
	}
	ev.Platform = strings.TrimSpace(ev.Platform)
	ev.UpstreamRequestID = strings.TrimSpace(ev.UpstreamRequestID)
	ev.UpstreamResponseBody = strings.TrimSpace(ev.UpstreamResponseBody)
	ev.Kind = strings.TrimSpace(ev.Kind)
	ev.UpstreamURL = strings.TrimSpace(ev.UpstreamURL)
	ev.Message = strings.TrimSpace(ev.Message)
	ev.Detail = strings.TrimSpace(ev.Detail)
	// 头里没有 request id 时从错误体兜底（New API 系中转只在错误 JSON 里带）；头里有值不覆盖。
	if ev.UpstreamRequestID == "" {
		ev.UpstreamRequestID = firstNonEmpty(upstreamRequestIDFromErrorBody(ev.UpstreamResponseBody), upstreamRequestIDFromErrorBody(ev.Detail))
	}
	if ev.Message != "" {
		ev.Message = sanitizeUpstreamErrorMessage(ev.Message)
	}

	var existing []*OpsUpstreamErrorEvent
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if arr, ok := v.([]*OpsUpstreamErrorEvent); ok {
			existing = arr
		}
	}

	evCopy := ev
	existing = append(existing, &evCopy)
	c.Set(OpsUpstreamErrorsKey, existing)

	checkSkipMonitoringForUpstreamEvent(c, &evCopy)
}

func MarkOpsUpstreamFailoverRecovered(c *gin.Context) {
	if c == nil {
		return
	}
	v, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return
	}
	events, ok := v.([]*OpsUpstreamErrorEvent)
	if !ok {
		return
	}
	for _, event := range events {
		if event == nil {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(event.Kind))
		if kind == "failover" || strings.Contains(kind, "failover") {
			event.FailoverRecovered = true
		}
	}
	c.Set(OpsUpstreamErrorsKey, events)
}

// checkSkipMonitoringForUpstreamEvent checks whether the upstream error event
// matches a passthrough rule with skip_monitoring=true and, if so, sets the
// OpsSkipPassthroughKey on the context.  This ensures intermediate retry /
// failover errors (which never go through the final applyErrorPassthroughRule
// path) can still suppress ops_error_logs recording.
func checkSkipMonitoringForUpstreamEvent(c *gin.Context, ev *OpsUpstreamErrorEvent) {
	if ev.UpstreamStatusCode == 0 {
		return
	}

	svc := getBoundErrorPassthroughService(c)
	if svc == nil {
		return
	}

	// Use the best available body representation for keyword matching.
	// Even when body is empty, MatchRule can still match rules that only
	// specify ErrorCodes (no Keywords), so we always call it.
	body := ev.Detail
	if body == "" {
		body = ev.Message
	}

	rule := svc.MatchRule(ev.Platform, ev.UpstreamStatusCode, []byte(body))
	if rule != nil && rule.SkipMonitoring {
		c.Set(OpsSkipPassthroughKey, true)
	}
}

func marshalOpsUpstreamErrors(events []*OpsUpstreamErrorEvent) *string {
	if len(events) == 0 {
		return nil
	}
	// Ensure we always store a valid JSON value.
	raw, err := json.Marshal(events)
	if err != nil || len(raw) == 0 {
		return nil
	}
	s := string(raw)
	return &s
}

func ParseOpsUpstreamErrors(raw string) ([]*OpsUpstreamErrorEvent, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []*OpsUpstreamErrorEvent{}, nil
	}
	var out []*OpsUpstreamErrorEvent
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// safeUpstreamURL returns scheme + host + path from a URL, stripping query/fragment
// to avoid leaking sensitive query parameters (e.g. OAuth tokens).
func safeUpstreamURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	if idx := strings.IndexByte(rawURL, '#'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	return rawURL
}
