package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	grokConversationIDHeader      = "X-Grok-Conv-Id"
	openCodeSessionAffinityHeader = "X-Session-Affinity"
	openCodeSessionIDHeader       = "X-Session-Id"
	openCodeNativeSessionHeader   = "X-OpenCode-Session"
	codeBuddyConversationHeader   = "X-Conversation-ID"
)

// resolveGrokCacheIdentity derives a tenant-isolated identity for xAI's
// server-side prompt cache. Raw client session values never leave sub2api.
func resolveGrokCacheIdentity(c *gin.Context, body []byte, explicitKey, upstreamModel string) string {
	apiKeyID := getAPIKeyIDFromContext(c)
	if apiKeyID <= 0 || isOpenAIResponsesCompactPath(c) {
		return ""
	}

	model := strings.ToLower(strings.TrimSpace(upstreamModel))
	if model == "" {
		return ""
	}

	seed := explicitGrokCacheSeed(c, body, explicitKey)
	if seed == "" {
		seed = deriveOpenAIStablePrefixSessionSeed(body)
		if seed == "" {
			// A model-only request is too broad. Use the first user/input anchor
			// only when no reusable system/tool prefix is available.
			seed = deriveOpenAIAnchoredContentSessionSeed(body)
		}
	}
	if seed == "" {
		return ""
	}

	return generateSessionUUID(fmt.Sprintf("grok-prompt-cache:v1:%d:%s:%s", apiKeyID, model, seed))
}

func explicitGrokCacheSeed(c *gin.Context, body []byte, explicitKey string) string {
	seed := extractClaudeCodeSessionID(c, body)
	if seed == "" {
		seed = explicitOpenAIHeaderSessionID(c)
	}
	if seed == "" && c != nil {
		seed = strings.TrimSpace(c.GetHeader(grokConversationIDHeader))
	}
	if seed == "" && len(body) > 0 {
		seed = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if seed == "" {
		seed = strings.TrimSpace(explicitKey)
	}
	return seed
}

// extractClaudeCodeSessionID accepts the stable Claude Code session header and
// the metadata shape used by the Messages compatibility route.
func extractClaudeCodeSessionID(c *gin.Context, body []byte) string {
	if c != nil {
		if sessionID := strings.TrimSpace(c.GetHeader("X-Claude-Code-Session-Id")); sessionID != "" {
			return sessionID
		}
	}
	userID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String())
	if strings.HasPrefix(userID, "{") {
		return strings.TrimSpace(gjson.Get(userID, "session_id").String())
	}
	const marker = "_session_"
	if index := strings.LastIndex(userID, marker); index >= 0 {
		return strings.TrimSpace(userID[index+len(marker):])
	}
	return ""
}

func isGrokRequestContext(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, exists := c.Get("api_key")
	if !exists {
		return false
	}
	apiKey, ok := value.(*APIKey)
	return ok && apiKey != nil && apiKey.Group != nil && apiKey.Group.Platform == PlatformGrok
}

// applyGrokResponsesCacheIdentity replaces client-provided cache keys with
// the isolated identity. An empty identity removes the compatibility field.
func applyGrokResponsesCacheIdentity(body []byte, identity string) ([]byte, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		if gjson.GetBytes(body, "prompt_cache_key").Exists() {
			return sjson.DeleteBytes(body, "prompt_cache_key")
		}
		return body, nil
	}
	return sjson.SetBytes(body, "prompt_cache_key", identity)
}

func applyGrokCacheHeaders(headers http.Header, identity string) {
	if headers == nil {
		return
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		headers.Del(grokConversationIDHeader)
		return
	}
	headers.Set(grokConversationIDHeader, identity)
}

func stripGrokChatPromptCacheKey(body []byte) ([]byte, error) {
	if !gjson.GetBytes(body, "prompt_cache_key").Exists() {
		return body, nil
	}
	return sjson.DeleteBytes(body, "prompt_cache_key")
}
