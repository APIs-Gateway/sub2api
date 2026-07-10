package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesToChatCompletionsRequest_ConvertsProxyToolsAndChoices(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-5.6",
		Input: json.RawMessage(`"help"`),
		Tools: []ResponsesTool{
			{Type: "function", Name: "wait", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Type: "custom", Name: "exec"},
			{Type: "tool_search"},
			{Type: "namespace", Name: "gmail", Tools: []ResponsesTool{{Type: "function", Name: "send"}}},
		},
		ToolChoice: json.RawMessage(`{"type":"tool_search"}`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Tools, 4)
	assert.Equal(t, "wait", out.Tools[0].Function.Name)
	assert.Equal(t, "exec", out.Tools[1].Function.Name)
	assert.JSONEq(t, customToolInputSchema, string(out.Tools[1].Function.Parameters))
	assert.Equal(t, toolSearchProxyName, out.Tools[2].Function.Name)
	assert.Equal(t, "gmail__send", out.Tools[3].Function.Name)
	assert.JSONEq(t, `{"type":"function","function":{"name":"tool_search"}}`, string(out.ToolChoice))
}

func TestResponsesToChatCompletionsRequest_RejectsAmbiguousToolNames(t *testing.T) {
	_, err := ResponsesToChatCompletionsRequest(&ResponsesRequest{
		Model: "gpt-5.6",
		Input: json.RawMessage(`"help"`),
		Tools: []ResponsesTool{
			{Type: "tool_search"},
			{Type: "custom", Name: toolSearchProxyName},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), toolSearchProxyName)

	_, err = ResponsesToChatCompletionsRequest(&ResponsesRequest{
		Model: "gpt-5.6",
		Input: json.RawMessage(`"help"`),
		Tools: []ResponsesTool{
			{Type: "function", Name: "gmail__send"},
			{Type: "namespace", Name: "gmail", Tools: []ResponsesTool{{Type: "function", Name: "send"}}},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gmail__send")
}

func TestResponsesToChatCompletionsRequest_DropsUndeclaredToolChoice(t *testing.T) {
	out, err := ResponsesToChatCompletionsRequest(&ResponsesRequest{
		Model:      "gpt-5.6",
		Input:      json.RawMessage(`"help"`),
		Tools:      []ResponsesTool{{Type: "function", Name: "wait"}},
		ToolChoice: json.RawMessage(`{"type":"custom","name":"exec"}`),
	})
	require.NoError(t, err)
	assert.Empty(t, out.ToolChoice)
}

func TestChatCompletionsResponseToResponsesWithToolContext_RestoresCalls(t *testing.T) {
	context := NewResponsesChatToolContext([]ResponsesTool{
		{Type: "custom", Name: "exec"},
		{Type: "tool_search"},
		{Type: "namespace", Name: "gmail", Tools: []ResponsesTool{{Type: "function", Name: "send"}}},
	})
	resp := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{ToolCalls: []ChatToolCall{
		{ID: "call_custom", Function: ChatFunctionCall{Name: "exec", Arguments: `{"input":"ls -la"}`}},
		{ID: "call_search", Function: ChatFunctionCall{Name: "tool_search", Arguments: `{"query":"gmail"}`}},
		{ID: "call_ns", Function: ChatFunctionCall{Name: "gmail__send", Arguments: `{"to":"a@example.test"}`}},
		{ID: "call_function", Function: ChatFunctionCall{Name: "wait", Arguments: `{"ms":1}`}},
	}}}}}

	out := ChatCompletionsResponseToResponsesWithToolContext(resp, "gpt-5.6", context)
	require.Len(t, out.Output, 4)
	assert.Equal(t, "custom_tool_call", out.Output[0].Type)
	assert.Equal(t, "ls -la", out.Output[0].Input)
	assert.Equal(t, "tool_search_call", out.Output[1].Type)
	assert.Equal(t, "function_call", out.Output[2].Type)
	assert.Equal(t, "gmail", out.Output[2].Namespace)
	assert.Equal(t, "send", out.Output[2].Name)
	assert.Equal(t, "function_call", out.Output[3].Type)
	assert.Equal(t, "wait", out.Output[3].Name)

	wire, err := json.Marshal(out.Output[1])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"tool_search_call","id":"`+out.Output[1].ID+`","call_id":"call_search","execution":"client","arguments":{"query":"gmail"},"status":"completed"}`, string(wire))
}

func TestChatCompletionsStreamWithToolContext_DelaysNameDependentItemAndPreservesReasoning(t *testing.T) {
	context := NewResponsesChatToolContext([]ResponsesTool{{Type: "custom", Name: "exec"}})
	state := NewChatCompletionsToResponsesStreamStateWithToolContext("gpt-5.6", context)
	index := 0
	reasoning := "checking"
	first := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ReasoningContent: &reasoning,
		ToolCalls:        []ChatToolCall{{Index: &index, ID: "call_exec", Function: ChatFunctionCall{Arguments: `{"inp`}}},
	}}}}
	second := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &index, Function: ChatFunctionCall{Name: "exec", Arguments: `ut":"pwd"}`}}},
	}}}}

	events := append(ChatCompletionsChunkToResponsesEvents(first, state), ChatCompletionsChunkToResponsesEvents(second, state)...)
	events = append(events, FinalizeChatCompletionsResponsesStream(state)...)

	var added, inputDone, completed *ResponsesStreamEvent
	for i := range events {
		event := &events[i]
		switch event.Type {
		case "response.output_item.added":
			if event.Item != nil && event.Item.CallID == "call_exec" {
				added = event
			}
		case "response.custom_tool_call_input.done":
			inputDone = event
		case "response.completed":
			completed = event
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			if event.CallID == "call_exec" {
				t.Fatalf("custom call emitted function lifecycle event %q", event.Type)
			}
		}
	}
	require.NotNil(t, added)
	assert.Equal(t, "custom_tool_call", added.Item.Type)
	require.NotNil(t, inputDone)
	assert.Equal(t, "pwd", inputDone.Input)
	require.NotNil(t, completed)
	require.Len(t, completed.Response.Output, 2)
	assert.Equal(t, "reasoning", completed.Response.Output[0].Type)
	assert.Equal(t, "custom_tool_call", completed.Response.Output[1].Type)
}

