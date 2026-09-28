package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// assertInterleavedLifecycle checks both halves of the default
// Chat Completions -> Responses -> Anthropic streaming route. In particular,
// every delta must target an item/block that is still open, and a Responses
// output_text part must be completed exactly once.
func assertInterleavedLifecycle(t *testing.T, chunks []string) ([]ResponsesStreamEvent, []AnthropicStreamEvent) {
	t.Helper()
	cc := NewChatCompletionsToResponsesStreamState("interleaved-model")
	anthropic := NewResponsesEventToAnthropicState()
	var responsesEvents []ResponsesStreamEvent
	var anthropicEvents []AnthropicStreamEvent
	forward := func(events []ResponsesStreamEvent) {
		for _, event := range events {
			responsesEvents = append(responsesEvents, event)
			anthropicEvents = append(anthropicEvents, ResponsesEventToAnthropicEvents(&event, anthropic)...)
		}
	}
	for _, payload := range chunks {
		var chunk ChatCompletionsChunk
		require.NoError(t, json.Unmarshal([]byte(payload), &chunk))
		forward(ChatCompletionsChunkToResponsesEvents(&chunk, cc))
	}
	require.NoError(t, cc.ValidateToolCallArguments())
	forward(FinalizeChatCompletionsResponsesStream(cc))
	require.Empty(t, FinalizeResponsesAnthropicStream(anthropic))

	openItems := make(map[int]string)
	openParts := make(map[int]bool)
	itemDone := make(map[int]int)
	textDone := make(map[int]int)
	for _, event := range responsesEvents {
		switch event.Type {
		case "response.output_item.added":
			require.NotContains(t, openItems, event.OutputIndex)
			require.Zero(t, itemDone[event.OutputIndex])
			openItems[event.OutputIndex] = event.Item.ID
		case "response.content_part.added":
			require.Contains(t, openItems, event.OutputIndex)
			require.False(t, openParts[event.OutputIndex])
			openParts[event.OutputIndex] = true
		case "response.output_text.delta":
			require.True(t, openParts[event.OutputIndex])
			require.Equal(t, openItems[event.OutputIndex], event.ItemID)
		case "response.reasoning_summary_text.delta", "response.function_call_arguments.delta":
			require.Contains(t, openItems, event.OutputIndex)
		case "response.output_text.done":
			require.True(t, openParts[event.OutputIndex])
			textDone[event.OutputIndex]++
		case "response.content_part.done":
			require.True(t, openParts[event.OutputIndex])
			openParts[event.OutputIndex] = false
		case "response.output_item.done":
			require.Equal(t, openItems[event.OutputIndex], event.Item.ID)
			require.False(t, openParts[event.OutputIndex])
			delete(openItems, event.OutputIndex)
			itemDone[event.OutputIndex]++
		case "response.completed":
			require.Empty(t, openItems)
			require.Len(t, event.Response.Output, len(itemDone))
			for index, item := range event.Response.Output {
				require.Equal(t, 1, itemDone[index])
				require.Equal(t, item.ID, responsesDoneItemID(responsesEvents, index))
			}
		}
	}
	for _, count := range textDone {
		require.Equal(t, 1, count)
	}

	openBlocks := make(map[int]bool)
	started := 0
	stopCount := 0
	for _, event := range anthropicEvents {
		switch event.Type {
		case "content_block_start":
			require.NotNil(t, event.Index)
			require.Equal(t, started, *event.Index)
			require.False(t, openBlocks[*event.Index])
			openBlocks[*event.Index] = true
			started++
		case "content_block_delta":
			require.NotNil(t, event.Index)
			require.Truef(t, openBlocks[*event.Index], "delta on stopped or unstarted Anthropic block %d", *event.Index)
		case "content_block_stop":
			require.NotNil(t, event.Index)
			require.True(t, openBlocks[*event.Index])
			delete(openBlocks, *event.Index)
		case "message_stop":
			stopCount++
			require.Empty(t, openBlocks)
		}
	}
	require.Equal(t, 1, stopCount)
	return responsesEvents, anthropicEvents
}

