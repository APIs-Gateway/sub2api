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
	var argumentsDone *ResponsesStreamEvent
	for i := range events {
		switch events[i].Type {
		case "response.function_call_arguments.done":
			argumentsDone = &events[i]
		case "response.completed":
			completed = &events[i]
		}
	}
	require.NotNil(t, argumentsDone)
	require.Equal(t, `{"city":"SH"}`, argumentsDone.Arguments)
	require.NotNil(t, completed)
	require.Len(t, completed.Response.Output, 1)
	require.Equal(t, "function_call", completed.Response.Output[0].Type)
	require.Equal(t, `{"city":"SH"}`, completed.Response.Output[0].Arguments)
	require.Equal(t, "get_weather", completed.Response.Output[0].Name)
}

func TestAnthropicEventToResponses_MultipleTextBlocksAdvanceContentIndex(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(evt, state)...)
	}

	first, second := 0, 1
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_multi_text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &first, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &first, Delta: &AnthropicDelta{Type: "text_delta", Text: "first"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &first})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &second, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &second, Delta: &AnthropicDelta{Type: "text_delta", Text: "second"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &second})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var completed *ResponsesStreamEvent
	var partAdded []ResponsesStreamEvent
	for i := range events {
		switch events[i].Type {
		case "response.content_part.added":
			partAdded = append(partAdded, events[i])
		case "response.completed":
			completed = &events[i]
		}
	}
	require.Len(t, partAdded, 2)
	require.Equal(t, 0, partAdded[0].ContentIndex)
	require.Equal(t, 1, partAdded[1].ContentIndex)
	require.NotNil(t, completed)
	require.Len(t, completed.Response.Output[0].Content, 2)
	require.Equal(t, "first", completed.Response.Output[0].Content[0].Text)
	require.Equal(t, "second", completed.Response.Output[0].Content[1].Text)
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
	var summaryDone *ResponsesStreamEvent
	for i := range events {
		switch events[i].Type {
		case "response.reasoning_summary_text.done":
			summaryDone = &events[i]
		case "response.completed":
			completed = &events[i]
		}
	}
	require.NotNil(t, summaryDone)
	require.Equal(t, "thinking", summaryDone.Text)
	require.NotNil(t, completed)
	require.Len(t, completed.Response.Output, 1)
	require.Equal(t, "reasoning", completed.Response.Output[0].Type)
	require.Len(t, completed.Response.Output[0].Summary, 1)
	require.Equal(t, "thinking", completed.Response.Output[0].Summary[0].Text)
}
