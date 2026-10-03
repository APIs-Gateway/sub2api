package service

import (
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// ModelFamily 把请求模型名映射到「模型族」，只供回退链熔断使用（按 分组 × 模型族 计数）。
//
// 规则（设计 3.6）：只在规则表精确命中时才返回族名，未命中返回空串，空串不进熔断。
// 这里刻意不复用 normalizeKnownOpenAICodexModel：它对 "gpt-5"、"codex" 等做宽泛的 Contains 兜底，
// 任意 gpt-5.x-随便写 都会被归进某个族，让用户随手编造模型名就能影响一个真实的熔断键。
//
// 取舍：
//   - OpenAI：先用 openai.CanonicalizeOpenAIModelAliasSpelling 统一大小写、下划线、供应商前缀，
//     再要求「等于已知基名，或 基名 + 已知后缀（effort / openai-compact / 日期）」，
//     6 系列直接用 openai 包里已有的严格判定函数；裸 gpt-5.6 与 gpt-6 沿用既有别名（sol / astra）。
//   - Claude：只认 claude-(opus|sonnet|haiku)-主版本[-次版本][-8 位日期] 与旧式 claude-3-5-sonnet，
//     容忍 [1m]、-thinking、anthropic. 前缀；Fable 复用 isAnthropicFableModel。
//   - Gemini：只认 gemini-版本-(pro|flash|flash-lite)[已知后缀]。
//
// 族名即归一后的基名，如 "gpt-6-sol"、"claude-opus-4-8"、"gemini-2.5-pro"。
func ModelFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	if fam := openAIModelFamily(model); fam != "" {
		return fam
	}
	if isAnthropicFableModel(m) {
		return anthropicFableRateLimitKey
	}
	if fam := claudeModelFamily(m); fam != "" {
		return fam
	}
	return geminiModelFamily(m)
}

// openAIFamilyBases 按特异性从高到低排列（先 -pro / -mini 再无后缀）。
var openAIFamilyBases = []string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5-pro",
	"gpt-5.5",
	"gpt-5.4-mini",
	"gpt-5.4-nano",
	"gpt-5.4",
	"gpt-5.3-codex-spark",
	"gpt-5.3-codex",
	"gpt-5.2",
	"gpt-image-2",
	"gpt-image-1",
}

func openAIModelFamily(model string) string {
	canonical := openai.CanonicalizeOpenAIModelAliasSpelling(model)
	if canonical == "" {
		return ""
	}
	switch {
	case openai.IsGPT61SolModelSpelling(canonical):
		return "gpt-6.1-sol"
	case openai.IsGPT6SolOrLunaModelSpelling(canonical):
		if strings.HasPrefix(canonical, "gpt-6-sol") {
			return "gpt-6-sol"
		}
		return "gpt-6-luna"
	case canonical == "gpt-6" || familyBaseMatches(canonical, "gpt-6-astra"):
		return "gpt-6-astra"
	case canonical == "gpt-5.6":
		return "gpt-5.6-sol"
	}
	for _, base := range openAIFamilyBases {
		if familyBaseMatches(canonical, base) {
			return base
		}
	}
	return ""
}

// familyBaseMatches 判断 model 是否等于 base，或等于 base-<已知后缀>。
func familyBaseMatches(model, base string) bool {
	if model == base {
		return true
	}
	suffix, ok := strings.CutPrefix(model, base+"-")
	if !ok {
		return false
	}
	return knownFamilySuffix(suffix)
}

// knownFamilySuffix 接受「至多一个 effort 词 + 至多一个 YYYY-MM-DD 日期 + 可选一个 openai-compact」，顺序不限，
// 用 "-" 连接；不接受重复、空段或结尾的 "-"（与网关 isKnownCodexModelSuffix 一样严格，避免可编造的命中写法）。
func knownFamilySuffix(s string) bool {
	var seenEffort, seenDate, seenCompact bool
	for s != "" {
		var rest string
		switch {
		case s == "openai-compact" || strings.HasPrefix(s, "openai-compact-"):
			if seenCompact {
				return false
			}
			seenCompact = true
			rest = strings.TrimPrefix(s, "openai-compact")
		case len(s) >= 10 && isCodexDateSuffix(s[:10]) && (len(s) == 10 || s[10] == '-'):
			if seenDate {
				return false
			}
			seenDate = true
			rest = s[10:]
		default:
			tok, _, _ := strings.Cut(s, "-")
			switch tok {
			case "none", "minimal", "low", "medium", "high", "xhigh", "max":
			default:
				return false
			}
			if seenEffort {
				return false
			}
			seenEffort = true
			rest = s[len(tok):]
		}
		if rest == "" {
			return true
		}
		// 后面必须还有内容：以 "-" 开头且 "-" 之后非空。
		if rest[0] != '-' || len(rest) == 1 {
			return false
		}
		s = rest[1:]
	}
	return false
}

var (
	claudeNewFamilyRe = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-(\d{1,2})(?:[.-](\d{1,2}))?(?:-\d{8})?$`)
	claudeOldFamilyRe = regexp.MustCompile(`^claude-(\d{1,2})(?:[.-](\d{1,2}))?-(opus|sonnet|haiku)(?:-\d{8})?$`)
	geminiFamilyRe    = regexp.MustCompile(`^gemini-(\d{1,2}(?:\.\d{1,2})?)-(pro|flash-lite|flash)(?:-(?:preview|latest|high|low|medium|tiered|\d{2}-\d{2}|\d{3}))*$`)
)

func stripModelPathAndDecorations(m string) string {
	if slash := strings.LastIndexByte(m, '/'); slash >= 0 {
		m = m[slash+1:]
	}
	m = strings.TrimPrefix(m, "anthropic.")
	m = strings.TrimSuffix(m, "[1m]")
	m = strings.TrimSuffix(m, "-thinking")
	return m
}

func claudeModelFamily(m string) string {
	m = stripModelPathAndDecorations(m)
	if sub := claudeNewFamilyRe.FindStringSubmatch(m); sub != nil {
		return joinClaudeFamily(sub[1], sub[2], sub[3])
	}
	if sub := claudeOldFamilyRe.FindStringSubmatch(m); sub != nil {
		return joinClaudeFamily(sub[3], sub[1], sub[2])
	}
	return ""
}

func joinClaudeFamily(tier, major, minor string) string {
	fam := "claude-" + tier + "-" + major
	if minor != "" {
		fam += "-" + minor
	}
	return fam
}

func geminiModelFamily(m string) string {
	if slash := strings.LastIndexByte(m, '/'); slash >= 0 {
		m = m[slash+1:]
	}
	sub := geminiFamilyRe.FindStringSubmatch(m)
	if sub == nil {
		return ""
	}
	return "gemini-" + sub[1] + "-" + sub[2]
}
