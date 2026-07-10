package apicompat

import (
	"encoding/json"
	"strings"
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

func TestResponsesToChatCompletionsRequest_ProxyToolReplayAndNamespaceEdges(t *testing.T) {
	longNamespace := "namespace_" + strings.Repeat("n", 80)
	longName := "tool_" + strings.Repeat("x", 80)
	req := &ResponsesRequest{
		Model: "gpt-5.6",
		Input: json.RawMessage(`[
			{"type":"custom_tool_call","call_id":"call_exec","name":"exec","input":"pwd"},
			{"type":"tool_search_call","call_id":"call_search","arguments":{"query":"gmail"}},
			{"type":"custom_tool_call_output","call_id":"call_exec","output":{"stdout":"/tmp"}},
			{"type":"tool_search_output","call_id":"call_search","output":["gmail__send"]}
		]`),
		Tools: []ResponsesTool{{
			Type: "namespace", Name: longNamespace,
			Children: []ResponsesTool{{Type: "function", Name: longName, Parameters: json.RawMessage(`{"type":"object"}`)}},
		}},
		ToolChoice: json.RawMessage(`"auto"`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)
	assert.LessOrEqual(t, len(out.Tools[0].Function.Name), chatToolNameMaxLen)
	assert.JSONEq(t, `"auto"`, string(out.ToolChoice))
	require.Len(t, out.Messages, 3)
	assert.Equal(t, "exec", out.Messages[0].ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"input":"pwd"}`, out.Messages[0].ToolCalls[0].Function.Arguments)
	assert.Equal(t, toolSearchProxyName, out.Messages[0].ToolCalls[1].Function.Name)
	assert.JSONEq(t, `{"query":"gmail"}`, out.Messages[0].ToolCalls[1].Function.Arguments)
	var customOutput, searchOutput string
	require.NoError(t, json.Unmarshal(out.Messages[1].Content, &customOutput))
	require.NoError(t, json.Unmarshal(out.Messages[2].Content, &searchOutput))
	assert.Equal(t, `{"stdout":"/tmp"}`, customOutput)
	assert.Equal(t, `["gmail__send"]`, searchOutput)

	_, err = ResponsesToChatCompletionsRequest(&ResponsesRequest{
		Model: "gpt-5.6", Input: json.RawMessage(`"help"`),
		Tools: []ResponsesTool{
			{Type: "namespace", Name: "one", Tools: []ResponsesTool{{Type: "function", Name: "same"}}},
			{Type: "namespace", Name: "one", Children: []ResponsesTool{{Type: "function", Name: "same"}}},
		},
	})
	require.NoError(t, err, "identical namespace children are de-duplicated")
}

func TestResponsesProxyToolWireHandlesMalformedArgumentsAndCustomInput(t *testing.T) {
	var declared ResponsesTool
	require.NoError(t, json.Unmarshal([]byte(`"exec"`), &declared))
	assert.Equal(t, "custom", declared.Type)
	assert.Equal(t, "exec", declared.Name)

	context := NewResponsesChatToolContext([]ResponsesTool{{Type: "custom", Name: "exec"}, {Type: "tool_search"}})
	response := ChatCompletionsResponseToResponsesWithToolContext(&ChatCompletionsResponse{
		Choices: []ChatChoice{{Message: ChatMessage{ToolCalls: []ChatToolCall{
			{ID: "call_exec", Function: ChatFunctionCall{Name: "exec", Arguments: `{"other":1}`}},
			{ID: "call_search", Function: ChatFunctionCall{Name: toolSearchProxyName, Arguments: `not-json`}},
		}}}},
	}, "gpt-5.6", context)
	require.Len(t, response.Output, 2)
	assert.Equal(t, `{"other":1}`, response.Output[0].Input)
	encoded, err := json.Marshal(response.Output[1])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"tool_search_call","id":"`+response.Output[1].ID+`","call_id":"call_search","execution":"client","arguments":"not-json","status":"completed"}`, string(encoded))
}

func TestChatCompletionsResponseWithToolContextKeepsTerminalAndEmptySemantics(t *testing.T) {
	empty := ChatCompletionsResponseToResponsesWithToolContext(nil, "gpt-5.6", ResponsesChatToolContext{})
	require.Len(t, empty.Output, 1)
	assert.Equal(t, "message", empty.Output[0].Type)

	content := json.RawMessage(`"done"`)
	length := ChatCompletionsResponseToResponsesWithToolContext(&ChatCompletionsResponse{
		Model:   "upstream-model",
		Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: content}, FinishReason: "length"}},
	}, "", ResponsesChatToolContext{})
	assert.Equal(t, "upstream-model", length.Model)
	assert.Equal(t, "incomplete", length.Status)
	require.NotNil(t, length.IncompleteDetails)
	assert.Equal(t, "max_output_tokens", length.IncompleteDetails.Reason)
}

