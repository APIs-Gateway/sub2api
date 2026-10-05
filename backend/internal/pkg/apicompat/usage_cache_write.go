package apicompat

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

type cacheWriteSource uint8

const (
	cacheWriteAbsent cacheWriteSource = iota
	cacheWriteTopLevel
	cacheWriteNestedWrite
	cacheWriteNestedCreation
)

// cacheWriteCounter mirrors the fork's HTTP openAICacheWriteTokens resolver.
// Nested presence wins, even for null or zero; top-level aliases use the first
// positive value in the existing billing order. Cache-read usage is untouched.
func cacheWriteCounter(usage gjson.Result) (int, cacheWriteSource) {
	for _, field := range []struct {
		path   string
		source cacheWriteSource
	}{
		{"input_tokens_details.cache_write_tokens", cacheWriteNestedWrite},
		{"prompt_tokens_details.cache_write_tokens", cacheWriteNestedWrite},
		{"input_tokens_details.cache_creation_tokens", cacheWriteNestedCreation},
		{"prompt_tokens_details.cache_creation_tokens", cacheWriteNestedCreation},
	} {
		if value := usage.Get(field.path); value.Exists() {
			return max(int(value.Int()), 0), field.source
		}
	}
	for _, field := range []string{"cache_creation_input_tokens", "cache_creation_tokens", "cache_write_tokens"} {
		if tokens := int(usage.Get(field).Int()); tokens > 0 {
			return tokens, cacheWriteTopLevel
		}
	}
	return 0, cacheWriteAbsent
}

// Preserve the winning spelling while clearing the loser. Otherwise a later
// conversion's positive-value fallback could revive a counter that lost to zero.
func normalizeCacheWrite(creation, write *int, tokens int, source cacheWriteSource) {
	*creation, *write = tokens, 0
	if source == cacheWriteNestedWrite {
		*creation, *write = 0, tokens
	}
}

func (u *ResponsesUsage) applyCacheWriteUsage(data []byte) {
	tokens, source := cacheWriteCounter(gjson.ParseBytes(data))
	u.CacheCreationInputTokens = tokens
	if u.InputTokensDetails != nil {
		normalizeCacheWrite(&u.InputTokensDetails.CacheCreationTokens, &u.InputTokensDetails.CacheWriteTokens, tokens, source)
	} else if tokens > 0 && source != cacheWriteTopLevel {
		u.InputTokensDetails = &ResponsesInputTokensDetails{}
		normalizeCacheWrite(&u.InputTokensDetails.CacheCreationTokens, &u.InputTokensDetails.CacheWriteTokens, tokens, source)
	}
}

func (u *ChatUsage) UnmarshalJSON(data []byte) error {
	type chatUsageAlias ChatUsage
	var decoded chatUsageAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*u = ChatUsage(decoded)
	tokens, source := cacheWriteCounter(gjson.ParseBytes(data))
	if u.PromptTokensDetails != nil || tokens > 0 {
		if u.PromptTokensDetails == nil {
			u.PromptTokensDetails = &ChatTokenDetails{}
		}
		normalizeCacheWrite(&u.PromptTokensDetails.CacheCreationTokens, &u.PromptTokensDetails.CacheWriteTokens, tokens, source)
	}
	return nil
}
