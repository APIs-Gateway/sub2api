package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

// Fixed per-request billing needs actual text, reasoning, or tool content;
// role, usage, and empty tool metadata can be released without an answer.
func TestOpenAIChatSilentRefusalDetector_SemanticOutput(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"empty legacy function call", `{"choices":[{"delta":{"function_call":{"name":"","arguments":""}}}]}`, false},
		{"named legacy function call", `{"choices":[{"delta":{"function_call":{"name":"search","arguments":""}}}]}`, true},
		{"empty tool call", `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"","arguments":""}}]}}]}`, false},
		{"tool call arguments", `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"q\":\"x\"}"}}]}}]}`, true},
		{"empty response tool item", `{"type":"response.output_item.added","item":{"type":"function_call","name":"","arguments":""}}`, false},
		{"named response tool item", `{"type":"response.output_item.added","item":{"type":"function_call","name":"search"}}`, true},
		{"empty response tool arguments delta", `{"type":"response.function_call_arguments.delta","delta":""}`, false},
		{"response tool arguments delta", `{"type":"response.function_call_arguments.delta","delta":"{\"q\":\"x\"}"}`, true},
		{"empty reasoning", `{"choices":[{"delta":{"reasoning_content":""}}]}`, false},
		{"reasoning content", `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newOpenAIChatSilentRefusalDetector(0)
			d.ObservePayload([]byte(tc.payload))
			require.Equal(t, tc.want, d.HasSemanticOutput())
		})
	}
}

func TestOpenAIChatChunkHasSemanticOutput(t *testing.T) {
	reasoning := "thinking"
	empty := ""
	for _, tc := range []struct {
		name  string
		delta apicompat.ChatDelta
		want  bool
	}{
		{"empty reasoning", apicompat.ChatDelta{Reasoning: &empty}, false},
		{"nonempty reasoning", apicompat.ChatDelta{Reasoning: &reasoning}, true},
		{"empty tool", apicompat.ChatDelta{ToolCalls: []apicompat.ChatToolCall{{Function: apicompat.ChatFunctionCall{}}}}, false},
		{"named tool", apicompat.ChatDelta{ToolCalls: []apicompat.ChatToolCall{{Function: apicompat.ChatFunctionCall{Name: "search"}}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunk := apicompat.ChatCompletionsChunk{Choices: []apicompat.ChatChunkChoice{{Delta: tc.delta}}}
			require.Equal(t, tc.want, openAIChatChunkHasSemanticOutput(chunk))
		})
	}
}
