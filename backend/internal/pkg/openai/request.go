package openai

import (
	"regexp"
	"strings"
)

// CodexCLIUserAgentPrefixes matches the legacy Codex CLI User-Agent patterns.
var CodexCLIUserAgentPrefixes = []string{
	"codex_vscode/",
	"codex_cli_rs/",
}

var codexOfficialClientUAPrefixes = []string{
	"codex_cli_rs/",
	"codex-tui/",
	"codex_vscode/",
	"codex_vscode_copilot/",
	"codex_app/",
	"codex_chatgpt_desktop/",
	"codex_atlas/",
	"codex_exec/",
	"codex_sdk_ts/",
}

const codexOfficialClientFamilyPrefix = "codex "

var codexOfficialClientOriginators = map[string]bool{
	"codex_cli_rs":          true,
	"codex-tui":             true,
	"codex_vscode":          true,
	"codex_vscode_copilot":  true,
	"codex_app":             true,
	"codex_chatgpt_desktop": true,
	"codex_atlas":           true,
	"codex_exec":            true,
	"codex_sdk_ts":          true,
}

func IsBrowserUserAgent(userAgent string) bool {
	ua := strings.TrimSpace(userAgent)
	return ua != "" && strings.HasPrefix(strings.ToLower(ua), "mozilla/")
}

func IsCodexCLIRequest(userAgent string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	return ua != "" && matchCodexClientHeaderPrefixes(ua, CodexCLIUserAgentPrefixes)
}

// IsCodexOfficialClientRequest uses the historical permissive matching path.
func IsCodexOfficialClientRequest(userAgent string) bool {
	return isCodexOfficialClientRequest(userAgent, false)
}

// IsCodexOfficialClientRequestStrict accepts only exact official UA prefixes.
func IsCodexOfficialClientRequestStrict(userAgent string) bool {
	return isCodexOfficialClientRequest(userAgent, true)
}

func isCodexOfficialClientRequest(userAgent string, strict bool) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	if strict {
		if matchCodexClientHeaderStrictPrefixes(ua, codexOfficialClientUAPrefixes) {
			return true
		}
	} else if matchCodexClientHeaderPrefixes(ua, codexOfficialClientUAPrefixes) {
		return true
	}
	if strings.HasPrefix(ua, codexOfficialClientFamilyPrefix) {
		return true
	}
	return IsCodexOfficialClientOriginator(codexUATrailerName(ua))
}

// codexUATrailerName extracts clientInfo.name from the final `(name; version)`
// group written by codex-rs, including when an originator override changed only
// the leading UA segment.
func codexUATrailerName(ua string) string {
	last := strings.LastIndex(ua, "(")
	if last < 0 {
		return ""
	}
	rest := ua[last+1:]
	closeIdx := strings.Index(rest, ")")
	if closeIdx < 0 {
		return ""
	}
	inner := strings.TrimSpace(rest[:closeIdx])
	if semi := strings.Index(inner, ";"); semi >= 0 {
		inner = strings.TrimSpace(inner[:semi])
	}
	return inner
}

func IsCodexOfficialClientOriginator(originator string) bool {
	v := normalizeCodexClientHeader(originator)
	return v != "" && (codexOfficialClientOriginators[v] || strings.HasPrefix(v, codexOfficialClientFamilyPrefix))
}

func IsCodexOfficialClientByHeaders(userAgent, originator string) bool {
	return IsCodexOfficialClientRequest(userAgent) || IsCodexOfficialClientOriginator(originator)
}

func normalizeCodexClientHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func matchCodexClientHeaderPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if normalizedPrefix := normalizeCodexClientHeader(prefix); normalizedPrefix != "" &&
			(strings.HasPrefix(value, normalizedPrefix) || strings.Contains(value, normalizedPrefix)) {
			return true
		}
	}
	return false
}

func matchCodexClientHeaderStrictPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if normalizedPrefix := normalizeCodexClientHeader(prefix); normalizedPrefix != "" && strings.HasPrefix(value, normalizedPrefix) {
			return true
		}
	}
	return false
}

// PairCodexClientIdentity derives the originator matching the final UA.
func PairCodexClientIdentity(userAgent string) (originator string, pairedUA string, ok bool) {
	ua := strings.TrimSpace(userAgent)
	slash := strings.IndexByte(ua, '/')
	if slash <= 0 {
		return "", "", false
	}
	if leading := strings.TrimSpace(ua[:slash]); isSaneCodexOriginator(leading) && IsCodexOfficialClientOriginator(leading) {
		leading = canonicalizeCodexOriginator(leading)
		return leading, leading + ua[slash:], true
	}
	if trailer := codexUATrailerName(ua); trailer != "" && !strings.ContainsRune(trailer, '/') &&
		isSaneCodexOriginator(trailer) && IsCodexOfficialClientOriginator(trailer) {
		trailer = canonicalizeCodexOriginator(trailer)
		return trailer, trailer + ua[slash:], true
	}
	return "", "", false
}

const codexOriginatorMaxLen = 64

func isSaneCodexOriginator(name string) bool {
	if name == "" || len(name) > codexOriginatorMaxLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func canonicalizeCodexOriginator(name string) string {
	if lower := normalizeCodexClientHeader(name); codexOfficialClientOriginators[lower] {
		return lower
	}
	return name
}

var codexEngineVersionPattern = regexp.MustCompile(`^(\d+\.\d+\.\d+)`)

func ParseCodexEngineVersion(ua string) (string, bool) {
	ua = strings.TrimSpace(ua)
	slash := strings.IndexByte(ua, '/')
	if slash < 0 {
		return "", false
	}
	rest := ua[slash+1:]
	end := len(rest)
	for i := 0; i < len(rest); i++ {
		if rest[i] == ' ' || rest[i] == '(' {
			end = i
			break
		}
	}
	version := codexEngineVersionPattern.FindString(strings.TrimSpace(rest[:end]))
	return version, version != ""
}
