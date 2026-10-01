package apicompat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesAnthropicParallelToolsStaySerial(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	var wire []AnthropicStreamEvent
	feed := func(event ResponsesStreamEvent) {
		wire = append(wire, ResponsesEventToAnthropicEvents(&event, state)...)
	}
	feed(ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "r", Model: "m"}})
	feed(ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", ID: "a", CallID: "call_a", Name: "first"}})
	feed(ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 1, Item: &ResponsesOutput{Type: "function_call", ID: "b", CallID: "call_b", Name: "second"}})
	feed(ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 1, ItemID: "b", Delta: `{"b":`})
	feed(ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 0, ItemID: "a", Delta: `{"a":1}`})
	feed(ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 1, ItemID: "b", Arguments: `{"b":2}`})
	feed(ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 0, ItemID: "a", Arguments: `{"a":1}`})
	feed(ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed", Usage: &ResponsesUsage{InputTokens: 7, OutputTokens: 3}}})

	require.Equal(t, []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}, responsesAnthropicEventTypes(wire))
	open := -1
	var args [2]string
	for _, event := range wire {
		switch event.Type {
		case "content_block_start":
			require.Equal(t, -1, open, "content blocks must never overlap")
			open = *event.Index
		case "content_block_delta":
			require.Equal(t, open, *event.Index)
			args[open] += event.Delta.PartialJSON
		case "content_block_stop":
			require.Equal(t, open, *event.Index)
			open = -1
		case "message_delta":
			require.Equal(t, -1, open)
			require.Equal(t, 7, event.Usage.InputTokens)
			require.Equal(t, 3, event.Usage.OutputTokens)
		case "message_stop":
			require.Equal(t, -1, open)
		}
	}
	require.JSONEq(t, `{"a":1}`, args[0])
	require.JSONEq(t, `{"b":2}`, args[1])
}

func TestResponsesAnthropicParallelToolBufferLimitIsStreamError(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "r"}}, state)
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", Name: "first"}}, state)
	require.Empty(t, ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 1, Item: &ResponsesOutput{Type: "function_call", Name: "second"}}, state))
	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 1, Delta: strings.Repeat("x", 1<<20)}, state)
	require.Len(t, events, 1)
	require.Equal(t, "error", events[0].Type)
	require.Equal(t, "api_error", events[0].Error.Type)
	require.Empty(t, ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}}, state))
	require.Empty(t, FinalizeResponsesAnthropicStream(state), "an errored stream must not emit message_stop")
}

func TestResponsesAnthropicQueuedCustomToolPreservesUTF8(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	var wire []AnthropicStreamEvent
	feed := func(event ResponsesStreamEvent) {
		wire = append(wire, ResponsesEventToAnthropicEvents(&event, state)...)
	}
	feed(ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "r"}})
	feed(ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", Name: "first", CallID: "call_first"}})
	feed(ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 1, Item: &ResponsesOutput{Type: "custom_tool_call", Name: "exec", CallID: "call_exec"}})
	feed(ResponsesStreamEvent{Type: "response.custom_tool_call_input.delta", OutputIndex: 1, Delta: "quote\"<" + string([]byte{0xe2})})
	feed(ResponsesStreamEvent{Type: "response.custom_tool_call_input.delta", OutputIndex: 1, Delta: string([]byte{0x82, 0xac}) + "\\tail"})
	feed(ResponsesStreamEvent{Type: "response.custom_tool_call_input.done", OutputIndex: 1, Input: "quote\"<€\\tail"})
	feed(ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 0, Arguments: `{}`})
	feed(ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}})

	open := -1
	var customJSON string
	for _, event := range wire {
		switch event.Type {
		case "content_block_start":
			require.Equal(t, -1, open)
			open = *event.Index
		case "content_block_delta":
			require.Equal(t, open, *event.Index)
			if open == 1 {
				customJSON += event.Delta.PartialJSON
			}
		case "content_block_stop":
			require.Equal(t, open, *event.Index)
			open = -1
		}
	}
	require.Equal(t, -1, open)
	require.JSONEq(t, `{"input":"quote\"<€\\tail"}`, customJSON)
}

func TestResponsesAnthropicBufferCountsNestedBlockPayloads(t *testing.T) {
	large := strings.Repeat("x", 1<<20)
	for _, tc := range []struct {
		name  string
		block AnthropicContentBlock
	}{
		{name: "image source", block: AnthropicContentBlock{Type: "image", Source: &AnthropicImageSource{Type: "base64", Data: large}}},
		{name: "thinking signature", block: AnthropicContentBlock{Type: "thinking", Signature: large}},
		{name: "redacted thinking data", block: AnthropicContentBlock{Type: "redacted_thinking", Data: large}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewResponsesEventToAnthropicState()
			state.ContentBlockIndex = 2
			first, second := 0, 1
			require.Len(t, state.serializeResponsesAnthropicEvents([]AnthropicStreamEvent{{Type: "content_block_start", Index: &first}}), 1)
			events := state.serializeResponsesAnthropicEvents([]AnthropicStreamEvent{{Type: "content_block_start", Index: &second, ContentBlock: &tc.block}})
			require.Len(t, events, 1)
			require.Equal(t, "error", events[0].Type)
		})
	}
}

func TestResponsesAnthropicReadToolArgumentsAreBounded(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "r"}}, state)
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", Name: "Read"}}, state)
	require.Empty(t, ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 0, Delta: strings.Repeat("x", 1<<20)}, state))
	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 0, Delta: "x"}, state)
	require.Len(t, events, 1)
	require.Equal(t, "error", events[0].Type)
	require.Empty(t, ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}}, state))
	require.Empty(t, FinalizeResponsesAnthropicStream(state))
}

func responsesAnthropicEventTypes(events []AnthropicStreamEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}
