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

const opsUpstreamModelMismatchKey = "ops_upstream_model_mismatch"
const upstreamModelMismatchMessage = "upstream returned a different model than requested"

var upstreamModelDateSuffixRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}|\d{8})$`)

// upstreamModelMatches 判断上游返回的 got 是否可视为与发送的 sent 一致。豁免规则依次为：
//  1. 忽略大小写与首尾空白；任一为空放行。
//  2. got == sent + "-" + 日期快照（YYYY-MM-DD / YYYYMMDD）；sent 以 -latest 结尾且 got 以其前缀开头。
//  3. provider 前缀容忍：去掉 "provider/" 前缀后（lastOpenAIModelSegment）再做 1、2 的比对，
//     覆盖 "openai/gpt-5.6-sol" 与 "gpt-5.6-sol" 互相回显的中转。
//  4. codex 别名归一化：网关自己的 codex 归一化（normalizeKnownCodexModel）会把 gpt-5.4-high → gpt-5.4、
//     gpt-5.3 → gpt-5.3-codex，上游按归一化后的名字回显视为一致；反向（上游回显带 reasoning 后缀）
//     只认精确别名表，不用 Contains 启发式，避免 gpt-5.6-sol-mini 被折叠成 gpt-5.6-sol 而漏拦。
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
	if normalized, ok := normalizeKnownCodexModel(sent); ok && strings.EqualFold(normalized, gotSeg) {
		return true
	}
	if normalized, ok := strictCodexModelAlias(gotSeg); ok && strings.EqualFold(normalized, sentSeg) {
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

// strictCodexModelAlias 是 normalizeKnownCodexModel 的保守子集：只查精确别名表（codexModelMap）
// 与「版本前缀 + 已知 reasoning/日期后缀」（codexVersionModelPrefixes），不走
// normalizeKnownOpenAICodexModel 里的 Contains 启发式。用于反向豁免（上游回显 gpt-5.4-high 而我们发的是 gpt-5.4）。
func strictCodexModelAlias(model string) (string, bool) {
	modelID := lastOpenAIModelSegment(model)
	if normalized := canonicalizeOpenAIModelAliasSpelling(modelID); normalized != "" {
		modelID = normalized
	}
	key := codexModelLookupKey(modelID)
	if key == "" {
		return "", false
	}
	if mapped, ok := codexModelMap[key]; ok && mapped != "" {
		return mapped, true
	}
	for _, item := range codexVersionModelPrefixes {
		if key == item.prefix {
			return item.target, true
		}
		if suffix, ok := strings.CutPrefix(key, item.prefix+"-"); ok && isKnownCodexModelSuffix(suffix) {
			return item.target, true
		}
	}
	return "", false
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
func (s *OpenAIGatewayService) checkUpstreamModelMismatch(
	c *gin.Context, account *Account, upstreamRequestID string,
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
		Kind: "failover", Message: message,
	})
	headers := http.Header{}
	if rid := strings.TrimSpace(upstreamRequestID); rid != "" {
		headers.Set("x-request-id", rid)
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type": "upstream_error", "code": "upstream_model_mismatch", "message": upstreamModelMismatchMessage,
	}})
	return &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body, ResponseHeaders: headers}
}
