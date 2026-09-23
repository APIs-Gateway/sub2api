package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// agent_message 与 input_text / input_image 一样属于 user 侧条目，会开启新轮次：
// 之后的工具调用不得沿用上一轮的 reasoning。
func TestResponsesToChat_AgentMessageEndsTurnReasoning(t *testing.T) {
	req := &ResponsesRequest{
		Model: "deepseek-reasoner",
		Input: json.RawMessage(`[
			{"type":"reasoning","id":"item_r1","summary":[{"type":"summary_text","text":"turn one"}]},
			{"type":"function_call","call_id":"call_a","name":"exec_command","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":"ok"},
			{"type":"agent_message","author":"/root","recipient":"/root/alpha","content":[{"type":"input_text","text":"new task"}]},
			{"type":"function_call","call_id":"call_b","name":"exec_command","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_b","output":"ok"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)

	byCallID := map[string]ChatMessage{}
	for _, m := range out.Messages {
		for _, tc := range m.ToolCalls {
			byCallID[tc.ID] = m
		}
	}
	require.Len(t, byCallID, 2)
	require.Equal(t, "turn one", byCallID["call_a"].ReasoningContent)
	require.Empty(t, byCallID["call_b"].ReasoningContent, "agent_message 之后不得回放上一轮 reasoning")
}

// reasoning item 没有 id 时无从回查缓存，lookup 不应被调用。
func TestResponsesToChat_ReasoningCacheLookup_SkipsItemWithoutID(t *testing.T) {
	req := &ResponsesRequest{
		Model: "deepseek-reasoner",
		Input: json.RawMessage(`[
			{"type":"reasoning","summary":[],"encrypted_content":"opaque"},
			{"type":"function_call","call_id":"call_1","name":"get_value","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		]`),
	}

	called := false
	out, err := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{
		ReasoningContentByID: func(string) string {
			called = true
			return "cached"
		},
	})
	require.NoError(t, err)
	require.False(t, called)
	require.Empty(t, out.Messages[0].ReasoningContent)
}

func TestExtractResponsesReasoningItem_EmptyAndNull(t *testing.T) {
	for _, raw := range []string{"", "  ", "null", " null "} {
		_, _, ok := ExtractResponsesReasoningItem(json.RawMessage(raw))
		require.False(t, ok, "raw=%q", raw)
	}
}
