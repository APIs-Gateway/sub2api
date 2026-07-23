package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	grokConversationIDHeader         = "X-Grok-Conv-Id"
	grokClientToolCacheOptInHeader   = "X-Sub2API-Grok-Client-Tool-Cache"
	openCodeSessionAffinityHeader    = "X-Session-Affinity"
	openCodeSessionIDHeader          = "X-Session-Id"
	openCodeNativeSessionHeader      = "X-OpenCode-Session"
	codeBuddyConversationHeader      = "X-Conversation-ID"
	grokClientToolCacheOptInExtraKey = "grok_client_tool_cache_enabled"
	grokFreeCacheNativeToolsJSON     = `[{"type":"web_search"},{"type":"x_search"}]`
	grokFreeCacheDisabledToolChoice  = "none"
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
	if seed == "" {
		seed = explicitGrokOnlyHeaderSessionID(c)
	}
	if seed == "" && len(body) > 0 {
		seed = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if seed == "" {
		seed = strings.TrimSpace(explicitKey)
	}
	return seed
}

// explicitGrokOnlyHeaderSessionID keeps client-specific Grok/IDE session
// headers out of the shared OpenAI sticky-session path.
func explicitGrokOnlyHeaderSessionID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	for _, header := range []string{
		openCodeSessionAffinityHeader,
		openCodeSessionIDHeader,
		openCodeNativeSessionHeader,
		codeBuddyConversationHeader,
		grokConversationIDHeader,
	} {
		if sessionID := strings.TrimSpace(c.GetHeader(header)); sessionID != "" {
			return sessionID
		}
	}
	return ""
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
// Known Free OAuth requests without explicit tool intent receive disabled
// native search tools so xAI selects its cache-capable tier.
func applyGrokResponsesCacheIdentity(body, intentSourceBody []byte, identity string, injectFreeTierTools bool) ([]byte, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		if gjson.GetBytes(body, "prompt_cache_key").Exists() {
			return sjson.DeleteBytes(body, "prompt_cache_key")
		}
		return body, nil
	}
	out, err := sjson.SetBytes(body, "prompt_cache_key", identity)
	if err != nil || !injectFreeTierTools {
		return out, err
	}
	if hasGrokResponsesToolIntent(intentSourceBody) {
		return out, nil
	}
	out, err = sjson.SetRawBytes(out, "tools", []byte(grokFreeCacheNativeToolsJSON))
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(out, "tool_choice", grokFreeCacheDisabledToolChoice)
}

func hasGrokResponsesToolIntent(body []byte) bool {
	if gjson.GetBytes(body, "tools").Exists() || gjson.GetBytes(body, "tool_choice").Exists() {
		return true
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
			continue
		}
		tools := item.Get("tools")
		if !tools.Exists() || !tools.IsArray() || len(tools.Array()) > 0 {
			return true
		}
	}
	return false
}

func applyGrokFreeMessagesFunctionToolCacheRoute(body, intentSourceBody []byte, account *Account, cacheIdentity string) ([]byte, error) {
	allowPureClientTools := account != nil && account.getExtraBool(grokClientToolCacheOptInExtraKey)
	return applyGrokFreeToolCacheRoute(body, intentSourceBody, account, cacheIdentity, allowPureClientTools, true)
}

// applyGrokFreeRequestToolCacheRoute adds request-scoped policy for native
// Responses requests. The opt-in header is consumed locally and is never
// forwarded to xAI.
func applyGrokFreeRequestToolCacheRoute(c *gin.Context, body, intentSourceBody []byte, account *Account, cacheIdentity string) ([]byte, error) {
	allowPureClientTools := account != nil && account.getExtraBool(grokClientToolCacheOptInExtraKey)
	requestOptOut := false
	if c != nil {
		switch strings.ToLower(strings.TrimSpace(c.GetHeader(grokClientToolCacheOptInHeader))) {
		case "1", "true", "yes", "on", "prefer-cache":
			allowPureClientTools = true
		case "0", "false", "no", "off":
			allowPureClientTools = false
			requestOptOut = true
		}
	}
	if !allowPureClientTools && !requestOptOut && isGrokClaudeDesktopResponsesCacheRequest(c) {
		allowPureClientTools = true
	}
	return applyGrokFreeToolCacheRoute(body, intentSourceBody, account, cacheIdentity, allowPureClientTools, allowPureClientTools)
}

// isGrokClaudeDesktopResponsesCacheRequest requires the independent markers
// emitted by Claude Desktop's Responses compatibility path. This prevents a
// generic Claude-compatible client from silently changing tool routing.
func isGrokClaudeDesktopResponsesCacheRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || isOpenAIResponsesCompactPath(c) {
		return false
	}
	path := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	if !strings.HasSuffix(path, "/responses") {
		return false
	}
	if !claudeCodeUAPattern.MatchString(strings.TrimSpace(c.GetHeader("User-Agent"))) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.GetHeader("X-App"))) {
	case "cli", "cli-bg":
	default:
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(c.GetHeader("anthropic-client-platform")), "desktop_app") {
		return false
	}
	return strings.TrimSpace(c.GetHeader("X-Claude-Code-Session-Id")) != ""
}

