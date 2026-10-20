package apicompat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesToChatCompletions_ContentFilterMapsToFinishReason(t *testing.T) {
	response := &ResponsesResponse{
		ID:     "resp_content_filter",
		Status: "incomplete",
		IncompleteDetails: &ResponsesIncompleteDetails{
			Reason: "content_filter",
		},
	}

	chat := ResponsesToChatCompletions(response, "gpt-5.6")
	require.Len(t, chat.Choices, 1)
	assert.Equal(t, "content_filter", chat.Choices[0].FinishReason)
}

func TestResponsesEventToChatChunks_ContentFilterMapsToFinishReason(t *testing.T) {
	state := NewResponsesEventToChatState()
	state.ID = "resp_content_filter"
	state.Model = "gpt-5.6"
	state.SentRole = true

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type: "response.incomplete",
		Response: &ResponsesResponse{
			ID:     "resp_content_filter",
			Status: "incomplete",
			IncompleteDetails: &ResponsesIncompleteDetails{
				Reason: "content_filter",
			},
		},
	}, state)

	require.Len(t, chunks, 1)
	require.Len(t, chunks[0].Choices, 1)
	require.NotNil(t, chunks[0].Choices[0].FinishReason)
	assert.Equal(t, "content_filter", *chunks[0].Choices[0].FinishReason)
}