func TestResponsesProxyToolWirePreservesCustomAndNamespaceFields(t *testing.T) {
	custom := ResponsesStreamEvent{Type: "response.output_item.done", Item: &ResponsesOutput{
		Type: "custom_tool_call", ID: "item_custom", CallID: "call_custom", Name: "exec", Input: "pwd", Status: "completed",
	}}
	namespace := ResponsesStreamEvent{Type: "response.output_item.done", Item: &ResponsesOutput{
		Type: "function_call", ID: "item_ns", CallID: "call_ns", Namespace: "gmail", Name: "send", Arguments: `{"to":"a@example.test"}`, Status: "completed",
	}}

	customSSE, err := ResponsesEventToSSE(custom)
	require.NoError(t, err)
	assert.Contains(t, customSSE, `"input":"pwd"`)
	assert.Contains(t, customSSE, `"name":"exec"`)

	namespaceSSE, err := ResponsesEventToSSE(namespace)
	require.NoError(t, err)
	assert.Contains(t, namespaceSSE, `"namespace":"gmail"`)
	assert.Contains(t, namespaceSSE, `"name":"send"`)
}

func TestResponsesProxyCompatibilityHelpersCoverContentUsageAndWireBranches(t *testing.T) {
	content, err := responsesContentPartsToChatContent([]json.RawMessage{
		json.RawMessage(`{"type":"input_text","text":"hello"}`),
		json.RawMessage(`{"type":"input_image","image_url":{"url":"https://example.test/image.png"}}`),
	}, "user")
	require.NoError(t, err)
	assert.Contains(t, string(content), "image_url")

	single, err := chatContentFromSingleResponsesPart("input_image", map[string]json.RawMessage{
		"image_url": json.RawMessage(`"https://example.test/direct.png"`),
	})
	require.NoError(t, err)
	assert.Contains(t, string(single), "direct.png")
	textSingle, err := chatContentFromSingleResponsesPart("input_text", map[string]json.RawMessage{"text": json.RawMessage(`"plain"`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"plain"`, string(textSingle))

	assert.Equal(t, "plain", chatMessageContentText(json.RawMessage(`"plain"`)))
	assert.Equal(t, "first\n\nsecond", chatMessageContentText(json.RawMessage(`[{"type":"text","text":"first"},{"type":"text","text":"second"}]`)))
	assert.Equal(t, "", chatMessageContentText(json.RawMessage(`null`)))

	usage := ChatUsageToResponsesUsage(&ChatUsage{
		PromptTokens: 3, CompletionTokens: 2,
		PromptTokensDetails: &ChatTokenDetails{CachedTokens: 1, CacheWriteTokens: 1},
	})
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	require.NotNil(t, usage.InputTokensDetails)
	assert.Equal(t, 1, usage.InputTokensDetails.CacheWriteTokens)
	assert.Nil(t, ChatUsageToResponsesUsage(nil))

	messageWire := responsesItemWire(&ResponsesOutput{Type: "message", ID: "msg", Content: []ResponsesContentPart{{Type: "output_text", Text: "ok"}}})
	assert.Equal(t, "assistant", messageWire["role"])
	reasoningWire := responsesItemWire(&ResponsesOutput{Type: "reasoning", ID: "reason", Summary: []ResponsesSummary{{Text: "think"}}})
	assert.NotEmpty(t, reasoningWire["summary"])
	assert.Empty(t, responsesItemWire(nil))

	var objectTool ResponsesTool
	require.NoError(t, json.Unmarshal([]byte(`{"type":"namespace","name":"drive","children":[{"type":"function","name":"list"}]}`), &objectTool))
	assert.Equal(t, "drive", objectTool.Name)
	require.Len(t, objectTool.Children, 1)
	var invalidTool ResponsesTool
	require.Error(t, json.Unmarshal([]byte(`{`), &invalidTool))

	emptyResponse := ChatCompletionsResponseToResponses(nil, "gpt-5.6")
	require.Len(t, emptyResponse.Output, 1)
	assert.Equal(t, "message", emptyResponse.Output[0].Type)
}
