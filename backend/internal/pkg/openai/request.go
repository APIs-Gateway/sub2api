package openai

import "strings"

// CodexCLIUserAgentPrefixes matches Codex CLI User-Agent patterns
// Examples: "codex_vscode/1.0.0", "codex_cli_rs/0.1.2"
var CodexCLIUserAgentPrefixes = []string{
	"codex_vscode/",
	"codex_cli_rs/",
}

// CodexOfficialClientUserAgentPrefixes matches Codex 官方客户端家族 User-Agent 前缀。
// 该列表仅用于 OpenAI OAuth `codex_cli_only` 访问限制判定。
var CodexOfficialClientUserAgentPrefixes = []string{
	"codex_cli_rs/",
	"codex_vscode/",
	"codex_app/",
	"codex_chatgpt_desktop/",
	"codex_atlas/",
	"codex_exec/",
	"codex_sdk_ts/",
	"codex ",
}

// CodexOfficialClientOriginatorPrefixes matches Codex 官方客户端家族 originator 前缀。
// 说明：OpenAI 官方 Codex 客户端并不只使用固定的 codex_app 标识。
// 例如 codex_cli_rs、codex_vscode、codex_chatgpt_desktop、codex_atlas、codex_exec、codex_sdk_ts 等。
var CodexOfficialClientOriginatorPrefixes = []string{
	"codex_",
	"codex ",
}

// IsBrowserUserAgent 判断 User-Agent 是否来自浏览器（Chrome/Firefox/Safari/Edge/Opera 等）。
// 所有现代浏览器的 UA 均以 "Mozilla/" 作为前缀，CLI 工具（codex/claude/curl/postman/python-requests 等）不会。
// 该判定用于避免 Cloudflare 对浏览器型 UA 在 OpenAI 上游接口上触发 JS 质询。
func IsBrowserUserAgent(userAgent string) bool {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(ua), "mozilla/")
}

// IsCodexCLIRequest checks if the User-Agent indicates a Codex CLI request
func IsCodexCLIRequest(userAgent string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	return matchCodexClientHeaderPrefixes(ua, CodexCLIUserAgentPrefixes)
}

// IsCodexOfficialClientRequest checks if the User-Agent indicates a Codex 官方客户端请求。
// 与 IsCodexCLIRequest 解耦，避免影响历史兼容逻辑。
func IsCodexOfficialClientRequest(userAgent string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	return matchCodexClientHeaderPrefixes(ua, CodexOfficialClientUserAgentPrefixes)
}

// IsCodexOfficialClientOriginator checks if originator indicates a Codex 官方客户端请求。
func IsCodexOfficialClientOriginator(originator string) bool {
	v := normalizeCodexClientHeader(originator)
	if v == "" {
		return false
	}
	return matchCodexClientHeaderPrefixes(v, CodexOfficialClientOriginatorPrefixes)
}

// IsCodexOfficialClientByHeaders checks whether the request headers indicate an
// official Codex client family request.
func IsCodexOfficialClientByHeaders(userAgent, originator string) bool {
	return IsCodexOfficialClientRequest(userAgent) || IsCodexOfficialClientOriginator(originator)
}

func normalizeCodexClientHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func matchCodexClientHeaderPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		normalizedPrefix := normalizeCodexClientHeader(prefix)
		if normalizedPrefix == "" {
			continue
		}
		// 优先前缀匹配；若 UA/Originator 被网关拼接为复合字符串时，退化为包含匹配。
		if strings.HasPrefix(value, normalizedPrefix) || strings.Contains(value, normalizedPrefix) {
			return true
		}
	}
	return false
}

var pairableCodexOriginators = map[string]struct{}{
	"codex_cli_rs":          {},
	"codex-tui":             {},
	"codex_vscode":          {},
	"codex_vscode_copilot":  {},
	"codex_app":             {},
	"codex_chatgpt_desktop": {},
	"codex_atlas":           {},
	"codex_exec":            {},
	"codex_sdk_ts":          {},
}

const codexOriginatorMaxLen = 64

// PairCodexClientIdentity derives the originator accepted by the Codex upstream
// from the final outbound User-Agent. The Codex upstream rejects a mismatched
// originator and User-Agent client name with 404, so callers must fall back to a
// known Codex CLI identity when this returns ok=false.
func PairCodexClientIdentity(userAgent string) (originator string, pairedUA string, ok bool) {
	ua := strings.TrimSpace(userAgent)
	slash := strings.IndexByte(ua, '/')
	if slash <= 0 {
		return "", "", false
	}
	if leading := strings.TrimSpace(ua[:slash]); isPairableCodexOriginator(leading) {
		leading = canonicalizePairableCodexOriginator(leading)
		return leading, leading + ua[slash:], true
	}
	// CODEX_INTERNAL_ORIGINATOR_OVERRIDE changes the UA prefix but retains the
	// real client name in the trailing `(name; version)` group.
	if trailer := codexUATrailerName(ua); trailer != "" && !strings.ContainsRune(trailer, '/') &&
		isPairableCodexOriginator(trailer) {
		trailer = canonicalizePairableCodexOriginator(trailer)
		return trailer, trailer + ua[slash:], true
	}
	return "", "", false
}

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

func isPairableCodexOriginator(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > codexOriginatorMaxLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	if _, ok := pairableCodexOriginators[strings.ToLower(name)]; ok {
		return true
	}
	return strings.HasPrefix(name, "Codex ")
}

func canonicalizePairableCodexOriginator(name string) string {
	if _, ok := pairableCodexOriginators[strings.ToLower(name)]; ok {
		return strings.ToLower(name)
	}
	return name
}
