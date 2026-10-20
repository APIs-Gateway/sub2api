//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These tests cover the fix for CN Anthropic-compatible providers (Kimi, GLM,
// DeepSeek, ...) sending OpenAI-style prompt/cache aliases (prompt_tokens,
// cached_tokens, prompt_tokens_details.cached_tokens,
// prompt_cache_hit/miss_tokens) alongside (or instead of correctly populated)
// Anthropic-native input_tokens/cache_read_input_tokens/
// cache_creation_input_tokens fields.
//
// Billing (see BillingService.calculateTokenCost) treats InputTokens,
// CacheReadInputTokens and CacheCreationInputTokens as mutually-exclusive
// buckets that sum to the total context. Before this fix, a raw OpenAI-style
// total leaking into InputTokens while CacheReadInputTokens is also populated
// from a fallback field double-counts the cached portion, silently
// overcharging the customer for every cached token.

func TestNormalizeAnthropicCompatiblePromptUsage(t *testing.T) {
	t.Parallel()

	t.Run("native Anthropic usage is left untouched", func(t *testing.T) {
		usage := &ClaudeUsage{InputTokens: 42, CacheReadInputTokens: 7}
		node := gjson.Parse(`{"input_tokens":42,"output_tokens":10,"cache_read_input_tokens":7}`)
		require.False(t, normalizeAnthropicCompatiblePromptUsage(node, usage))
		require.Equal(t, 42, usage.InputTokens)
		require.Equal(t, 7, usage.CacheReadInputTokens)
	})

	t.Run("Kimi top-level cached_tokens", func(t *testing.T) {
		usage := &ClaudeUsage{}
		node := gjson.Parse(`{"input_tokens":173306,"output_tokens":166,"cache_read_input_tokens":173056,"prompt_tokens":173306,"cached_tokens":173056}`)
		require.True(t, normalizeAnthropicCompatiblePromptUsage(node, usage))
		require.Equal(t, 250, usage.InputTokens)
		require.Equal(t, 173056, usage.CacheReadInputTokens)
	})

	t.Run("GLM nested prompt_tokens_details.cached_tokens", func(t *testing.T) {
		usage := &ClaudeUsage{}
		node := gjson.Parse(`{"input_tokens":1200,"output_tokens":300,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}`)
		require.True(t, normalizeAnthropicCompatiblePromptUsage(node, usage))
		require.Equal(t, 400, usage.InputTokens)
		require.Equal(t, 800, usage.CacheReadInputTokens)
	})

	t.Run("DeepSeek prompt_cache_hit/miss buckets", func(t *testing.T) {
		usage := &ClaudeUsage{}
		node := gjson.Parse(`{"input_tokens":1200,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}`)
		require.True(t, normalizeAnthropicCompatiblePromptUsage(node, usage))
		require.Equal(t, 400, usage.InputTokens)
		require.Equal(t, 800, usage.CacheReadInputTokens)
	})
}

func TestParseSSEUsagePassthroughNormalizesKimiPromptUsage(t *testing.T) {
	svc := &GatewayService{}
	usage := &ClaudeUsage{}

	svc.parseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":{"input_tokens":173306,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"prompt_tokens":173306,"cached_tokens":0}}}`, usage)
	require.Equal(t, 173306, usage.InputTokens)
	require.Zero(t, usage.CacheReadInputTokens)

	svc.parseSSEUsagePassthrough(`{"type":"message_delta","usage":{"input_tokens":250,"cache_creation_input_tokens":0,"cache_read_input_tokens":173056,"output_tokens":166,"prompt_tokens":173306,"cached_tokens":173056}}`, usage)
	require.Equal(t, 250, usage.InputTokens, "Kimi message_delta input_tokens is already the uncached bucket")
	require.Equal(t, 173056, usage.CacheReadInputTokens)
	require.Equal(t, 166, usage.OutputTokens)
}

