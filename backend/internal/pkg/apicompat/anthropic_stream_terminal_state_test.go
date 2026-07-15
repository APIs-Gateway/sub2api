package apicompat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func anthropicStreamingStateForTerminalTest(t *testing.T, stopReason string) *AnthropicEventToResponsesState {
	t.Helper()
	state := NewAnthropicEventToResponsesState()
	AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:    "msg_terminal_test",
			Model: "claude-opus-4-6",
		},
	}, state)
	AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type:  "message_delta",
		Delta: &AnthropicDelta{StopReason: stopReason},
		Usage: &AnthropicUsage{OutputTokens: 4096},
	}, state)
	return state
}

func assertAnthropicResponsesTerminalEvent(t *testing.T, events []ResponsesStreamEvent, eventType, status string, incompleteReason string) {
	t.Helper()
	require.Len(t, events, 1)
	assert.Equal(t, eventType, events[0].Type)
	require.NotNil(t, events[0].Response)
	assert.Equal(t, status, events[0].Response.Status)
	if incompleteReason == "" {
		assert.Nil(t, events[0].Response.IncompleteDetails)
		return
	}
	require.NotNil(t, events[0].Response.IncompleteDetails)
	assert.Equal(t, incompleteReason, events[0].Response.IncompleteDetails.Reason)
}

func TestAnthropicStreamingMaxTokensMessageStopMapsToIncomplete(t *testing.T) {
	state := anthropicStreamingStateForTerminalTest(t, "max_tokens")

	events := AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)
	assertAnthropicResponsesTerminalEvent(t, events, "response.incomplete", "incomplete", "max_output_tokens")
	assert.Empty(t, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state))
}

func TestAnthropicStreamingMaxTokensFinalizeMapsToIncomplete(t *testing.T) {
	state := anthropicStreamingStateForTerminalTest(t, "max_tokens")

	events := FinalizeAnthropicResponsesStream(state)
	assertAnthropicResponsesTerminalEvent(t, events, "response.incomplete", "incomplete", "max_output_tokens")
	assert.Empty(t, FinalizeAnthropicResponsesStream(state))
}

func TestAnthropicStreamingEndTurnRemainsCompleted(t *testing.T) {
	state := anthropicStreamingStateForTerminalTest(t, "end_turn")

	events := AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)
	assertAnthropicResponsesTerminalEvent(t, events, "response.completed", "completed", "")
}
