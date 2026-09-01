package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

const codexUpstreamMinVersion = "0.144.0"

// codexLoadShedOriginators 是上游 /backend-api/codex 按 originator 分桶调度容量时已观测到的
// 降载桶集合。命中降载桶的请求即使 HTTP 200 也会立刻推 SSE server_is_overloaded 错误并以
// response.failed 收尾，网关据此判定瞬时上游故障并冷却账号，对外表现为「Codex 账号频繁过载
// 不可用」。同账号、同请求体、同 UA，仅把 originator 换成 codex_cli_rs 即恢复正常（换言之
// UA 不是判定因子，originator 才是）。该集合是上游容量策略的快照而非协议常量，上游调整
// 分桶后需同步修订。
var codexLoadShedOriginators = map[string]bool{
	"codex-tui": true,
}

// normalizeCodexLoadShedIdentity 把落在上游降载桶的官方身份改写为 Codex CLI 身份：
// originator 与 UA 首段替换为 codex_cli_rs，并在尾部 `(name; version)` 客户端标识组的 name
// 与被归一化的降载身份一致时裁掉该组（真实 CLI UA 不带该组），版本 / OS / 架构 / 终端指纹
// 原样保留。未命中降载桶时原样返回，天然幂等（归一化后的 codex_cli_rs 不在降载桶集合中）。
func normalizeCodexLoadShedIdentity(originator, userAgent string) (string, string) {
	normalizedOriginator := strings.ToLower(strings.TrimSpace(originator))
	if !codexLoadShedOriginators[normalizedOriginator] {
		return originator, userAgent
	}
	ua := strings.TrimSpace(userAgent)
	slash := strings.IndexByte(ua, '/')
	if slash <= 0 {
		return "codex_cli_rs", ua
	}
	rest := ua[slash:]
	// 仅当尾部括号组的 name 段与被归一化的身份一致时才裁剪，避免误截合法 UA 尾巴
	// （如 `(Ubuntu 22.4.0; x86_64)` 这类 OS/架构组）。
	if trailer := strings.ToLower(codexUATrailerName(rest)); trailer != "" && trailer == normalizedOriginator {
		if open := strings.LastIndex(rest, "("); open > 0 {
			rest = strings.TrimRight(rest[:open], " ")
		}
	}
	return "codex_cli_rs", "codex_cli_rs" + rest
}

// codexUATrailerName 从 User-Agent 尾部提取 `(name; version)` 客户端标识组的 name 段。
// 仅供 normalizeCodexLoadShedIdentity 使用；与 pkg/openai 内部同名逻辑保持一致语义，
// 未跨包导出。
func codexUATrailerName(ua string) string {
	last := strings.LastIndex(ua, "(")
	if last < 0 {
		return ""
	}
	rest := ua[last+1:]
	closeIndex := strings.Index(rest, ")")
	if closeIndex < 0 {
		return ""
	}
	name := strings.TrimSpace(rest[:closeIndex])
	if separator := strings.Index(name, ";"); separator >= 0 {
		name = strings.TrimSpace(name[:separator])
	}
	return name
}

// ensureCodexIdentityHeaders fills the identity headers required by synthetic
// Codex probes while preserving any caller-provided official identity.
func ensureCodexIdentityHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	if strings.TrimSpace(headers.Get("user-agent")) == "" {
		headers.Set("user-agent", codexCLIUserAgent)
	}
	if strings.TrimSpace(headers.Get("originator")) == "" {
		headers.Set("originator", "codex_cli_rs")
	}
	if strings.TrimSpace(headers.Get("version")) == "" {
		headers.Set("version", codexCLIVersion)
	}
	headers.Set("OpenAI-Beta", "responses=experimental")
}

// applyOpenAICodexProbeHeaders adds the minimum Codex fingerprint to a
// generated probe without changing the identity of a real OAuth request.
func applyOpenAICodexProbeHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	ensureCodexIdentityHeaders(headers)
	headers.Set("X-Codex-Window-ID", uuid.NewString())
}

// enforceCodexIdentityHeaders ensures OAuth requests use an official Codex
// identity whose originator matches the final User-Agent client name.
//
// 配对之后再做降载身份归一化：落在上游降载桶的身份统一改写为 CLI 身份，只替换身份段，
// 保留版本 / OS / 架构 / 终端指纹。这里是 HTTP / 透传 / WS / 探针等出站路径共用的收口点，
// 单点修复即可覆盖所有调用方。
func enforceCodexIdentityHeaders(headers http.Header) {
	if headers == nil || headers.Get("originator") == "" {
		return
	}
	originator, pairedUA, ok := openai.PairCodexClientIdentity(headers.Get("user-agent"))
	if !ok {
		originator, pairedUA = "codex_cli_rs", codexCLIUserAgent
	}
	originator, pairedUA = normalizeCodexLoadShedIdentity(originator, pairedUA)
	headers.Set("originator", originator)
	headers.Set("user-agent", pairedUA)
	if version := strings.TrimSpace(headers.Get("version")); version != "" && CompareVersions(version, codexUpstreamMinVersion) < 0 {
		headers.Set("version", codexCLIVersion)
	}
}
