package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func assertThinkingSignatureWire(t *testing.T, value any, want string) {
	t.Helper()
	wire, err := json.Marshal(value)
	require.NoError(t, err)
	var block map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(wire, &block))
	require.Contains(t, block, "thinking")
	require.Contains(t, block, "signature", string(wire))
	var signature string
	require.NoError(t, json.Unmarshal(block["signature"], &signature))
	require.Equal(t, want, signature)
}

func TestThinkingSignatureWireBlockAndControls(t *testing.T) {
	for _, block := range []AnthropicContentBlock{
		{Type: "thinking"}, {Type: "thinking", Thinking: "plan"},
		{Type: "thinking", Signature: "signed"}, {Type: "thinking", Thinking: "plan", Signature: "signed"},
	} {
		assertThinkingSignatureWire(t, block, block.Signature)
	}
	for _, block := range []AnthropicContentBlock{
		{Type: "text", Text: "answer"}, {Type: "tool_use", ID: "call_1", Name: "lookup", Input: json.RawMessage(`{}`)},
	} {
		wire, err := json.Marshal(block)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(wire, &fields))
		require.NotContains(t, fields, "signature")
		require.NotContains(t, fields, "thinking")
	}
}

func TestThinkingSignatureWireBufferedConversions(t *testing.T) {
	var chat ChatCompletionsResponse
	require.NoError(t, json.Unmarshal([]byte(`{"id":"chat","choices":[{"message":{"role":"assistant","reasoning_content":"plan","content":"answer"},"finish_reason":"stop"}]}`), &chat))
	converted := ChatCompletionsResponseToAnthropic(&chat, "grok-4.5")
	require.Equal(t, "thinking", converted.Content[0].Type)
	assertThinkingSignatureWire(t, converted.Content[0], "")
	for _, signature := range []string{"", "encrypted-real-signature"} {
		resp := &ResponsesResponse{ID: "resp", Status: "completed", Output: []ResponsesOutput{{Type: "reasoning", EncryptedContent: signature, Summary: []ResponsesSummary{{Type: "summary_text", Text: "plan"}}}}}
		converted := ResponsesToAnthropic(resp, "grok-4.5")
		require.NotEmpty(t, converted.Content)
		require.Equal(t, "thinking", converted.Content[0].Type)
		assertThinkingSignatureWire(t, converted.Content[0], signature)
	}
}

func TestThinkingSignatureWireStreamingConversions(t *testing.T) {
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"reasoning_content":"plan"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
	})
	thinkingStarts := 0
	for _, event := range events {
		if event.Type == "content_block_start" && event.ContentBlock.Type == "thinking" {
			assertThinkingSignatureWire(t, event.ContentBlock, "")
			thinkingStarts++
		}
	}
	require.Equal(t, 1, thinkingStarts)
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "resp", Model: "grok-4.5"}}, state)
	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", Item: &ResponsesOutput{Type: "reasoning"}}, state)
	require.Len(t, events, 1)
	assertThinkingSignatureWire(t, events[0].ContentBlock, "")
	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.done", Item: &ResponsesOutput{Type: "reasoning", EncryptedContent: "signed"}}, state)
	var signatures []string
	for _, event := range events {
		if event.Delta != nil && event.Delta.Type == "signature_delta" {
			signatures = append(signatures, event.Delta.Signature)
		}
	}
	require.Equal(t, []string{"signed"}, signatures)
}
