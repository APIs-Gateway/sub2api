package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// Codex CLI 在收到错误时几乎全部用自己硬编码的文案渲染，只有极少数分支会把服务端
// 的 error.message 拼进去。把上游错误翻译成下面这些 code/type，Codex 就会显示官方
// 文案，而不是把上游（可能是中文的）原始报错透给用户。
//
// 判定逻辑的取证位置（openai/codex，rust-v0.153.4）：
//   - HTTP 状态码分支：codex-rs/codex-api/src/api_bridge.rs map_api_error()
//   - 流内 response.failed 分支：codex-rs/codex-api/src/sse/responses.rs
//   - 文案模板：codex-rs/protocol/src/error.rs
//
// 注意两侧判定键不同：HTTP 429 的额度判定读 error.type，流内分派读 error.code。
const (
	CodexErrCodeServerOverloaded      = "server_is_overloaded"
	CodexErrCodeContextLengthExceeded = "context_length_exceeded"
	CodexErrCodeInsufficientQuota     = "insufficient_quota"
	CodexErrCodeUsageNotIncluded      = "usage_not_included"
	CodexErrCodeInvalidPrompt         = "invalid_prompt"
	CodexErrCodeCyberPolicy           = "cyber_policy"
	CodexErrCodeMisalignmentPolicy    = "misalignment_policy_violation"

	codexErrTypeUsageLimitReached = "usage_limit_reached"
	codexErrTypeUsageNotIncluded  = "usage_not_included"

	// codexUnknownErrorMessage 与 Codex 在响应体为空时自己渲染出来的文案一致
	// （protocol/src/error.rs display_body()）。显式写出来是因为 Codex 只有在 body
	// 完全为空时才会用 "Unknown error"；留一个 `{"error":{}}` 反而会被原样打印。
	codexUnknownErrorMessage = "Unknown error"

	// codexCanonicalUpstreamKey 保存本次请求真实的上游状态码与响应体。
	// handler 层拿到的 status 往往已经被 MapUpstreamErrorDefault 归一（401/403 → 502），
	// 归一化映射需要的是归一之前的原始值。
	codexCanonicalUpstreamKey = "codex_canonical_upstream"

	// codexUpstreamStatusOverloaded 是 Anthropic 系上游用来表示过载的非标准状态码。
	codexUpstreamStatusOverloaded = 529

	// codexCanonicalErrorType 沿用仓库既有的上游错误 envelope 类型
	// （MapUpstreamErrorDefault 用的也是它）。/responses 上并不只有 Codex 一种客户端，
	// 归一化只该换掉文案，不该把错误结构换成另一种形状。
	codexCanonicalErrorType = "upstream_error"
)

// CodexCanonicalError 描述一次上游失败要以什么形态呈现给 Codex CLI。
//
// HTTPStatus 为 0 表示「不覆盖」，调用方应沿用自己原有的响应；
// SSEErrCode 为空同样表示「不改写」流内的 response.failed 事件。
type CodexCanonicalError struct {
	HTTPStatus int
	Body       []byte
	SSEErrCode string
}

type codexCanonicalUpstream struct {
	status int
	body   []byte
}

// SetCodexCanonicalUpstream 记录归一化映射要用的原始上游状态码与响应体。
func SetCodexCanonicalUpstream(c *gin.Context, upstreamStatus int, upstreamBody []byte) {
	if c == nil {
		return
	}
	c.Set(codexCanonicalUpstreamKey, codexCanonicalUpstream{status: upstreamStatus, body: upstreamBody})
}

// HasCodexCanonicalUpstream 报告本次请求是否已记录过原始上游错误。
func HasCodexCanonicalUpstream(c *gin.Context) bool {
	_, ok := codexCanonicalUpstreamFromContext(c)
	return ok
}

func codexCanonicalUpstreamFromContext(c *gin.Context) (codexCanonicalUpstream, bool) {
	if c == nil {
		return codexCanonicalUpstream{}, false
	}
	v, ok := c.Get(codexCanonicalUpstreamKey)
	if !ok {
		return codexCanonicalUpstream{}, false
	}
	hint, ok := v.(codexCanonicalUpstream)
	return hint, ok
}