func responsesDoneItemID(events []ResponsesStreamEvent, index int) string {
	for _, event := range events {
		if event.Type == "response.output_item.done" && event.OutputIndex == index && event.Item != nil {
			return event.Item.ID
		}
	}
	return ""
}

func TestInterleavedTextReasoningTextHasDistinctItemsAndBlocks(t *testing.T) {
	responsesEvents, anthropicEvents := assertInterleavedLifecycle(t, []string{
		`{"choices":[{"index":0,"delta":{"content":"before"}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"after"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}}`,
	})
	var textDone []string
	for _, event := range responsesEvents {
		if event.Type == "response.output_text.done" {
			textDone = append(textDone, event.Text)
		}
		if event.Type == "response.completed" {
			require.Equal(t, []string{"message", "reasoning", "message"}, []string{event.Response.Output[0].Type, event.Response.Output[1].Type, event.Response.Output[2].Type})
			require.Equal(t, "before", event.Response.Output[0].Content[0].Text)
			require.Equal(t, "think", event.Response.Output[1].Summary[0].Text)
			require.Equal(t, "after", event.Response.Output[2].Content[0].Text)
		}
	}
	require.Equal(t, []string{"before", "after"}, textDone)
	var textDeltas []string
	for _, event := range anthropicEvents {
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			textDeltas = append(textDeltas, event.Delta.Text)
		}
		if event.Type == "message_delta" {
			require.Equal(t, "end_turn", event.Delta.StopReason)
			require.Equal(t, 8, event.Usage.InputTokens)
			require.Equal(t, 5, event.Usage.OutputTokens)
		}
	}
	require.Equal(t, []string{"before", "after"}, textDeltas)
}

func TestInterleavedToolsLateDeltasStayWithOwner(t *testing.T) {
	responsesEvents, anthropicEvents := assertInterleavedLifecycle(t, []string{
		`{"choices":[{"index":0,"delta":{"content":"before"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"tool_b","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"after","tool_calls":[{"index":1,"function":{"arguments":" "}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})
	var final *ResponsesResponse
	for _, event := range responsesEvents {
		if event.Type == "response.completed" {
			final = event.Response
		}
	}
	require.NotNil(t, final)
	require.Len(t, final.Output, 4)
	require.Equal(t, []string{"message", "function_call", "function_call", "message"}, []string{final.Output[0].Type, final.Output[1].Type, final.Output[2].Type, final.Output[3].Type})
	require.JSONEq(t, `{"a":1}`, final.Output[1].Arguments)
	require.JSONEq(t, `{}`, final.Output[2].Arguments)
	require.Equal(t, "after", final.Output[3].Content[0].Text)
	toolJSON := make(map[int]string)
	for _, event := range anthropicEvents {
		if event.Type == "content_block_delta" && event.Delta.Type == "input_json_delta" {
			toolJSON[*event.Index] += event.Delta.PartialJSON
		}
		if event.Type == "message_delta" {
			require.Equal(t, "tool_use", event.Delta.StopReason)
		}
	}
	require.JSONEq(t, `{"a":1}`, toolJSON[1])
	require.JSONEq(t, `{}`, toolJSON[2])
}

func TestInterleavedReasoningResumesInNewItem(t *testing.T) {
	responsesEvents, _ := assertInterleavedLifecycle(t, []string{
		`{"choices":[{"index":0,"delta":{"reasoning_content":"first"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"visible"}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"second"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})
	for _, event := range responsesEvents {
		if event.Type == "response.completed" {
			require.Len(t, event.Response.Output, 3)
			require.Equal(t, "first", event.Response.Output[0].Summary[0].Text)
			require.Equal(t, "visible", event.Response.Output[1].Content[0].Text)
			require.Equal(t, "second", event.Response.Output[2].Summary[0].Text)
		}
	}
}

func TestAnnouncedDoneOnlyTextAfterEarlierTextIsDelivered(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "r"}}, state)
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.content_part.added", OutputIndex: 0, Part: &ResponsesContentPart{Type: "output_text"}}, state)
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 0, Delta: "first"}, state)
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 0, Text: "first"}, state)
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.content_part.added", OutputIndex: 1, Part: &ResponsesContentPart{Type: "output_text"}}, state)
	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 1, Text: "second"}, state)
	require.Len(t, events, 3)
	require.Equal(t, "content_block_start", events[0].Type)
	require.Equal(t, "second", events[1].Delta.Text)
	require.Equal(t, "content_block_stop", events[2].Type)
	require.Equal(t, *events[0].Index, *events[2].Index)
}

