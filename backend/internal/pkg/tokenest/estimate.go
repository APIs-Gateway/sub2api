// Package tokenest provides lightweight input-token estimation for the
// gateway. It exists so oversized prompts can be rejected before a request is
// forwarded upstream, where an over-limit prompt would still be billed for its
// input tokens even though the provider returns nothing usable.
package tokenest

import (
	"strings"

	"github.com/tidwall/gjson"
)

// EstimateText approximates the token count of a text fragment.
//
// The heuristic matches the one already used for Gemini count-tokens
// estimation: predominantly ASCII text averages roughly 4 characters per
// token, while CJK-heavy text is closer to one rune per token.
func EstimateText(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	runes := []rune(s)
	if len(runes) == 0 {
		return 0
	}
	ascii := 0
	for _, r := range runes {
		if r <= 0x7f {
			ascii++
		}
	}
	if float64(ascii)/float64(len(runes)) >= 0.8 {
		// Roughly 4 chars per token for English-like text.
		return (len(runes) + 3) / 4
	}
	// For CJK-heavy text, approximate 1 rune per token.
	return len(runes)
}

// EstimateJSONBody sums the estimated token count of every string value in a
// JSON request body.
//
// The walk is deliberately format-agnostic. OpenAI Responses, Chat
// Completions, Anthropic Messages and Gemini all carry prompt text as JSON
// strings, so summing every string covers all gateway routes without needing
// per-format parsing. Base64 image payloads are counted too, which is the
// desired behaviour: they consume upstream input tokens just like text.
//
// A non-JSON body yields 0, letting callers fall back to their own limits.
func EstimateJSONBody(body []byte) int {
	total := 0
	var walk func(gjson.Result)
	walk = func(r gjson.Result) {
		switch r.Type {
		case gjson.String:
			total += EstimateText(r.String())
		case gjson.JSON:
			r.ForEach(func(_, v gjson.Result) bool {
				walk(v)
				return true
			})
		}
	}
	walk(gjson.ParseBytes(body))
	if total < 0 {
		return 0
	}
	return total
}

// WithinLimit reports whether body stays within maxTokens, along with the
// estimate that was used. A maxTokens of zero or below disables the check.
//
// The byte-length shortcut keeps this free for normal traffic: the estimator
// never returns more than one token per rune, and the shortest UTF-8 rune that
// maps to a whole token occupies a single byte, so a body shorter than
// maxTokens bytes can never exceed maxTokens tokens. Only genuinely large
// bodies are parsed.
func WithinLimit(body []byte, maxTokens int) (estimated int, ok bool) {
	if maxTokens <= 0 {
		return 0, true
	}
	if len(body) < maxTokens {
		return 0, true
	}
	estimated = EstimateJSONBody(body)
	return estimated, estimated <= maxTokens
}
