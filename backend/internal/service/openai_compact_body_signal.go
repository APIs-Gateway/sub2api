package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAIRemoteCompactionV2Feature 是真实 Codex 默认安装唯一会 advertise 的
// beta feature token（codex-rs FEATURES 枚举里没有默认开启的 Experimental
// 特性，见 upstream #5668 提交说明）。
const openAIRemoteCompactionV2Feature = "remote_compaction_v2"

// ensureOpenAIRemoteCompactionV2BetaFeature 确保出站 x-codex-beta-features
// 头包含 remote_compaction_v2。已存在时保持原样，不重复追加。
func ensureOpenAIRemoteCompactionV2BetaFeature(h http.Header) {
	if h == nil {
		return
	}
	tokens := make([]string, 0, 4)
	for _, value := range h.Values("x-codex-beta-features") {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if token == openAIRemoteCompactionV2Feature {
				return
			}
			tokens = append(tokens, token)
		}
	}
	tokens = append(tokens, openAIRemoteCompactionV2Feature)
	h.Set("x-codex-beta-features", strings.Join(tokens, ","))
}

// hasOpenAICodexBetaFeaturesHeader 报告出站头里是否已存在非空的
// x-codex-beta-features（即客户端自己声明过能力集）。
func hasOpenAICodexBetaFeaturesHeader(h http.Header) bool {
	if h == nil {
		return false
	}
	for _, value := range h.Values("x-codex-beta-features") {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// applyOpenAICodexBetaFeatures 按真实 Codex 的会话级行为补注
// x-codex-beta-features。
//
// codex 侧规则：该头是会话级常量，挂在 /responses、WS 握手、
// /responses/compact 三处的每一个请求上，而不是只挂压缩回合。实测默认
// 安装下没有任何 Experimental 特性默认开启，因此默认 Codex 的头值恰好就是
// 单个 "remote_compaction_v2"。
//
// 网关据此对齐：
//   - ChatGPT codex 上游（OAuth）：客户端未声明该头时补成默认 Codex 形态，
//     消除"仅压缩回合才带该头"这种真实 Codex 不会产生的模式；
//   - 客户端已声明该头：原样保留。非空但不含 v2 表示用户显式关闭了该特性，
//     网关不得替其改写能力声明；
//   - 非 OAuth 上游（API Key/第三方兼容网关）：不做会话级注入，避免向非
//     Codex 后端撒 Codex 专属头。
//
// 注：upstream #5668 还包含"原生 remote compaction v2 回合（body 带
// compaction_trigger）无论账号类型都强制确保 v2 在列"的分支，该分支依赖
// handler 层在识别出该请求形态后调用 MarkOpenAINativeCompactionV2 标记
// gin.Context；本批次只吸收 backend/internal/service/** 范围内能独立生效
// 的部分，handler 改动不在此批次授权范围内，故该分支未落地（见 batch
// svc-16 item13 报告）。参数 c 目前未参与判定，保留在签名里是为了future
// handler 层接入时不必再改所有调用点。
func applyOpenAICodexBetaFeatures(c *gin.Context, account *Account, h http.Header) {
	if h == nil {
		return
	}
	if account == nil || !account.IsOpenAIOAuth() {
		return
	}
	if hasOpenAICodexBetaFeaturesHeader(h) {
		return
	}
	h.Set("x-codex-beta-features", openAIRemoteCompactionV2Feature)
}

// HasCompactionTriggerInInput detects Codex remote compact v2 requests that
// send the compact signal as an input item instead of calling /responses/compact.
func HasCompactionTriggerInInput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "compaction_trigger" {
			found = true
			return false
		}
		return true
	})
	return found
}
