package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// namespace 内的 custom 子工具（Codex functions/exec）要摊平成 function，
// 历史里带 namespace 的 custom_tool_call 要用同一个摊平名，回程按命名空间归属还原。
func TestNamespaceCustomToolFlattenHistoryAndRestore(t *testing.T) {
	tools := []ResponsesTool{{Type: "namespace", Name: "functions", Tools: []ResponsesTool{
		{Type: "custom", Name: "exec", Description: "run"},
		{Type: "function", Name: "wait", Parameters: json.RawMessage(`{"type":"object"}`)},
	}}}
	chatTools, err := responsesToolsToChatTools(tools)
	require.NoError(t, err)
	require.Len(t, chatTools, 2)
	require.Equal(t, "functions__exec", chatTools[0].Function.Name)
	require.JSONEq(t, customToolInputSchema, string(chatTools[0].Function.Parameters))

	ns := NamespaceToolNames(tools)
	require.Equal(t, NamespacedToolName{Namespace: "functions", Name: "exec", Custom: true}, ns["functions__exec"])
	require.False(t, ns["functions__wait"].Custom)

	// exec 也在顶层声明为 custom 时，functions__exec 仍按命名空间归属还原。
	msg := ChatMessage{Role: "assistant", ToolCalls: []ChatToolCall{{ID: "c1", Type: "function", Function: ChatFunctionCall{Name: "functions__exec", Arguments: `{"input":"ls"}`}}}}
	out := chatMessageToResponsesOutput(msg, map[string]bool{"exec": true}, nil, false, ns)
	require.Len(t, out, 1)
	require.Equal(t, "custom_tool_call", out[0].Type)
	require.Equal(t, "exec", out[0].Name)
	require.Equal(t, "functions", out[0].Namespace)
	require.Equal(t, "ls", out[0].Input)

	// 只有顶层 custom exec 时保留旧别名行为：无 namespace。
	nsFn := NamespaceToolNames([]ResponsesTool{{Type: "namespace", Name: "functions", Tools: []ResponsesTool{{Type: "function", Name: "wait"}}}})
	out = chatMessageToResponsesOutput(msg, map[string]bool{"exec": true}, nil, false, nsFn)
	require.Equal(t, "exec", out[0].Name)
	require.Empty(t, out[0].Namespace)
}

func TestNamespaceCustomToolHistoryUsesFlatName(t *testing.T) {
	req := &ResponsesRequest{
		Model: "m",
		Input: json.RawMessage(`[{"type":"custom_tool_call","call_id":"c1","namespace":"functions","name":"exec","input":"ls"},{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]`),
	}
	chatReq, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Equal(t, "functions__exec", chatReq.Messages[0].ToolCalls[0].Function.Name)
	require.JSONEq(t, `{"input":"ls"}`, chatReq.Messages[0].ToolCalls[0].Function.Arguments)
}