// TestParseSSEUsagePassthroughKimiFullyCachedInputReplacesStartTotal reproduces
// the exact bug this fix addresses: on a fully-cached follow-up turn, Kimi's
// message_delta reports input_tokens=0 (correctly: zero *fresh* input). The
// pre-fix code only overwrote usage.InputTokens when the delta value was > 0
// (a guard meant to ignore absent fields), so an explicit legitimate zero
// left message_start's raw total (173306) in place while
// cache_read_input_tokens was also set to 173306 from the delta -- double
// counting the same 173306 tokens and inflating the bill.
func TestParseSSEUsagePassthroughKimiFullyCachedInputReplacesStartTotal(t *testing.T) {
	svc := &GatewayService{}
	usage := &ClaudeUsage{}

	svc.parseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":{"input_tokens":173306,"prompt_tokens":173306}}}`, usage)
	svc.parseSSEUsagePassthrough(`{"type":"message_delta","usage":{"input_tokens":0,"cache_read_input_tokens":173306,"output_tokens":8,"prompt_tokens":173306,"cached_tokens":173306}}`, usage)

	require.Zero(t, usage.InputTokens, "an explicit zero uncached bucket must not retain message_start's total")
	require.Equal(t, 173306, usage.CacheReadInputTokens)
}

func TestParseClaudeUsageFromResponseBodyNormalizesCNProviderAliases(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantInput     int
		wantCacheRead int
		wantOutput    int
	}{
		{
			name:          "Kimi top-level cached_tokens",
			body:          `{"usage":{"input_tokens":173306,"output_tokens":166,"cache_read_input_tokens":173056,"prompt_tokens":173306,"cached_tokens":173056}}`,
			wantInput:     250,
			wantCacheRead: 173056,
			wantOutput:    166,
		},
		{
			name:          "GLM nested prompt cache details",
			body:          `{"usage":{"input_tokens":1200,"output_tokens":300,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}`,
			wantInput:     400,
			wantCacheRead: 800,
			wantOutput:    300,
		},
		{
			name:          "DeepSeek prompt cache hit and miss buckets",
			body:          `{"usage":{"input_tokens":1200,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}`,
			wantInput:     400,
			wantCacheRead: 800,
			wantOutput:    300,
		},
		{
			name:          "native Anthropic response is untouched",
			body:          `{"usage":{"input_tokens":42,"output_tokens":10,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}}`,
			wantInput:     42,
			wantCacheRead: 7,
			wantOutput:    10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := parseClaudeUsageFromResponseBody([]byte(tt.body))
			require.Equal(t, tt.wantInput, usage.InputTokens)
			require.Equal(t, tt.wantCacheRead, usage.CacheReadInputTokens)
			require.Equal(t, tt.wantOutput, usage.OutputTokens)
		})
	}
}

// TestGatewayForwardMergeThenNormalizeMatchesCallSitePattern replicates the
// exact call sequence added at each merge site in
// gateway_forward_as_chat_completions.go / gateway_forward_as_responses.go
// (mergeAnthropicUsage on the typed apicompat.AnthropicUsage, immediately
// followed by normalizeAnthropicCompatiblePromptUsage on the raw event JSON),
// confirming the two compose correctly for a fully-cached Kimi turn where the
// typed struct alone (no prompt_tokens/cached_tokens fields on
// apicompat.AnthropicUsage) cannot express the fix.
func TestGatewayForwardMergeThenNormalizeMatchesCallSitePattern(t *testing.T) {
	var usage ClaudeUsage

	startPayload := `{"type":"message_start","message":{"usage":{"input_tokens":173306,"prompt_tokens":173306}}}`
	var startEvent apicompat.AnthropicStreamEvent
	require.NoError(t, json.Unmarshal([]byte(startPayload), &startEvent))
	mergeAnthropicUsage(&usage, startEvent.Message.Usage)
	normalizeAnthropicCompatiblePromptUsage(gjson.Get(startPayload, "message.usage"), &usage)
	require.Equal(t, 173306, usage.InputTokens)

	deltaPayload := `{"type":"message_delta","usage":{"input_tokens":0,"cache_read_input_tokens":173306,"output_tokens":8,"prompt_tokens":173306,"cached_tokens":173306}}`
	var deltaEvent apicompat.AnthropicStreamEvent
	require.NoError(t, json.Unmarshal([]byte(deltaPayload), &deltaEvent))
	require.NotNil(t, deltaEvent.Usage)
	mergeAnthropicUsage(&usage, *deltaEvent.Usage)
	normalizeAnthropicCompatiblePromptUsage(gjson.Get(deltaPayload, "usage"), &usage)

	require.Zero(t, usage.InputTokens, "fully-cached follow-up turn must not retain message_start's raw total")
	require.Equal(t, 173306, usage.CacheReadInputTokens)
	require.Equal(t, 8, usage.OutputTokens)
}

