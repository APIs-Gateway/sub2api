package apicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatLengthTerminal_EventMatchesStatus(t *testing.T) {
	for _, reason := range []string{"length", "stop", "tool_calls", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			state := NewChatCompletionsToResponsesStreamState("gpt-5")
			state.FinishReason = reason
			state.Usage = &ResponsesUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
			events := FinalizeChatCompletionsResponsesStream(state)
			terminal := events[len(events)-1]
			wantEvent, wantStatus := "response.completed", "completed"
			if reason == "length" {
				wantEvent, wantStatus = "response.incomplete", "incomplete"
				require.Equal(t, "max_output_tokens", terminal.Response.IncompleteDetails.Reason)
			} else {
				require.Nil(t, terminal.Response.IncompleteDetails)
			}
			require.Equal(t, wantEvent, terminal.Type)
			require.Equal(t, wantStatus, terminal.Response.Status)
			require.Equal(t, state.Usage, terminal.Response.Usage)
			require.Equal(t, state.ResponseID, terminal.Response.ID)
			sse, err := ResponsesEventToSSE(terminal)
			require.NoError(t, err)
			require.Contains(t, sse, "event: "+wantEvent+"\n")
			require.Empty(t, FinalizeChatCompletionsResponsesStream(state), "terminal emitted once")
		})
	}
}
