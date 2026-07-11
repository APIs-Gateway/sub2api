package apicompat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesEventToAnthropicEvents_StreamingCacheCreation(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	state.MessageStartSent = true

	completedEvt := &ResponsesStreamEvent{
		Type: "response.completed",
		Response: &ResponsesResponse{
			Status: "completed",
			Usage: &ResponsesUsage{
				InputTokens:              20,
				OutputTokens:             5,
				CacheCreationInputTokens: 6,
				InputTokensDetails: &ResponsesInputTokensDetails{
					CachedTokens: 4,
				},
			},
		},
	}

	events := ResponsesEventToAnthropicEvents(completedEvt, state)

	var deltaEvt *AnthropicStreamEvent
	for i := range events {
		if events[i].Type == "message_delta" {
			deltaEvt = &events[i]
			break
		}
	}
	require.NotNil(t, deltaEvt, "should have message_delta event")
	require.NotNil(t, deltaEvt.Usage)
	assert.Equal(t, 6, deltaEvt.Usage.CacheCreationInputTokens, "streaming cache_creation must be preserved")
	assert.Equal(t, 10, deltaEvt.Usage.InputTokens, "input = 20 - 4(read) - 6(creation)")
	assert.Equal(t, 4, deltaEvt.Usage.CacheReadInputTokens)
}
