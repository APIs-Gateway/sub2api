package service

import (
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	claudeCodeSessionHeader = "X-Claude-Code-Session-Id"
	// maxClaudeCodeSessionIDLength bounds the accepted identifier so an
	// oversized header cannot be used as a routing seed.
	maxClaudeCodeSessionIDLength = 255
)

// ClaudeCodeSessionIDFromHeader returns the stable Claude Code conversation
// identifier carried by X-Claude-Code-Session-Id. It is intentionally exposed
// separately from the OpenAI explicit session resolution: callers that use it
// for routing must make that scope explicit rather than accidentally changing
// every protocol's session semantics.
func ClaudeCodeSessionIDFromHeader(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return sanitizeClaudeCodeSessionID(c.GetHeader(claudeCodeSessionHeader))
}

// sanitizeClaudeCodeSessionID trims the raw header value and rejects values
// that are not valid UTF-8, contain control characters, or exceed the length
// bound. Invalid input yields "".
func sanitizeClaudeCodeSessionID(raw string) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	count := 0
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return ""
		}
		count++
		if count > maxClaudeCodeSessionIDLength {
			return ""
		}
	}
	return trimmed
}
