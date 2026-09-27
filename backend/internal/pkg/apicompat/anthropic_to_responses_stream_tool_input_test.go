package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectToolCallStreamEvents drives a tool_use block through the Anthropic →
// Responses stream converter and returns every emitted event in order.
func collectToolCallStreamEvents(t *testing.T, blockInput json.RawMessage, partials []string) []ResponsesStreamEvent {
	t.Helper()

	state := NewAnthropicEventToResponsesState()
	var all []ResponsesStreamEvent

	all = append(all, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:    "msg_tool_input",
			Model: "gemini-3.7-flash",
			Usage: AnthropicUsage{InputTokens: 12},
		},
	}, state)...)

	all = append(all, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "content_block_start",
		ContentBlock: &AnthropicContentBlock{
			Type:  "tool_use",
			ID:    "toolu_01abc",
			Name:  "eval",
			Input: blockInput,
		},
	}, state)...)

	for _, partial := range partials {
		all = append(all, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
			Type:  "content_block_delta",
			Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: partial},
		}, state)...)
	}

	all = append(all, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "content_block_stop"}, state)...)
	all = append(all, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type:  "message_delta",
		Usage: &AnthropicUsage{OutputTokens: 9},
	}, state)...)
	all = append(all, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)...)

	return all
}

func concatArgumentDeltas(events []ResponsesStreamEvent) string {
	out := ""
	for _, e := range events {
		if e.Type == "response.function_call_arguments.delta" {
			out += e.Delta
		}
	}
	return out
}

func findFunctionCallOutput(events []ResponsesStreamEvent) *ResponsesOutput {
	for i := range events {
		if events[i].Type != "response.output_item.done" || events[i].Item == nil {
			continue
		}
		if events[i].Item.Type == "function_call" {
			return events[i].Item
		}
	}
	return nil
}

func toolArgumentLifecycle(events []ResponsesStreamEvent, name string) []string {
	var lifecycle []string
	for _, event := range events {
		switch event.Type {
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			if event.Name == name {
				lifecycle = append(lifecycle, event.Type)
			}
		case "response.output_item.done":
			if event.Item != nil && event.Item.Type == "function_call" && event.Item.Name == name {
				lifecycle = append(lifecycle, event.Type)
			}
		}
	}
	return lifecycle
}

// Upstreams that are not the canonical Anthropic API (here: an Anthropic-compatible
// relay fronting Gemini) put the complete tool arguments on content_block_start and
// never emit an input_json_delta. The converter must not drop them: this repository
// already reads ContentBlock.Input on the non-streaming path
// (anthropicResponseToResponsesOutputs) and on the ChatCompletions bridge, so the
// streaming path has to agree. Dropping it makes every client-side tool call arrive
// with `{}` and the agent loop cannot proceed.
func TestAnthropicEventToResponses_ToolInputOnContentBlockStart(t *testing.T) {
	const args = `{"language":"py","code":"print(1)"}`

	events := collectToolCallStreamEvents(t, json.RawMessage(args), nil)

	item := findFunctionCallOutput(events)
	require.NotNil(t, item, "a function_call output item must be emitted")
	assert.Equal(t, args, item.Arguments,
		"arguments carried on content_block_start must survive to output_item.done")
	assert.Equal(t, "eval", item.Name)

	// Clients that accumulate from the event stream (rather than reading the
	// terminal item) must see the arguments too.
	assert.Equal(t, args, concatArgumentDeltas(events),
		"argument deltas must reconstruct the full arguments JSON")

	done := firstEventOfType(events, "response.function_call_arguments.done")
	require.NotNil(t, done, "function_call_arguments.done must be emitted")
	assert.Equal(t, args, done.Arguments,
		"function_call_arguments.done must carry the complete arguments")

	completed := firstEventOfType(events, "response.completed")
	require.NotNil(t, completed)
	require.NotNil(t, completed.Response)
	require.Len(t, completed.Response.Output, 1)
	assert.Equal(t, args, completed.Response.Output[0].Arguments,
		"response.completed must carry the same arguments")
	assert.Equal(t, []string{
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
	}, toolArgumentLifecycle(events, "eval"))
}

// The canonical Anthropic shape (empty input on content_block_start, arguments
// streamed as input_json_delta) must keep its existing event sequence exactly.
func TestAnthropicEventToResponses_ToolInputFromDeltasUnchanged(t *testing.T) {
	const args = `{"language":"py","code":"print(1)"}`

	events := collectToolCallStreamEvents(t, json.RawMessage(`{}`),
		[]string{`{"language":"py",`, `"code":"print(1)"}`})

	item := findFunctionCallOutput(events)
	require.NotNil(t, item)
	assert.Equal(t, args, item.Arguments)

	// Exactly the two upstream deltas, not duplicated by the seed.
	var deltas []string
	for _, e := range events {
		if e.Type == "response.function_call_arguments.delta" {
			deltas = append(deltas, e.Delta)
		}
	}
	assert.Equal(t, []string{`{"language":"py",`, `"code":"print(1)"}`}, deltas,
		"canonical delta streaming must not gain or lose events")
}

