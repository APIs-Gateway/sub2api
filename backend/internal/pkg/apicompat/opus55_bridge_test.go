package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Claude Opus 5.5 桥接同步自上游 Wei-Shaw/sub2api#7509。

func TestOpus55ResponsesAdaptiveThinkingAndToolChoice(t *testing.T) {
	for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		req := &ResponsesRequest{Model: "claude-opus-5-5", Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: effort}}
		out, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err)
		require.Equal(t, "adaptive", out.Thinking.Type)
		require.Zero(t, out.Thinking.BudgetTokens)
		if effort == "" {
			effort = "medium"
		}
		require.Equal(t, effort, out.OutputConfig.Effort)
	}
	for _, choice := range []string{`"required"`, `{"type":"function","name":"lookup"}`} {
		_, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-opus-5-5", Input: json.RawMessage(`"hello"`), ToolChoice: json.RawMessage(choice)})
		require.ErrorContains(t, err, "forced tool_choice")
	}
	_, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-opus-5-5", Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: "none"}})
	require.ErrorContains(t, err, "reasoning effort")

	// 旧 Opus 保持原有映射：xhigh → max + enabled 预算思考。
	old, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-opus-5", Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: "xhigh"}})
	require.NoError(t, err)
	require.Equal(t, "max", old.OutputConfig.Effort)
	require.Equal(t, "enabled", old.Thinking.Type)
}

func TestOpus55SignedThinkingResponsesRoundTrip(t *testing.T) {
	block := AnthropicContentBlock{Type: "thinking", Thinking: "", Signature: "upstream-signed-block"}
	response := AnthropicToResponsesResponse(&AnthropicResponse{Model: "claude-opus-5-5", Content: []AnthropicContentBlock{block, {Type: "tool_use", ID: "toolu_1", Name: "lookup", Input: json.RawMessage(`{}`)}}})
	require.Len(t, response.Output, 2)
	require.NotEmpty(t, response.Output[0].EncryptedContent)
	raw, err := json.Marshal(response.Output)
	require.NoError(t, err)
	var items []ResponsesInputItem
	require.NoError(t, json.Unmarshal(raw, &items))
	items = append(items, ResponsesInputItem{Type: "function_call_output", CallID: response.Output[1].CallID, Output: "ok"})
	raw, err = json.Marshal(items)
	require.NoError(t, err)
	converted, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-opus-5-5", Input: raw})
	require.NoError(t, err)
	require.Len(t, converted.Messages, 2)
	var blocks []AnthropicContentBlock
	require.NoError(t, json.Unmarshal(converted.Messages[0].Content, &blocks))
	require.Equal(t, block, blocks[0])
	require.Equal(t, "tool_use", blocks[1].Type)

	// Arbitrary OpenAI ciphertext must never be treated as an Anthropic signature.
	_, _, err = convertResponsesInputToAnthropic("", json.RawMessage(`[{"type":"reasoning","encrypted_content":"anthropic-thinking-v1:!"}]`), true)
	require.Error(t, err)
	// 非 Opus 5.5 不解码信封，保持原来丢弃 reasoning 的行为。
	_, msgs, err := convertResponsesInputToAnthropic("", json.RawMessage(`[{"type":"reasoning","encrypted_content":"anthropic-thinking-v1:!"},{"role":"user","content":"hi"}]`), false)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	// 其它模型的非流式转换不带信封。
	plain := AnthropicToResponsesResponse(&AnthropicResponse{Model: "claude-opus-5", Content: []AnthropicContentBlock{{Type: "thinking", Thinking: "t", Signature: "s"}}})
	require.Empty(t, plain.Output[0].EncryptedContent)
}

func TestOpus55StreamingPreservesSignatureAndClosesOpenMessage(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	var events []ResponsesStreamEvent
	feed := func(raw string) {
		var evt AnthropicStreamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &evt))
		events = append(events, AnthropicEventToResponsesEvents(&evt, state)...)
	}
	feed(`{"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":10}}}`)
	feed(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	feed(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
	feed(`{"type":"content_block_stop","index":0}`)
	feed(`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`)
	feed(`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"signed"}}`)
	feed(`{"type":"content_block_stop","index":1}`)
	require.Len(t, state.Outputs, 2)
	require.Equal(t, "message", state.Outputs[0].Type)
	require.Equal(t, "reasoning", state.Outputs[1].Type)
	require.Contains(t, state.Outputs[1].EncryptedContent, anthropicThinkingEnvelopePrefix)
}

func TestChatPromptCacheOptionsAndBreakpointsPassThrough(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna", "gpt-5.4"} {
		out, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
			Model:              model,
			PromptCacheOptions: json.RawMessage(`{"mode":"explicit","ttl":"30m"}`),
			Messages:           []ChatMessage{{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}}]`)}},
		})
		require.NoError(t, err)
		require.JSONEq(t, `{"mode":"explicit","ttl":"30m"}`, string(out.PromptCacheOptions))
		require.Contains(t, string(out.Input), "prompt_cache_breakpoint")
	}
}