// CodexCanonicalErrorForContext 用本次请求记录的原始上游错误算归一化结果；
// 没有记录时退回调用方给的 fallbackStatus。
func CodexCanonicalErrorForContext(c *gin.Context, fallbackStatus int) CodexCanonicalError {
	if hint, ok := codexCanonicalUpstreamFromContext(c); ok {
		status := hint.status
		if status <= 0 {
			status = fallbackStatus
		}
		return CodexCanonicalErrorFor(status, hint.body)
	}
	return CodexCanonicalErrorFor(fallbackStatus, nil)
}

// CodexCanonicalResponsesError 返回 Responses 端点上应当写出的归一化响应。
// 不适用（非 Responses 路由，或这一类错误按设计不覆盖）时返回 nil，
// 调用方沿用自己原有的写出逻辑。
func CodexCanonicalResponsesError(c *gin.Context, upstreamStatus int, upstreamBody []byte) *CodexCanonicalError {
	if !InboundIsResponses(c) {
		return nil
	}
	canonical := CodexCanonicalErrorFor(upstreamStatus, upstreamBody)
	if canonical.HTTPStatus <= 0 {
		return nil
	}
	return &canonical
}

// CodexCanonicalErrorFor 把一次上游失败映射成 Codex CLI 能用官方文案渲染的形态。
func CodexCanonicalErrorFor(upstreamStatus int, upstreamBody []byte) CodexCanonicalError {
	status, body := codexCanonicalHTTPResponse(upstreamStatus, upstreamBody)
	return CodexCanonicalError{
		HTTPStatus: status,
		Body:       body,
		SSEErrCode: codexCanonicalSSEErrCode(upstreamStatus, upstreamBody),
	}
}

// codexCanonicalHTTPResponse 覆盖「流尚未开始」时写出的状态码与响应体。
// 状态码 1:1 保留上游，只替换响应体；返回 0 表示这一类不覆盖。
func codexCanonicalHTTPResponse(upstreamStatus int, upstreamBody []byte) (int, []byte) {
	// 上游已经给出 Codex 原生识别的策略类 code：原样保留，Codex 自己有官方回落文案。
	switch codexUpstreamErrorCode(upstreamBody) {
	case CodexErrCodeCyberPolicy, CodexErrCodeMisalignmentPolicy:
		return 0, nil
	}

	// 额度类判定不看状态码：上游可能把它藏在 HTTP 200 的 SSE 事件里。
	switch codexUpstreamErrorType(upstreamBody) {
	case codexErrTypeUsageLimitReached:
		return http.StatusTooManyRequests, codexUsageLimitReachedBody(upstreamBody)
	case codexErrTypeUsageNotIncluded:
		return http.StatusTooManyRequests, []byte(`{"error":{"type":"usage_not_included"}}`)
	}

	// 上下文超长是唯一一类「上游原文比官方文案更有用」的错误：Codex 的 HTTP 分支根本
	// 不认 context_length_exceeded，没有对应的官方文案可换，换成 "Unknown error" 只会
	// 让用户丢掉唯一可操作的线索。本仓库既有的 context-window 放行就是为它设计的。
	// 判据只看上游 message 是否本身就是英文的 context-window 提示——仅靠 error.code
	// 命中、message 是中文的情况仍然走下面的归一化，不会泄露上游原文。
	if isOpenAIContextWindowError(codexUpstreamErrorField(upstreamBody, "message").String(), nil) {
		return 0, nil
	}

	if upstreamStatus == http.StatusUnauthorized {
		// 上游账号鉴权失败是网关侧的问题，但 Codex 的 is_recoverable_auth_error 只对 401
		// 成立：把 401 原样透回去，它会以为是用户自己的 ChatGPT token 失效，去跑 token
		// 刷新流程并提示重新登录，把人引到完全错误的方向。503 是唯一既能立刻失败、又能
		// 拿到纯硬编码官方文案的出口。
		//
		// 只作用于「上游返回的 401」。网关自身的 401（API key 无效等）不会走到归一化层
		// ——那些路径不调用 SetCodexCanonicalUpstream，文案照旧。
		return http.StatusServiceUnavailable, codexServerOverloadedBody()
	}

	if upstreamStatus < 400 || upstreamStatus > 599 {
		// 传输错误 / 断流 / 未知：兜底 429，Codex 显示
		// "exceeded retry limit, last status: 429 Too Many Requests"。
		return http.StatusTooManyRequests, codexUnknownErrorBody()
	}

	switch status := codexCanonicalStatus(upstreamStatus); status {
	case http.StatusBadRequest:
		// Codex 对 400 的处理是原样打印整个响应体（api_bridge.rs InvalidRequest(body_text)），
		// 协议上没有官方文案可用；本仓库 400 的文案本来就是固定英文，保持现状。
		return 0, nil
	case http.StatusServiceUnavailable:
		return http.StatusServiceUnavailable, codexServerOverloadedBody()
	default:
		return status, codexUnknownErrorBody()
	}
}

