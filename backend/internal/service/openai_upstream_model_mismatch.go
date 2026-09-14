package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// 上游模型不一致拦截：上游（第三方中转 / 账号池对端）自己做路由，把我们发过去的
// 模型 A 换成 B 再返回。A 是账号级 model_mapping + 规范化之后真正写进上游请求体的
// 模型（OpenAIForwardResult.UpstreamModel 同源），所以 3233 那种刻意 sol→luna 映射
// A 已经是 luna，与 B 相等，天然放行；只有上游偷换才会 A != B。
//
// 命中后按 UpstreamFailoverError 走现有切号流程（同 issue #5009 空 completed），
// 并在 gin context 打标，handler 侧据此记一行不计费的审计 usage_log。
//
// 文案分工——对外笼统、对内详尽：
//   - 对外（UpstreamFailoverError.ResponseBody 的 error.message，以及 failover 耗尽后经
//     ResolveUpstreamErrorResponse / 透传规则可能原样给到客户端的那句）只用
//     UpstreamModelMismatchClientMessage，不带 "upstream"、模型名、账号等任何内部名词；
//     error.type / error.code（upstream_error / upstream_model_mismatch）保留供内部识别。
//   - 对内（setOpsUpstreamError、appendOpsUpstreamError 事件、WARN 日志、审计 usage_log）
//     一个都不省：sent / got 模型、账号 ID / 名称、上游 request id、上游头指纹。

const opsUpstreamModelMismatchKey = "ops_upstream_model_mismatch"

// upstreamModelMismatchMessage 只用于内部记录（ops 事件 / 日志），会再拼上 sent=… got=…。
const upstreamModelMismatchMessage = "upstream returned a different model than requested"

// UpstreamModelMismatchClientMessage 是拦截后给终端用户的唯一文案，沿用产品既有的笼统句
// （handler/concurrency_error_response.go 同款），不新造、不含内部名词。
const UpstreamModelMismatchClientMessage = "Service temporarily unavailable, please retry later"

var upstreamModelDateSuffixRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}|\d{8})$`)

// upstreamModelMatches 判断上游返回的 got 是否可视为与发送的 sent 一致。豁免规则依次为：
//  1. 忽略大小写与首尾空白；任一为空放行。
//  2. got == sent + "-" + 日期快照（YYYY-MM-DD / YYYYMMDD）；sent 以 -latest 结尾且 got 以其前缀开头。
//  3. provider 前缀容忍：去掉 "provider/" 前缀后（lastOpenAIModelSegment）再做 1、2 的比对，
//     覆盖 "openai/gpt-5.6-sol" 与 "gpt-5.6-sol" 互相回显的中转。
//  4. codex 别名，只认升级方向：sent 是精确别名表（codexModelMap）里的键且 got 正是它的目标
//     （gpt-5.3 → gpt-5.3-codex、gpt-5.1 → gpt-5.4）放行；反向（sent 是目标、got 是别名）不放行，
//     否则 gpt-5.4 被偷换成 gpt-5-mini / gpt-5 这类降级会漏拦。
//  5. 同族 reasoning / 日期后缀剥离：任一方去掉已知后缀（codexVersionModelPrefixes + isKnownCodexModelSuffix）
//     后等于另一方放行（gpt-5.4-high ↔ gpt-5.4）。不用任何 Contains 启发式。
func upstreamModelMatches(sent, got string) bool {
	sent = strings.ToLower(strings.TrimSpace(sent))
	got = strings.ToLower(strings.TrimSpace(got))
	if sent == "" || got == "" {
		return true
	}
	if upstreamModelSegmentMatches(sent, got) {
		return true
	}
	sentSeg := strings.ToLower(lastOpenAIModelSegment(sent))
	gotSeg := strings.ToLower(lastOpenAIModelSegment(got))
	if upstreamModelSegmentMatches(sentSeg, gotSeg) {
		return true
	}
	if target, ok := codexModelMap[codexModelLookupKey(sentSeg)]; ok && target == gotSeg {
		return true
	}
	if stripCodexModelSuffix(sentSeg) == gotSeg || stripCodexModelSuffix(gotSeg) == sentSeg {
		return true
	}
	return false
}

// upstreamModelSegmentMatches：精确相等 / 日期快照 / -latest 三条基础规则，入参已小写去空白。
func upstreamModelSegmentMatches(sent, got string) bool {
	if sent == "" || got == "" || sent == got {
		return true
	}
	if strings.HasPrefix(got, sent+"-") && upstreamModelDateSuffixRe.MatchString(strings.TrimPrefix(got, sent+"-")) {
		return true
	}
	if base, ok := strings.CutSuffix(sent, "-latest"); ok && strings.HasPrefix(got, base) {
		return true
	}
	return false
}

// stripCodexModelSuffix 去掉 codex 版本前缀之后的已知 reasoning / 日期后缀
// （gpt-5.4-high → gpt-5.4、gpt-5.3-codex-2026-01-01 → gpt-5.3-codex）；不是已知前缀 + 已知后缀的原样返回。
// 只做同族剥离，不查别名表，所以 gpt-5-mini / gpt-5.1-codex-mini 这类不同族模型不会被折叠。
func stripCodexModelSuffix(model string) string {
	key := codexModelLookupKey(model)
	if key == "" {
		return model
	}
	for _, item := range codexVersionModelPrefixes {
		if key == item.prefix {
			return item.prefix
		}
		if suffix, ok := strings.CutPrefix(key, item.prefix+"-"); ok && isKnownCodexModelSuffix(suffix) {
			return item.prefix
		}
	}
	return key
}

// upstreamModelObserveOnly：xAI 的 grok 系列用带日期的模型名（grok-4.3-0709 等），现有豁免覆盖不了，
// 且真实回显尚未验证，先只记录不拦截，避免误杀。
func upstreamModelObserveOnly(sentModel string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(sentModel)), "grok")
}

// sentModelForCheck：A 的取值。passthrough 路径的 mappedModel 可能为空（body 原样转发），此时 A 就是 originalModel。
func sentModelForCheck(mappedModel, originalModel string) string {
	if strings.TrimSpace(mappedModel) != "" {
		return mappedModel
	}
	return originalModel
}

func extractUpstreamResponseModel(payload []byte) string {
	if len(payload) == 0 || !bytes.Contains(payload, []byte(`"model"`)) {
		return ""
	}
	values := gjson.GetManyBytes(payload, "response.model", "model")
	if values[0].Type == gjson.String {
		return strings.TrimSpace(values[0].Str)
	}
	if values[1].Type == gjson.String {
		return strings.TrimSpace(values[1].Str)
	}
	return ""
}

// UpstreamModelMismatchMark 记录一次「上游返回模型 != 请求模型」的命中证据。
// service 层命中后写入，handler 层读出记 usage 行后清掉（照 CyberPolicyMark）。
type UpstreamModelMismatchMark struct {
	SentModel     string
	ResponseModel string
	AccountID     int64
	Stream        bool
	// Blocked 为 true 表示本次尝试因模型不一致被 checkUpstreamModelMismatch 拦截并返回 failover
	//（handler 据此落审计行）；开关关闭 / canBlock=false 的观察性打标为 false。
	// 标记首个生效，所以必须在打标时就带上，不能事后补写。
	Blocked bool
	// 上游在被拦截前已报的 usage（通常为 0；非流式路径可能非 0），仅用于审计行 token 字段，不计费。
	Usage OpenAIUsage
}

// MarkOpsUpstreamModelMismatch 记录不一致标记；首个写入生效，后续忽略（同一 turn 只记一次）。
func MarkOpsUpstreamModelMismatch(c *gin.Context, mark UpstreamModelMismatchMark) {
	if c == nil || GetOpsUpstreamModelMismatch(c) != nil {
		return
	}
	c.Set(opsUpstreamModelMismatchKey, &mark)
}

// GetOpsUpstreamModelMismatch 返回不一致标记，未命中（或已被 Clear）返回 nil。
func GetOpsUpstreamModelMismatch(c *gin.Context) *UpstreamModelMismatchMark {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(opsUpstreamModelMismatchKey); ok {
		if m, ok := v.(*UpstreamModelMismatchMark); ok && m != nil {
			return m
		}
	}
	return nil
}

// ClearOpsUpstreamModelMismatch 用 typed-nil 覆盖（与 ClearOpsCyberPolicy 同理，gin context 无删除原语）。
// failover 换号后下一次尝试还要能重新打标，所以 handler 每次记完审计行都要清。
func ClearOpsUpstreamModelMismatch(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(opsUpstreamModelMismatchKey, (*UpstreamModelMismatchMark)(nil))
}

func (s *OpenAIGatewayService) upstreamModelMismatchBlockEnabled() bool {
	return s != nil && (s.cfg == nil || !s.cfg.Gateway.DisableUpstreamModelMismatchBlock)
}

// checkUpstreamModelMismatch 一站式：比对 + 打标 + 记 ops 错误 + 构造 failover error。
// 返回 nil 表示一致/豁免/开关关闭/canBlock=false（后两种仍打标但 Blocked=false，供 RecordUsage 落 upstream_response_model）；
// 返回非 nil 时 mark.Blocked=true，handler 据此落审计行。
// canBlock=false 用于「客户端已收到输出、无法收回」的场景（上游把 model 放在 response.completed 才给）；
// grok 系列模型（upstreamModelObserveOnly）无论 canBlock 如何都只记录不拦截。
// upstreamHeaders 是上游响应头（HTTP 路径传 resp.Header，WS 路径传 nil）：只按白名单摘指纹进 ops 事件，
// 便于识别中转实现并向厂商追责。
func (s *OpenAIGatewayService) checkUpstreamModelMismatch(
	c *gin.Context, account *Account, upstreamRequestID string, upstreamHeaders http.Header,
	sentModel, responseModel string, stream, canBlock bool, usage OpenAIUsage,
) *UpstreamFailoverError {
	if upstreamModelMatches(sentModel, responseModel) {
		return nil
	}
	if upstreamModelObserveOnly(sentModel) {
		canBlock = false
	}
	accountID, accountName, platform := int64(0), "", PlatformOpenAI
	if account != nil {
		accountID, accountName, platform = account.ID, account.Name, account.Platform
	}
	blocked := canBlock && s.upstreamModelMismatchBlockEnabled()
	MarkOpsUpstreamModelMismatch(c, UpstreamModelMismatchMark{
		SentModel: sentModel, ResponseModel: responseModel, AccountID: accountID, Stream: stream, Blocked: blocked, Usage: usage,
	})
	logger.L().Warn("openai.upstream_model_mismatch",
		zap.Int64("account_id", accountID), zap.String("sent_model", sentModel),
		zap.String("response_model", responseModel), zap.Bool("blocked", blocked), zap.Bool("can_block", canBlock))
	if !blocked {
		return nil
	}
	message := fmt.Sprintf("%s: sent=%s got=%s", upstreamModelMismatchMessage, sentModel, responseModel)
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: platform, AccountID: accountID, AccountName: accountName,
		UpstreamStatusCode: http.StatusBadGateway, UpstreamRequestID: upstreamRequestID,
		UpstreamHeaders: opsUpstreamHeaderFingerprint(upstreamHeaders), Kind: "failover", Message: message,
	})
	headers := http.Header{}
	if rid := strings.TrimSpace(upstreamRequestID); rid != "" {
		headers.Set("x-request-id", rid)
	}
	// 对外 body：message 用笼统文案。管理员配了「透传 body」的错误透传规则时这句会经
	// sanitizeClientVisibleUpstreamMessage 原样给到客户端，所以这里绝不能带 sent/got。
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type": "upstream_error", "code": "upstream_model_mismatch", "message": UpstreamModelMismatchClientMessage,
	}})
	// 池模式账号（中转自身是一池多 key，一个凭证背后有很多上游节点）：偷换模型的多半只是池里
	// 某个坏节点，立刻切号并降权会把整个凭证一起摘掉。标 RetryableOnSameAccount 后走 handler
	// 现有的池模式分支：同账号最多重试 pool_mode_retry_count 次，用尽再切号 + 降权。
	// 不把 502 加进 defaultPoolModeRetryableStatusCodes——那会让所有 502 都同账号重试。
	// 非池模式（单 key / OAuth）保持直接切号。
	return &UpstreamFailoverError{
		StatusCode: http.StatusBadGateway, ResponseBody: body, ResponseHeaders: headers,
		RetryableOnSameAccount: account != nil && account.IsPoolMode(),
	}
}