func TestEarlierMessageDoneDoesNotCloseLaterToolBlock(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "r"}}, state)
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 0, Delta: "first"}, state)
	toolStart := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 1, Item: &ResponsesOutput{Type: "function_call", Name: "Write", CallID: "call_1"}}, state)
	require.Len(t, toolStart, 1)
	toolIndex := *toolStart[0].Index
	messageDone := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.done", OutputIndex: 0, Item: &ResponsesOutput{Type: "message", Content: []ResponsesContentPart{{Type: "output_text", Text: "first"}}}}, state)
	for _, event := range messageDone {
		if event.Type == "content_block_stop" {
			require.NotEqual(t, toolIndex, *event.Index)
		}
	}
	toolDelta := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 1, Delta: `{"path":"x"}`}, state)
	require.Len(t, toolDelta, 1)
	require.Equal(t, toolIndex, *toolDelta[0].Index)
	require.Equal(t, `{"path":"x"}`, toolDelta[0].Delta.PartialJSON)
	toolDone := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 1, Arguments: `{"path":"x"}`}, state)
	require.Len(t, toolDone, 1)
	require.Equal(t, toolIndex, *toolDone[0].Index)
	require.Equal(t, "content_block_stop", toolDone[0].Type)
}

func TestInterleavedReadSanitizationAndLateReasoningSignature(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "r"}}, state)
	readStart := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", Name: "Read", CallID: "call_read"}}, state)
	otherStart := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 1, Item: &ResponsesOutput{Type: "function_call", Name: "Write", CallID: "call_write"}}, state)
	thinkingStart := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 2, Item: &ResponsesOutput{Type: "reasoning"}}, state)
	readIndex, otherIndex, thinkingIndex := *readStart[0].Index, *otherStart[0].Index, *thinkingStart[0].Index
	require.Empty(t, ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 0, Delta: `{"file_path":"a",`}, state))
	otherDelta := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 1, Delta: `{}`}, state)
	require.Equal(t, otherIndex, *otherDelta[0].Index)
	readDelta := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 0, Delta: `"pages":""}`}, state)
	require.Len(t, readDelta, 1)
	require.Equal(t, readIndex, *readDelta[0].Index)
	require.JSONEq(t, `{"file_path":"a"}`, readDelta[0].Delta.PartialJSON)
	require.Equal(t, readIndex, *ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 0, Arguments: `{"file_path":"a","pages":""}`}, state)[0].Index)
	thinkingDelta := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.reasoning_summary_text.delta", OutputIndex: 2, Delta: "thought"}, state)
	require.Equal(t, thinkingIndex, *thinkingDelta[0].Index)
	thinkingDone := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.done", OutputIndex: 2, Item: &ResponsesOutput{Type: "reasoning", EncryptedContent: "late-signature"}}, state)
	require.Len(t, thinkingDone, 2)
	require.Equal(t, "signature_delta", thinkingDone[0].Delta.Type)
	require.Equal(t, "late-signature", thinkingDone[0].Delta.Signature)
	require.Equal(t, thinkingIndex, *thinkingDone[1].Index)
	otherDone := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 1, Arguments: `{}`}, state)
	require.Len(t, otherDone, 1)
	require.Equal(t, otherIndex, *otherDone[0].Index)
}
