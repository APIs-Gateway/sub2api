package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	openAIWSThreadIDHeader = "thread-id"
	openAIWSWindowIDHeader = "x-codex-window-id"
	openAIWSSubagentHeader = "x-openai-subagent"

	openAIWSRequestKindTurn       = "turn"
	openAIWSRequestKindPrewarm    = "prewarm"
	openAIWSRequestKindCompaction = "compaction"
)

// resolveOpenAIWSClientThreadID selects a stable client-declared identity for
// a Codex execution. Parent and child agents can share session-id, but not a
// thread id, so request content is intentionally never used as a fallback.
func resolveOpenAIWSClientThreadID(c *gin.Context, body []byte) string {
	if c != nil && c.Request != nil {
		if id := strings.TrimSpace(c.GetHeader(openAIWSThreadIDHeader)); id != "" {
			return id
		}
		if id := codexTurnMetadataThreadID(c.GetHeader(openAIWSTurnMetadataHeader)); id != "" {
			return id
		}
		if window := strings.TrimSpace(c.GetHeader(openAIWSWindowIDHeader)); window != "" {
			if id := strings.TrimSpace(strings.SplitN(window, ":", 2)[0]); id != "" {
				return id
			}
		}
	}
	if len(body) == 0 {
		return ""
	}
	if id := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.thread_id").String()); id != "" {
		return id
	}
	return codexTurnMetadataThreadID(gjson.GetBytes(body, "client_metadata."+openAIWSTurnMetadataHeader).String())
}

type codexTurnMetadata struct {
	ThreadID    string `json:"thread_id"`
	RequestKind string `json:"request_kind"`
}

func parseCodexTurnMetadata(raw string) (codexTurnMetadata, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return codexTurnMetadata{}, false
	}
	var metadata codexTurnMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return codexTurnMetadata{}, false
	}
	metadata.ThreadID = strings.TrimSpace(metadata.ThreadID)
	metadata.RequestKind = strings.ToLower(strings.TrimSpace(metadata.RequestKind))
	return metadata, true
}

func codexTurnMetadataThreadID(raw string) string {
	metadata, _ := parseCodexTurnMetadata(raw)
	return metadata.ThreadID
}

func openAIWSExecutionTurnMetadata(c *gin.Context, body []byte) codexTurnMetadata {
	if c != nil && c.Request != nil {
		if metadata, ok := parseCodexTurnMetadata(c.GetHeader(openAIWSTurnMetadataHeader)); ok {
			return metadata
		}
	}
	if len(body) == 0 {
		return codexTurnMetadata{}
	}
	metadata, _ := parseCodexTurnMetadata(gjson.GetBytes(body, "client_metadata."+openAIWSTurnMetadataHeader).String())
	return metadata
}

func openAIWSExecutionSubagent(c *gin.Context, body []byte) string {
	if c != nil && c.Request != nil {
		if subagent := strings.TrimSpace(c.GetHeader(openAIWSSubagentHeader)); subagent != "" {
			return strings.ToLower(subagent)
		}
	}
	return strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "client_metadata."+openAIWSSubagentHeader).String()))
}

// resolveOpenAIWSExecutionLane separates independent work that legitimately
// reuses a client thread id. Normal turns, prewarm and compaction remain one
// lane; memory/unknown kinds and standalone subagents cannot evict that lane.
func resolveOpenAIWSExecutionLane(c *gin.Context, body []byte) string {
	metadata := openAIWSExecutionTurnMetadata(c, body)
	switch metadata.RequestKind {
	case "", openAIWSRequestKindTurn, openAIWSRequestKindPrewarm, openAIWSRequestKindCompaction:
	default:
		return "kind=" + metadata.RequestKind
	}
	if metadata.ThreadID == "" {
		if subagent := openAIWSExecutionSubagent(c, body); subagent != "" {
			return "subagent=" + subagent
		}
	}
	return ""
}

func openAIWSExecutionScopeSeed(apiKeyID int64, identity, value, lane string) string {
	// Length-prefix every user-controlled field. Separators alone are ambiguous:
	// a thread id such as "a|kind=memory" must not share a seed with thread "a"
	// in the "kind=memory" lane.
	return fmt.Sprintf("openai_ws_exec:%d|%d:%s|%d:%s|%d:%s",
		apiKeyID,
		len(identity), identity,
		len(value), value,
		len(lane), lane,
	)
}

// resolveOpenAIWSExecutionScope produces the key used only for WS-owned turn
// state, connection bindings and preemption. It includes API-key isolation and
// requires a declared thread or explicit session identity; content-derived
// sticky affinity is deliberately not safe enough to participate.
func resolveOpenAIWSExecutionScope(c *gin.Context, body []byte, apiKeyID int64) (scope, threadID string) {
	lane := resolveOpenAIWSExecutionLane(c, body)
	if threadID = resolveOpenAIWSClientThreadID(c, body); threadID != "" {
		scope, _ = deriveOpenAISessionHashes(openAIWSExecutionScopeSeed(apiKeyID, "thread", threadID, lane))
		return scope, threadID
	}
	if sessionID := strings.TrimSpace(explicitOpenAISessionID(c, body)); sessionID != "" {
		scope, _ = deriveOpenAISessionHashes(openAIWSExecutionScopeSeed(apiKeyID, "session", sessionID, lane))
		return scope, ""
	}
	return "", ""
}