func applyGrokFreeToolCacheRoute(body, intentSourceBody []byte, account *Account, cacheIdentity string, allowPureClientTools, allowFunctionSearch bool) ([]byte, error) {
	if strings.TrimSpace(cacheIdentity) == "" || !isKnownGrokFreeAccount(account) {
		return body, nil
	}
	intentTools := gjson.GetBytes(intentSourceBody, "tools")
	intentToolChoice := gjson.GetBytes(intentSourceBody, "tool_choice")
	if !isGrokFreeCacheFunctionToolIntent(intentTools, intentToolChoice) {
		return body, nil
	}
	if intentToolChoice.Type == gjson.String && strings.TrimSpace(intentToolChoice.String()) == grokFreeCacheDisabledToolChoice {
		// Adding native cache-routing tools cannot change behavior when the
		// client has explicitly disabled all tool execution.
		return appendGrokFreeCacheNativeToolsWithPolicy(body, true, false)
	}
	return appendGrokFreeCacheNativeToolsWithPolicy(body, allowPureClientTools, allowFunctionSearch)
}

func isKnownGrokFreeAccount(account *Account) bool {
	if account == nil || !account.IsGrokOAuth() {
		return false
	}
	freeSignal := false
	paidSignal := false
	inferredFreeSignal := false
	if snapshot, err := grokQuotaSnapshotFromExtra(account.Extra); err == nil && snapshot != nil {
		tier := strings.TrimSpace(snapshot.SubscriptionTier)
		if isGrokFreeSubscriptionTier(tier) {
			freeSignal = true
		} else if !isGrokUnknownSubscriptionTier(tier) {
			paidSignal = true
		}
		if snapshot.Tokens != nil && snapshot.Tokens.Limit != nil &&
			xai.IsGrokFreeRolling24hTokenLimit(*snapshot.Tokens.Limit) {
			inferredFreeSignal = true
		}
	}
	tier := strings.TrimSpace(account.GetCredential("subscription_tier"))
	if isGrokFreeSubscriptionTier(tier) {
		freeSignal = true
	} else if !isGrokUnknownSubscriptionTier(tier) {
		paidSignal = true
	}
	return !paidSignal && (freeSignal || inferredFreeSignal)
}

func isGrokFreeSubscriptionTier(tier string) bool {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "free", "grok-free", "grok_free", "free-tier", "free_tier", "basic", "grok-basic", "grok_basic":
		return true
	default:
		return false
	}
}

func isGrokUnknownSubscriptionTier(tier string) bool {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "unknown", "n/a", "none":
		return true
	default:
		return false
	}
}

func isGrokFreeCacheFunctionToolIntent(tools, toolChoice gjson.Result) bool {
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	for _, tool := range tools.Array() {
		if !tool.IsObject() {
			return false
		}
		toolType := strings.TrimSpace(tool.Get("type").String())
		if _, ok := grokResponsesSupportedToolTypes[toolType]; !ok {
			return false
		}
		if toolType == "function" {
			if strings.TrimSpace(tool.Get("name").String()) == "" || tool.Get("function").Exists() {
				return false
			}
		}
	}
	if !toolChoice.Exists() {
		return true
	}
	if toolChoice.Type != gjson.String {
		return false
	}
	switch strings.TrimSpace(toolChoice.String()) {
	case "auto", grokFreeCacheDisabledToolChoice:
		return true
	default:
		return false
	}
}

func appendGrokFreeCacheNativeToolsWithPolicy(body []byte, allowPureClientTools, allowFunctionSearch bool) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0 {
		return body, nil
	}
	items := tools.Array()
	hasNativeSearch := false
	for _, tool := range items {
		switch strings.TrimSpace(tool.Get("type").String()) {
		case "web_search", "x_search":
			hasNativeSearch = true
		}
	}
	if !allowPureClientTools && !allowFunctionSearch && !hasNativeSearch {
		return body, nil
	}
	merged := make([]json.RawMessage, 0, len(tools.Array())+2)
	present := make(map[string]bool, 2)
	hasCompanionTool := false
	for _, tool := range tools.Array() {
		toolType := strings.TrimSpace(tool.Get("type").String())
		switch toolType {
		case "function":
			name := strings.TrimSpace(tool.Get("name").String())
			if !tool.IsObject() || name == "" || tool.Get("function").Exists() {
				return body, nil
			}
			if (name == "web_search" || name == "x_search") && allowFunctionSearch {
				if present[name] {
					continue
				}
				raw, err := json.Marshal(map[string]string{"type": name})
				if err != nil {
					return nil, err
				}
				merged = append(merged, raw)
				present[name] = true
				if allowPureClientTools {
					hasCompanionTool = true
				}
				continue
			}
			if name == "web_search" || name == "x_search" {
				// Keep client functions intact unless conversion is explicitly enabled.
				present[name] = true
			}
			hasCompanionTool = true
			merged = append(merged, json.RawMessage(tool.Raw))
		case "web_search", "x_search":
			if present[toolType] {
				continue
			}
			merged = append(merged, json.RawMessage(tool.Raw))
			present[toolType] = true
		default:
			if _, ok := grokResponsesSupportedToolTypes[toolType]; !ok {
				return body, nil
			}
			hasCompanionTool = true
			merged = append(merged, json.RawMessage(tool.Raw))
		}
	}
	if !hasCompanionTool {
		return body, nil
	}
	if !allowPureClientTools && !present["web_search"] && !present["x_search"] {
		return body, nil
	}
	for _, toolType := range []string{"web_search", "x_search"} {
		if present[toolType] {
			continue
		}
		raw, err := json.Marshal(map[string]string{"type": toolType})
		if err != nil {
			return nil, err
		}
		merged = append(merged, raw)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encoded)
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