func TestChatCompletionsStreamWithToolContext_RestoresNamespace(t *testing.T) {
	context := NewResponsesChatToolContext([]ResponsesTool{{
		Type: "namespace", Name: "gmail", Tools: []ResponsesTool{{Type: "function", Name: "send"}},
	}})
	state := NewChatCompletionsToResponsesStreamStateWithToolContext("gpt-5.6", context)
	index := 0
	events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &index, ID: "call_send", Function: ChatFunctionCall{Name: "gmail__send", Arguments: `{"to":"a@example.test"}`}}},
	}}}}, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(state)...)

	var added, done *ResponsesStreamEvent
	for i := range events {
		event := &events[i]
		if event.Item == nil || event.Item.CallID != "call_send" {
			continue
		}
		if event.Type == "response.output_item.added" {
			added = event
		}
		if event.Type == "response.output_item.done" {
			done = event
		}
	}
	require.NotNil(t, added)
	require.NotNil(t, done)
	assert.Equal(t, "gmail", added.Item.Namespace)
	assert.Equal(t, "send", added.Item.Name)
	assert.Equal(t, "gmail", done.Item.Namespace)
	assert.Equal(t, "send", done.Item.Name)
}

func TestChatCompletionsStreamWithToolContext_RestoresToolSearchWireItem(t *testing.T) {
	context := NewResponsesChatToolContext([]ResponsesTool{{Type: "tool_search"}})
	state := NewChatCompletionsToResponsesStreamStateWithToolContext("gpt-5.6", context)
	index := 0
	events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &index, ID: "call_search", Function: ChatFunctionCall{Name: toolSearchProxyName, Arguments: `{"query":"gmail"}`}}},
	}}}}, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(state)...)

	var done *ResponsesStreamEvent
	for i := range events {
		event := &events[i]
		if event.Type == "response.output_item.done" && event.Item != nil && event.Item.CallID == "call_search" {
			done = event
		}
	}
	require.NotNil(t, done)
	assert.Equal(t, "tool_search_call", done.Item.Type)

	sse, err := ResponsesEventToSSE(*done)
	require.NoError(t, err)
	assert.Contains(t, sse, `"execution":"client"`)
	assert.Contains(t, sse, `"arguments":{"query":"gmail"}`)
}
