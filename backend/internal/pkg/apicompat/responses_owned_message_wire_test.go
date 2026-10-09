package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Only pre-existing exported constructors are used: this exact public API/wire
// fixture can be installed on OLD without referencing new private helpers.
func ownedMessageWireJSON(t *testing.T, value any) gjson.Result {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return gjson.ParseBytes(data)
}

func requireOwnedTextShape(t *testing.T, item gjson.Result, text string) {
	t.Helper()
	require.NotEmpty(t, item.Get("id").String(), item.Raw)
	require.Equal(t, "message", item.Get("type").String())
	require.Equal(t, text, item.Get("content.0.text").String())
	require.True(t, item.Get("content.0.annotations").IsArray(), item.Raw)
	require.True(t, item.Get("content.0.logprobs").IsArray(), item.Raw)
}

func TestOwnedResponsesMessage_PublicConverters(t *testing.T) {
	t.Run("chat_unary_and_existing_empty_fallback", func(t *testing.T) {
		for _, text := range []string{"hello", ""} {
			resp := &ChatCompletionsResponse{ID: "chat_source", Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: json.RawMessage(`"` + text + `"`)}}}}
			out := ChatCompletionsResponseToResponses(resp, "gpt-5.4", nil, nil, false, nil)
			wire := ownedMessageWireJSON(t, out)
			requireOwnedTextShape(t, wire.Get("output.0"), text)
			require.Equal(t, "completed", wire.Get("output.0.status").String())
		}
	})
	t.Run("anthropic_unary_and_existing_empty_fallback", func(t *testing.T) {
		for _, text := range []string{"hello", ""} {
			out := AnthropicToResponsesResponse(&AnthropicResponse{ID: "msg_source", Model: "claude-sonnet-4-5", Content: []AnthropicContentBlock{{Type: "text", Text: text}}})
			wire := ownedMessageWireJSON(t, out)
			requireOwnedTextShape(t, wire.Get("output.0"), text)
			require.Equal(t, "completed", wire.Get("output.0.status").String())
		}
	})
	t.Run("anthropic_added_done_terminal_identity_and_arrays", func(t *testing.T) {
		state := NewAnthropicEventToResponsesState()
		state.Model = "claude-sonnet-4-5"
		index := 0
		input := []*AnthropicStreamEvent{
			{Type: "message_start", Message: &AnthropicResponse{ID: "msg_source", Model: state.Model}},
			{Type: "content_block_start", Index: &index, ContentBlock: &AnthropicContentBlock{Type: "text"}},
			{Type: "content_block_delta", Index: &index, Delta: &AnthropicDelta{Type: "text_delta", Text: "hello"}},
			{Type: "content_block_stop", Index: &index},
			{Type: "message_stop"},
		}
		id := ""
		seenDone, seenTerminal := false, false
		for _, event := range input {
			for _, output := range AnthropicEventToResponsesEvents(event, state) {
				wire := ownedMessageWireJSON(t, output)
				switch output.Type {
				case "response.output_item.added":
					if wire.Get("item.type").String() == "message" {
						id = wire.Get("item.id").String()
						require.NotEmpty(t, id)
						require.Equal(t, "in_progress", wire.Get("item.status").String())
					}
				case "response.output_item.done":
					seenDone = true
					require.Equal(t, id, wire.Get("item.id").String())
					requireOwnedTextShape(t, wire.Get("item"), "hello")
				case "response.completed":
					seenTerminal = true
					require.Equal(t, id, wire.Get("response.output.0.id").String())
					requireOwnedTextShape(t, wire.Get("response.output.0"), "hello")
				}
			}
		}
		require.True(t, seenDone)
		require.True(t, seenTerminal)
	})
	t.Run("chat_added_done_terminal_identity_and_arrays", func(t *testing.T) {
		state := NewChatCompletionsToResponsesStreamState("gpt-5.4")
		text, finish := "hello", "stop"
		chunks := []*ChatCompletionsChunk{
			{ID: "chat_source", Model: "gpt-5.4", Choices: []ChatChunkChoice{{Delta: ChatDelta{Content: &text}}}},
			{ID: "chat_source", Model: "gpt-5.4", Choices: []ChatChunkChoice{{FinishReason: &finish}}},
		}
		id := ""
		seenDone, seenTerminal := false, false
		var outputs []ResponsesStreamEvent
		for _, chunk := range chunks {
			outputs = append(outputs, ChatCompletionsChunkToResponsesEvents(chunk, state)...)
		}
		outputs = append(outputs, FinalizeChatCompletionsResponsesStream(state)...)
		for _, output := range outputs {
			wire := ownedMessageWireJSON(t, output)
			switch output.Type {
			case "response.output_item.added":
				if wire.Get("item.type").String() == "message" {
					id = wire.Get("item.id").String()
					require.NotEmpty(t, id)
					require.Equal(t, "in_progress", wire.Get("item.status").String())
				}
			case "response.output_item.done":
				seenDone = true
				require.Equal(t, id, wire.Get("item.id").String())
				requireOwnedTextShape(t, wire.Get("item"), "hello")
			case "response.completed":
				seenTerminal = true
				require.Equal(t, id, wire.Get("response.output.0.id").String())
				requireOwnedTextShape(t, wire.Get("response.output.0"), "hello")
			}
		}
		require.Empty(t, FinalizeChatCompletionsResponsesStream(state), "existing finalizer is idempotent")
		require.True(t, seenDone)
		require.True(t, seenTerminal)
	})
	t.Run("refusal_content_part_is_not_output_text", func(t *testing.T) {
		var event ResponsesStreamEvent
		require.NoError(t, json.Unmarshal([]byte(`{"type":"response.content_part.done","output_index":0,"content_index":1,"item_id":"msg_refusal","part":{"type":"refusal","refusal":"declined","vendor":{"n":9223372036854775807}}}`), &event))
		wire := ownedMessageWireJSON(t, event)
		require.Equal(t, "refusal", wire.Get("part.type").String())
		require.Equal(t, "declined", wire.Get("part.refusal").String())
		require.Equal(t, "9223372036854775807", wire.Get("part.vendor.n").Raw)
		require.False(t, wire.Get("part.annotations").Exists())
		require.False(t, wire.Get("part.text").Exists())
	})
	t.Run("provided_metadata_is_not_reset", func(t *testing.T) {
		var event ResponsesStreamEvent
		require.NoError(t, json.Unmarshal([]byte(`{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg_text","part":{"type":"output_text","text":"hello","annotations":[{"type":"url_citation","url":"https://example.test"}],"logprobs":[{"token":"hello","logprob":-0.1}],"prompt_cache_breakpoint":{"n":9223372036854775807}}}`), &event))
		wire := ownedMessageWireJSON(t, event)
		require.Len(t, wire.Get("part.annotations").Array(), 1)
		require.Len(t, wire.Get("part.logprobs").Array(), 1)
		require.Equal(t, "9223372036854775807", wire.Get("part.prompt_cache_breakpoint.n").Raw)
	})
	t.Run("typed_part_metadata_keeps_integer_precision", func(t *testing.T) {
		event := ResponsesStreamEvent{
			Type: "response.content_part.done", ItemID: "msg_typed",
			Part: &ResponsesContentPart{Type: "output_text", Text: "hello", PromptCacheBreakpoint: json.RawMessage(`{"id":9223372036854775807,"nested":[9007199254740993]}`)},
		}
		wire := ownedMessageWireJSON(t, event)
		require.Equal(t, "9223372036854775807", wire.Get("part.prompt_cache_breakpoint.id").Raw)
		require.Equal(t, "9007199254740993", wire.Get("part.prompt_cache_breakpoint.nested.0").Raw)
	})
	t.Run("request_input_parts_do_not_acquire_output_arrays", func(t *testing.T) {
		part := ResponsesContentPart{Type: "input_file", Filename: "sample.txt", FileID: "file_1"}
		wire := ownedMessageWireJSON(t, part)
		require.Equal(t, "input_file", wire.Get("type").String())
		require.Equal(t, "file_1", wire.Get("file_id").String())
		require.False(t, wire.Get("annotations").Exists())
		require.False(t, wire.Get("logprobs").Exists())
	})
	t.Run("zero_content_accumulator_creates_no_message", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.added", Item: &ResponsesOutput{Type: "message", ID: "msg_empty", Status: "in_progress"}})
		acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_text.delta", Delta: ""})
		require.False(t, acc.HasContent())
		require.Empty(t, acc.BuildOutput())
	})
}