// codexCanonicalStatus 只在上游状态码不在 IANA 注册表里时才替换它。
//
// Go 的 http.StatusText 和 Codex 用的 Rust http::StatusCode 认同一张注册表，未注册的
// 状态码会被 Codex 渲染成 "unexpected status 520 <unknown status code>"，客户端等于
// 什么都没拿到。已注册的状态码一律 1:1 原样保留，不做任何合并。
func codexCanonicalStatus(upstreamStatus int) int {
	if http.StatusText(upstreamStatus) != "" {
		return upstreamStatus
	}
	switch upstreamStatus {
	case 522, 524:
		// Cloudflare 522 Connection Timed Out / 524 A Timeout Occurred，语义等价 504。
		return http.StatusGatewayTimeout
	case codexUpstreamStatusOverloaded:
		// 529 Site is overloaded（Anthropic 系上游也用它），语义等价 503。
		return http.StatusServiceUnavailable
	default:
		// 其余非标准码（Cloudflare 520/521/523/525/526/527 等）都表示边缘或源站故障。
		return http.StatusBadGateway
	}
}

// codexCanonicalSSEErrCode 决定「流已开始」时 response.failed 里要写的 error.code。
// 此时 HTTP 状态码已固化为 200，只能靠 code 让 Codex 选中官方文案；Codex 流内只认
// 下面这几个 code，没有对应 code 的上游状态只能落到 server_is_overloaded。
// 返回空串表示不改写。
func codexCanonicalSSEErrCode(upstreamStatus int, upstreamBody []byte) string {
	code := codexUpstreamErrorCode(upstreamBody)
	switch code {
	case CodexErrCodeCyberPolicy, CodexErrCodeMisalignmentPolicy:
		return ""
	case CodexErrCodeInsufficientQuota, CodexErrCodeUsageNotIncluded, CodexErrCodeContextLengthExceeded:
		return code
	}

	if isOpenAIContextWindowError(extractOpenAISSEErrorMessage(upstreamBody), upstreamBody) {
		return CodexErrCodeContextLengthExceeded
	}

	switch codexUpstreamErrorType(upstreamBody) {
	case codexErrTypeUsageLimitReached:
		// 流内没有 usage-limit 等价 code，insufficient_quota 的官方文案
		// "Quota exceeded. Check your plan and billing details." 语义最接近。
		return CodexErrCodeInsufficientQuota
	case codexErrTypeUsageNotIncluded:
		return CodexErrCodeUsageNotIncluded
	}

	if IsRequestShapedUpstream4xx(upstreamStatus) {
		return CodexErrCodeInvalidPrompt
	}
	return CodexErrCodeServerOverloaded
}

// codexUsageLimitReachedBody 只保留 Codex 渲染额度文案真正需要的字段，
// 丢掉上游 message 等一切自定义内容。
func codexUsageLimitReachedBody(upstreamBody []byte) []byte {
	errPayload := gin.H{"type": codexErrTypeUsageLimitReached}
	if planType := strings.TrimSpace(codexUpstreamErrorField(upstreamBody, "plan_type").String()); planType != "" {
		errPayload["plan_type"] = planType
	}
	// Codex 期望 resets_at 是 Unix 秒；上游偶尔用字符串给，gjson 的 Int() 会一并解析。
	if resetsAt := codexUpstreamErrorField(upstreamBody, "resets_at").Int(); resetsAt > 0 {
		errPayload["resets_at"] = resetsAt
	}
	body, err := marshalOpenAIUpstreamJSON(gin.H{"error": errPayload})
	if err != nil {
		return []byte(`{"error":{"type":"usage_limit_reached"}}`)
	}
	return body
}

