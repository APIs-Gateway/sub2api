package apicompat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newResponsesToChatToolState(t *testing.T, outputIndex int, itemType string) *ResponsesEventToChatState {
	t.Helper()
	state := NewResponsesEventToChatState()
	state.Model = "gpt-4o"
	state.SentRole = true
	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: outputIndex,
		Item:        &ResponsesOutput{Type: itemType, CallID: "call_1", Name: "apply_patch"},
	}, state)
	require.Len(t, chunks, 1)
	return state
}

// custom_tool_call_input.done 携带的是 Input 而不是 Arguments：只补发比已下发内容多出的后缀。
func TestResponsesEventToChatChunks_CustomToolInputDoneEmitsSuffix(t *testing.T) {
	state := newResponsesToChatToolState(t, 3, "custom_tool_call")

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.custom_tool_call_input.delta",
		OutputIndex: 3,
		Delta:       "*** Begin",
	}, state)
	require.Len(t, chunks, 1)

	chunks = ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.custom_tool_call_input.done",
		OutputIndex: 3,
		Input:       "*** Begin Patch",
	}, state)
	require.Len(t, chunks, 1)
	tc := chunks[0].Choices[0].Delta.ToolCalls[0]
	require.NotNil(t, tc.Index)
	assert.Equal(t, 0, *tc.Index)
	assert.Equal(t, " Patch", tc.Function.Arguments)

	// 同样内容的重复 done 不再补发。
	chunks = ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.custom_tool_call_input.done",
		OutputIndex: 3,
		Input:       "*** Begin Patch",
	}, state)
	assert.Empty(t, chunks)
}

// done 事件指向未登记的 output_index 时忽略。
func TestResponsesEventToChatChunks_ArgumentsDoneUnknownOutputIndexIgnored(t *testing.T) {
	state := newResponsesToChatToolState(t, 1, "function_call")

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 9,
		Arguments:   `{"a":1}`,
	}, state)
	assert.Empty(t, chunks)
}

// 缓冲路径：custom_tool_call_input.done 用 Input 覆盖已累计的增量。
func TestBufferedResponseAccumulator_CustomToolInputDoneReplacesDeltas(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "custom_tool_call", CallID: "call_patch", Name: "apply_patch"},
	})
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.custom_tool_call_input.delta",
		OutputIndex: 0,
		Delta:       "*** Begin",
	})
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.custom_tool_call_input.done",
		OutputIndex: 0,
		Input:       "*** Begin Patch",
	})

	output := acc.BuildOutput()
	require.Len(t, output, 1)
	assert.Equal(t, "*** Begin Patch", output[0].Arguments)
}

func TestBufferedResponseAccumulator_SupplementNilResponse(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	assert.NotPanics(t, func() { acc.SupplementResponseOutput(nil) })
}

// 终态 function_call 与缓冲的调用 call_id 和 output_index 都对不上时不补参数。
func TestBufferedResponseAccumulator_SupplementSkipsNonMatchingCall(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 5,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_other", Name: "lookup_item"},
	})
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 5,
		Arguments:   `{"id":1}`,
	})

	resp := &ResponsesResponse{Output: []ResponsesOutput{{
		Type:   "function_call",
		CallID: "call_lookup",
		Name:   "lookup_item",
	}}}
	acc.SupplementResponseOutput(resp)

	require.Len(t, resp.Output, 1)
	assert.Empty(t, resp.Output[0].Arguments)
}
