package claude

import (
	"strings"
	"unicode"
)

// Opus55ModelID is the fixed public ID of Claude Opus 5.5.
const Opus55ModelID = "claude-opus-5-5"

// IsOpus55 identifies Claude Opus 5.5 after provider-prefix, "-thinking" and
// date-snapshot normalization (e.g. "anthropic/claude-opus-5-5-20260922").
// It deliberately does not match claude-opus-5 or claude-opus-4-5.
func IsOpus55(model string) bool {
	return normalizeClaudeModelIDForFamily(model) == Opus55ModelID
}

func normalizeClaudeModelIDForFamily(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	id = strings.TrimPrefix(id, "models/")
	if slash := strings.IndexByte(id, '/'); slash >= 0 {
		id = strings.TrimPrefix(strings.TrimSpace(id[slash+1:]), "models/")
	}
	id = strings.TrimPrefix(id, "anthropic.")
	id = strings.TrimSuffix(id, "-thinking")
	if mapped, ok := ModelIDReverseOverrides[id]; ok {
		id = mapped
	}
	if len(id) >= 9 {
		suffix := id[len(id)-9:]
		if suffix[0] == '-' {
			digits := true
			for _, r := range suffix[1:] {
				if !unicode.IsDigit(r) {
					digits = false
					break
				}
			}
			if digits {
				id = id[:len(id)-9]
			}
		}
	}
	return id
}
