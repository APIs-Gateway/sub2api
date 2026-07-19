package apicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicEventToResponses_TextIncludesContentPartAndFullOutput(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.Model = "claude-sonnet-4-5"
	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(evt, state)...)
	}

	idx := 0
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "Hello "}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "world"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	indexOf := func(typ string) int {
		for i, evt := range events {
			if evt.Type == typ {
				return i
			}
		}
		return -1
	}
	partAdded := indexOf("response.content_part.added")
	delta := indexOf("response.output_text.delta")
	require.GreaterOrEqual(t, partAdded, 0)
	require.GreaterOrEqual(t, delta, 0)
	require.Less(t, partAdded, delta)

	var textDone, partDone, completed *ResponsesStreamEvent
	for i := range events {
		switch events[i].Type {
		case "response.output_text.done":
			textDone = &events[i]
		case "response.content_part.done":
			partDone = &events[i]
		case "response.completed":
			completed = &events[i]
		}
	}
	require.NotNil(t, textDone)
	require.Equal(t, "Hello world", textDone.Text)
	require.NotNil(t, partDone)
	require.Equal(t, "Hello world", partDone.Part.Text)
	require.NotNil(t, completed)
	require.Len(t, completed.Response.Output, 1)
	require.Equal(t, "message", completed.Response.Output[0].Type)
	require.Equal(t, "Hello world", completed.Response.Output[0].Content[0].Text)
}

func TestAnthropicEventToResponses_FunctionCallIncludesArgumentsInOutput(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(evt, state)...)
	}

	idx := 0
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_2"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_1", Name: "get_weather"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `{"city":`}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `"SH"}`}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
		}
	}
	require.NotNil(t, completed)
	require.Len(t, completed.Response.Output, 1)
	require.Equal(t, "function_call", completed.Response.Output[0].Type)
	require.Equal(t, `{"city":"SH"}`, completed.Response.Output[0].Arguments)
	require.Equal(t, "get_weather", completed.Response.Output[0].Name)
}

func TestAnthropicEventToResponses_ReasoningIncludesSummaryInOutput(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(evt, state)...)
	}

	idx := 0
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_3"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "thinking"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "thinking"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
		}
	}
	require.NotNil(t, completed)
	require.Len(t, completed.Response.Output, 1)
	require.Equal(t, "reasoning", completed.Response.Output[0].Type)
	require.Len(t, completed.Response.Output[0].Summary, 1)
	require.Equal(t, "thinking", completed.Response.Output[0].Summary[0].Text)
}