// An upstream that sends both a populated content_block_start and real deltas
// must not have the two concatenated into malformed JSON.
func TestAnthropicEventToResponses_ToolInputSeedNotDuplicatedByDeltas(t *testing.T) {
	const args = `{"language":"py","code":"print(1)"}`

	events := collectToolCallStreamEvents(t, json.RawMessage(`{"ignored":true}`),
		[]string{`{"language":"py",`, `"code":"print(1)"}`})

	item := findFunctionCallOutput(events)
	require.NotNil(t, item)
	assert.Equal(t, args, item.Arguments, "deltas win over the content_block_start seed")
	assert.True(t, json.Valid([]byte(item.Arguments)), "arguments must stay valid JSON")
	assert.Equal(t, args, concatArgumentDeltas(events), "seed must not be replayed alongside deltas")
}

// An empty or absent input on content_block_start with no deltas keeps the
// existing "{}" fallback rather than emitting a spurious delta.
func TestAnthropicEventToResponses_ToolInputEmptyStaysEmptyObject(t *testing.T) {
	for name, input := range map[string]json.RawMessage{
		"absent": nil,
		"empty":  json.RawMessage(`{}`),
		"null":   json.RawMessage(`null`),
	} {
		t.Run(name, func(t *testing.T) {
			events := collectToolCallStreamEvents(t, input, nil)

			item := findFunctionCallOutput(events)
			require.NotNil(t, item)
			assert.Equal(t, "{}", item.Arguments)
			assert.Empty(t, concatArgumentDeltas(events),
				"no argument delta should be synthesized when there are no arguments")
		})
	}
}

// An incomplete relay stream can close without content_block_stop. The
// response item still needs the complete inline arguments already received.
func TestAnthropicEventToResponses_InlineToolInputSurvivesIncompleteClose(t *testing.T) {
	const args = `{"language":"py","code":"print(1)"}`
	for name, end := range map[string]func(*AnthropicEventToResponsesState) []ResponsesStreamEvent{
		"message_stop": func(state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
			return AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)
		},
		"transport_finalization": FinalizeAnthropicResponsesStream,
	} {
		t.Run(name, func(t *testing.T) {
			state := NewAnthropicEventToResponsesState()
			var events []ResponsesStreamEvent
			events = append(events, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
				Type: "message_start",
				Message: &AnthropicResponse{
					ID: "msg_incomplete_tool",
				},
			}, state)...)
			events = append(events, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
				Type: "content_block_start",
				ContentBlock: &AnthropicContentBlock{
					Type: "tool_use", ID: "toolu_incomplete", Name: "eval", Input: json.RawMessage(args),
				},
			}, state)...)
			events = append(events, end(state)...)

			item := findFunctionCallOutput(events)
			require.NotNil(t, item)
			assert.Equal(t, args, item.Arguments)
			assert.Equal(t, args, concatArgumentDeltas(events))

			completed := firstEventOfType(events, "response.completed")
			require.NotNil(t, completed)
			require.NotNil(t, completed.Response)
			require.Len(t, completed.Response.Output, 1)
			assert.Equal(t, args, completed.Response.Output[0].Arguments)
			assert.Equal(t, []string{
				"response.function_call_arguments.delta",
				"response.function_call_arguments.done",
				"response.output_item.done",
			}, toolArgumentLifecycle(events, "eval"))
		})
	}
}

// Starting another tool without a stop for the first implicitly closes the
// first item. Its inline arguments still need one complete, ordered lifecycle.
func TestAnthropicEventToResponses_InlineToolInputSurvivesNextToolStart(t *testing.T) {
	const args = `{"query":"weather"}`
	state := NewAnthropicEventToResponsesState()
	var events []ResponsesStreamEvent
	events = append(events, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type:    "message_start",
		Message: &AnthropicResponse{ID: "msg_two_tools"},
	}, state)...)
	events = append(events, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "content_block_start",
		ContentBlock: &AnthropicContentBlock{
			Type: "tool_use", ID: "toolu_first", Name: "first", Input: json.RawMessage(args),
		},
	}, state)...)
	events = append(events, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "content_block_start",
		ContentBlock: &AnthropicContentBlock{
			Type: "tool_use", ID: "toolu_second", Name: "second", Input: json.RawMessage(`{}`),
		},
	}, state)...)
	events = append(events, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "content_block_stop"}, state)...)
	events = append(events, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)...)

	assert.Equal(t, []string{
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
	}, toolArgumentLifecycle(events, "first"))
	completed := firstEventOfType(events, "response.completed")
	require.NotNil(t, completed)
	require.NotNil(t, completed.Response)
	require.Len(t, completed.Response.Output, 2)
	assert.Equal(t, args, completed.Response.Output[0].Arguments)
}