func codexServerOverloadedBody() []byte {
	return []byte(`{"error":{"code":"` + CodexErrCodeServerOverloaded + `","type":"` + codexCanonicalErrorType + `"}}`)
}

func codexUnknownErrorBody() []byte {
	return []byte(`{"error":{"message":"` + codexUnknownErrorMessage + `","type":"` + codexCanonicalErrorType + `"}}`)
}

// codexUpstreamErrorField 同时兼容普通 JSON 错误体和 Responses 的 response.failed 事件。
func codexUpstreamErrorField(upstreamBody []byte, field string) gjson.Result {
	if len(upstreamBody) == 0 || !gjson.ValidBytes(upstreamBody) {
		return gjson.Result{}
	}
	for _, prefix := range []string{"error.", "response.error."} {
		if r := gjson.GetBytes(upstreamBody, prefix+field); r.Exists() {
			return r
		}
	}
	return gjson.Result{}
}

func codexUpstreamErrorCode(upstreamBody []byte) string {
	return strings.ToLower(strings.TrimSpace(codexUpstreamErrorField(upstreamBody, "code").String()))
}

func codexUpstreamErrorType(upstreamBody []byte) string {
	return strings.ToLower(strings.TrimSpace(codexUpstreamErrorField(upstreamBody, "type").String()))
}

// codexResponsesFailedEventData 构造一个最小但合法的 Responses 协议终止事件。
//
// Codex 完全忽略 SSE 的 `event:` 名字，只解析 data 行的 JSON，并要求顶层有 "type"；
// 解析失败会被静默丢弃。通用的 `{"type":"error"}` 帧因此对 Codex 无效，流最终会以
// "stream closed before response.completed" 收场——所以断流时必须补一个 response.failed。
func codexResponsesFailedEventData(c *gin.Context, errCode string) string {
	body, err := marshalOpenAIUpstreamJSON(gin.H{
		"type": "response.failed",
		"response": gin.H{
			"id":     codexSynthesizedResponseID(c),
			"object": "response",
			"status": "failed",
			"output": []any{},
			"error":  gin.H{"code": errCode},
		},
	})
	if err != nil {
		return ""
	}
	return string(body)
}

// codexSynthesizedResponseID 优先复用 server 端 request_id，便于把客户端报错关联回日志。
func codexSynthesizedResponseID(c *gin.Context) string {
	if c != nil && c.Request != nil {
		if rid, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok {
			if rid = strings.TrimSpace(rid); rid != "" {
				return "resp_" + strings.ReplaceAll(rid, "-", "")
			}
		}
	}
	return "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// InboundIsResponses 判断当前请求是否落在任何 /responses 路由上。
//
// 不能直接用 GetInboundEndpoint(c) == EndpointResponses 比较，因为
// NormalizeInboundEndpoint 只识别包含 "/v1/responses" 子串的路径；
// 项目里实际注册了多组路由（gateway_v1、top-level bare、codex direct），
// 其中 r.POST("/responses", ...) 和 codexDirect.POST("/responses", ...)
// 的 c.FullPath() 不含 "/v1/" 前缀，会被归一化为原始路径，
// 导致协议合规终止事件没法发出去。
//
// 这里用 FullPath 的后缀判断，覆盖所有变体：
//   - /v1/responses
//   - /v1/responses/compact
//   - /responses
//   - /responses/compact
//   - /backend-api/codex/responses
//   - /backend-api/codex/responses/compact
func InboundIsResponses(c *gin.Context) bool {
	if c == nil {
		return false
	}
	p := strings.TrimRight(c.FullPath(), "/")
	if p == "" && c.Request != nil && c.Request.URL != nil {
		p = strings.TrimRight(c.Request.URL.Path, "/")
	}
	if p == "" {
		return false
	}
	return strings.HasSuffix(p, "/responses") || strings.Contains(p, "/responses/")
}