// TestCNProviderAnthropicUsageBillsUncachedInputNotDoubleCounted proves the
// fix's effect in dollar terms. It feeds a realistic Kimi non-streaming usage
// payload (identical in shape to the fixtures above) through
// parseClaudeUsageFromResponseBody and then through BillingService's actual
// token-cost calculation (computeTokenBreakdown, the same function every
// gateway path funnels through) with hand-picked, deterministic per-token
// prices -- so the assertion is a plain, independently-computable
// multiplication rather than a lookup into live/dynamic pricing data.
//
// Without the fix, InputTokens would still hold Kimi's raw total (173306)
// while CacheReadInputTokens also holds 173056 (picked up from the
// cached_tokens fallback already present in parseClaudeUsageFromResponseBody),
// so computeTokenBreakdown would charge the full input rate on 173306 tokens
// AND the cache-read rate on 173056 of those same tokens -- a real,
// reproducible overcharge. With the fix, InputTokens is the correct
// uncached remainder (250) and the input-rate charge reflects only those
// fresh tokens.
func TestCNProviderAnthropicUsageBillsUncachedInputNotDoubleCounted(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":173306,"output_tokens":166,"prompt_tokens":173306,"cached_tokens":173056}}`)
	usage := parseClaudeUsageFromResponseBody(body)
	require.Equal(t, 250, usage.InputTokens)
	require.Equal(t, 173056, usage.CacheReadInputTokens)
	require.Equal(t, 166, usage.OutputTokens)

	const (
		inputPricePerToken     = 0.000003  // $3 / 1M tokens
		outputPricePerToken    = 0.000015  // $15 / 1M tokens
		cacheReadPricePerToken = 0.0000003 // $0.30 / 1M tokens
	)
	pricing := &ModelPricing{
		InputPricePerToken:     inputPricePerToken,
		OutputPricePerToken:    outputPricePerToken,
		CacheReadPricePerToken: cacheReadPricePerToken,
	}

	billing := &BillingService{}
	breakdown := billing.computeTokenBreakdown(pricing, UsageTokens{
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.CacheReadInputTokens,
	}, 1.0, "", false)

	wantInputCost := float64(250) * inputPricePerToken
	wantOutputCost := float64(166) * outputPricePerToken
	wantCacheReadCost := float64(173056) * cacheReadPricePerToken

	require.InDelta(t, wantInputCost, breakdown.InputCost, 1e-12,
		"input cost must be charged on the 250 uncached tokens only, not the raw 173306 total")
	require.InDelta(t, wantOutputCost, breakdown.OutputCost, 1e-12)
	require.InDelta(t, wantCacheReadCost, breakdown.CacheReadCost, 1e-12)

	wantTotalCost := wantInputCost + wantOutputCost + wantCacheReadCost
	require.InDelta(t, wantTotalCost, breakdown.TotalCost, 1e-9)

	// The double-counted (pre-fix) total would have charged the input rate on
	// 173306 tokens instead of 250, i.e. materially more than the correct
	// total computed above.
	doubleCountedInputCost := float64(173306) * inputPricePerToken
	require.Greater(t, doubleCountedInputCost, wantInputCost*100,
		"sanity check: the bug this test guards against would be a large, not marginal, overcharge")
}