func TestOwnedResponsesMessage_PublicAccumulator(t *testing.T) {
	feed := func(t *testing.T, acc *BufferedResponseAccumulator, frames ...string) {
		t.Helper()
		for _, raw := range frames {
			var event ResponsesStreamEvent
			require.NoError(t, json.Unmarshal([]byte(raw), &event))
			acc.ProcessEvent(&event)
		}
	}
	t.Run("matching_first_empty_message_recovers_own_fields", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		feed(t, acc, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_stream","delta":"hello"}`)
		resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "message", ID: "msg_stream", Role: "assistant"}}}
		acc.SupplementResponseOutput(resp)
		wire := ownedMessageWireJSON(t, resp)
		requireOwnedTextShape(t, wire.Get("output.0"), "hello")
		require.Equal(t, "msg_stream", wire.Get("output.0.id").String())
		require.Equal(t, "completed", wire.Get("output.0.status").String())
	})
	t.Run("second_message_and_existing_terminal_identity_are_separate", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		feed(t, acc, `{"type":"response.output_text.delta","output_index":1,"content_index":0,"item_id":"msg_second","delta":"B"}`)
		resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{
			{Type: "message", ID: "msg_first", Status: "completed", Role: "assistant", Content: []ResponsesContentPart{{Type: "output_text", Text: "A"}}},
			{Type: "message", ID: "msg_second", Status: "completed", Role: "assistant"},
		}}
		acc.SupplementResponseOutput(resp)
		wire := ownedMessageWireJSON(t, resp)
		require.Len(t, wire.Get("output").Array(), 2)
		require.Equal(t, "msg_first", wire.Get("output.0.id").String())
		require.Equal(t, "A", wire.Get("output.0.content.0.text").String())
		requireOwnedTextShape(t, wire.Get("output.1"), "B")
	})
	t.Run("refusal_insertion_has_stable_id_status_without_text_pollution", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		feed(t, acc, `{"type":"response.refusal.delta","output_index":0,"content_index":0,"item_id":"msg_refusal","delta":"declined"}`)
		resp := &ResponsesResponse{Status: "incomplete", Output: []ResponsesOutput{{Type: "function_call", CallID: "call_1", Name: "exec", Arguments: "{}"}}}
		acc.SupplementResponseOutput(resp)
		first := ownedMessageWireJSON(t, resp).Get("output.0")
		require.Equal(t, "msg_refusal", first.Get("id").String())
		require.Equal(t, "incomplete", first.Get("status").String())
		require.Equal(t, "refusal", first.Get("content.0.type").String())
		require.Equal(t, "declined", first.Get("content.0.refusal").String())
		require.False(t, first.Get("content.0.annotations").Exists())
		require.False(t, first.Get("content.0.text").Exists())
		acc.SupplementResponseOutput(resp)
		require.Len(t, resp.Output, 2)
		require.Equal(t, "msg_refusal", resp.Output[0].ID)
	})
	t.Run("missing_insertion_does_not_shift_later_terminal_index_match", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		feed(t, acc,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_missing","delta":"A"}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"B"}`,
		)
		resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{
			{Type: "function_call", CallID: "call_1", Name: "exec", Arguments: "{}"},
			{Type: "message", ID: "msg_terminal", Status: "completed", Role: "assistant"},
		}}
		acc.SupplementResponseOutput(resp)
		wire := ownedMessageWireJSON(t, resp)
		require.Len(t, wire.Get("output").Array(), 3)
		requireOwnedTextShape(t, wire.Get("output.0"), "A")
		require.Equal(t, "msg_missing", wire.Get("output.0.id").String())
		require.Equal(t, "function_call", wire.Get("output.1.type").String())
		requireOwnedTextShape(t, wire.Get("output.2"), "B")
		require.Equal(t, "msg_terminal", wire.Get("output.2.id").String())
	})
	t.Run("arguments_match_before_owned_message_insertion", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		feed(t, acc,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"exec"}}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"command\":\"echo hi\"}"}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"item_id":"msg_text","delta":"hello"}`,
		)
		resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "function_call", CallID: "call_1", Name: "exec"}}}
		acc.SupplementResponseOutput(resp)
		require.Len(t, resp.Output, 2)
		require.Equal(t, "msg_text", resp.Output[0].ID)
		require.Equal(t, "function_call", resp.Output[1].Type)
		require.Equal(t, "call_1", resp.Output[1].CallID)
		require.Equal(t, `{"command":"echo hi"}`, resp.Output[1].Arguments)
	})
}
