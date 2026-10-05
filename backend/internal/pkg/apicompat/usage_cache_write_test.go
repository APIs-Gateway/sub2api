package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheWriteUsage_ForkPriorityAndRoundTrips(t *testing.T) {
	cases := []struct {
		name, fields string
		want         int
		write        bool
	}{
		{"creation_input", `"cache_creation_input_tokens":800`, 800, false},
		{"creation", `"cache_creation_tokens":800`, 800, false},
		{"write", `"cache_write_tokens":800`, 800, false},
		{"top_priority", `"cache_creation_input_tokens":800,"cache_creation_tokens":700,"cache_write_tokens":600`, 800, false},
		{"top_first_positive", `"cache_creation_input_tokens":0,"cache_creation_tokens":700,"cache_write_tokens":600`, 700, false},
		{"top_null_fallback", `"cache_creation_input_tokens":null,"cache_creation_tokens":700`, 700, false},
		{"top_negative_fallback", `"cache_creation_input_tokens":-1,"cache_creation_tokens":700`, 700, false},
		{"unsupported_write_input", `"cache_write_input_tokens":800`, 0, false},
		{"nested_write", `"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":600,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 600, true},
		{"nested_creation", `"prompt_tokens_details":{"cached_tokens":100,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 800, false},
		{"nested_write_zero", `"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":0,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 0, true},
		{"nested_write_null", `"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":null,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 0, true},
		{"nested_write_negative", `"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":-1,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 0, true},
		{"input_write_beats_prompt_write", `"input_tokens_details":{"cache_write_tokens":500},"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":600}`, 500, true},
		{"prompt_write_beats_input_creation", `"input_tokens_details":{"cache_creation_tokens":500},"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":600}`, 600, true},
		{"input_null_beats_prompt_positive", `"input_tokens_details":{"cache_write_tokens":null},"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":600}`, 0, true},
		{"input_creation_beats_prompt_creation", `"input_tokens_details":{"cache_creation_tokens":500},"prompt_tokens_details":{"cached_tokens":100,"cache_creation_tokens":600}`, 500, false},
		{"top_zero", `"cache_creation_input_tokens":0,"cache_creation_tokens":0,"cache_write_tokens":0`, 0, false},
		{"no_writes", `"vendor_field":1`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, shape := range []string{"chat", "responses"} {
				t.Run(shape, func(t *testing.T) {
					var chat *ChatUsage
					if shape == "chat" {
						chat = &ChatUsage{}
						require.NoError(t, json.Unmarshal([]byte(`{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,`+tc.fields+`}`), chat))
					} else {
						var usage ResponsesUsage
						require.NoError(t, json.Unmarshal([]byte(`{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,`+tc.fields+`}`), &usage))
						require.Equal(t, tc.want, usage.CacheCreationInputTokens)
						chat = chatUsageFromResponsesUsage(&usage)
					}
					cached := 0
					if chat.PromptTokensDetails != nil {
						cached = chat.PromptTokensDetails.CachedTokens
						if tc.write {
							require.Zero(t, chat.PromptTokensDetails.CacheCreationTokens)
							require.Equal(t, tc.want, chat.PromptTokensDetails.CacheWriteTokens)
						} else {
							require.Zero(t, chat.PromptTokensDetails.CacheWriteTokens)
						}
					}
					for round := 0; round < 3; round++ {
						anthropic := chatUsageToAnthropicUsage(chat)
						require.Equal(t, tc.want, anthropic.CacheCreationInputTokens)
						require.Equal(t, 1000-cached-tc.want, anthropic.InputTokens)
						require.Equal(t, 50, anthropic.OutputTokens)
						responses := ChatUsageToResponsesUsage(chat)
						require.Equal(t, tc.want, responses.CacheCreationInputTokens)
						encoded, err := json.Marshal(responses)
						require.NoError(t, err)
						var decoded ResponsesUsage
						require.NoError(t, json.Unmarshal(encoded, &decoded))
						chat = chatUsageFromResponsesUsage(&decoded)
					}
				})
			}
		})
	}
}

func TestCacheWriteUsage_UnrelatedDetailsUnchanged(t *testing.T) {
	payload := []byte(`{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1077,"cache_creation_input_tokens":800,"cache_read_input_tokens":900,"prompt_tokens_details":{"cached_tokens":100,"audio_tokens":2},"completion_tokens_details":{"audio_tokens":3,"reasoning_tokens":4,"accepted_prediction_tokens":5,"rejected_prediction_tokens":6}}`)
	var chat ChatUsage
	require.NoError(t, json.Unmarshal(payload, &chat))
	require.Equal(t, 1077, chat.TotalTokens)
	require.Equal(t, 100, chat.PromptTokensDetails.CachedTokens, "do not introduce top-level read aliases")
	require.Equal(t, 2, chat.PromptTokensDetails.AudioTokens)
	require.Equal(t, &ChatTokenDetails{AudioTokens: 3, ReasoningTokens: 4, AcceptedPredictionTokens: 5, RejectedPredictionTokens: 6}, chat.CompletionTokensDetails)
	var responses ResponsesUsage
	require.NoError(t, json.Unmarshal(payload, &responses))
	require.Equal(t, 1077, responses.TotalTokens)
	require.Equal(t, 100, responses.InputTokensDetails.CachedTokens)
	require.Equal(t, 2, responses.InputTokensDetails.AudioTokens)
	require.Equal(t, &ResponsesOutputTokensDetails{AudioTokens: 3, ReasoningTokens: 4, AcceptedPredictionTokens: 5, RejectedPredictionTokens: 6}, responses.OutputTokensDetails)
	for _, payload := range []string{`{"cache_read_input_tokens":900}`, `{"cache_write_input_tokens":800}`} {
		var c ChatUsage
		var r ResponsesUsage
		require.NoError(t, json.Unmarshal([]byte(payload), &c))
		require.NoError(t, json.Unmarshal([]byte(payload), &r))
		require.Nil(t, c.PromptTokensDetails)
		require.Nil(t, r.InputTokensDetails)
		require.Zero(t, r.CacheCreationInputTokens)
	}
}
