package apicompat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicToResponsesResponse_RestoresCustomToolCall(t *testing.T) {
	resp := &AnthropicResponse{
		ID:    "msg_custom",
		Model: "claude-test",
		Content: []AnthropicContentBlock{{
			Type:  "tool_use",
			ID:    "toolu_exec",
			Name:  "exec",
			Input: []byte(`{"input":"ls -la"}`),
		}},
	}

	out := AnthropicToResponsesResponseWithCustomTools(resp, map[string]bool{"exec": true})
	require.Len(t, out.Output, 1)
	assert.Equal(t, "custom_tool_call", out.Output[0].Type)
	assert.Equal(t, "exec", out.Output[0].Name)
	assert.Equal(t, "ls -la", out.Output[0].Input)
	assert.Empty(t, out.Output[0].Arguments)
}

func TestAnthropicEventToResponsesEvents_RestoresCustomToolCallLifecycle(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.CustomTools = map[string]bool{"exec": true}

	added := AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "content_block_start",
		ContentBlock: &AnthropicContentBlock{
			Type: "tool_use",
			ID:   "toolu_exec",
			Name: "exec",
		},
	}, state)
	require.Len(t, added, 1)
	require.NotNil(t, added[0].Item)
	assert.Equal(t, "custom_tool_call", added[0].Item.Type)

	deltas := AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "content_block_delta",
		Delta: &AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: `{"input":"ls -la"}`,
		},
	}, state)
	assert.Empty(t, deltas, "custom input is emitted as text after the full JSON object is available")

	stopped := AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "content_block_stop"}, state)
	require.Len(t, stopped, 3)
	assert.Equal(t, "response.custom_tool_call_input.delta", stopped[0].Type)
	assert.Equal(t, "ls -la", stopped[0].Delta)
	assert.Equal(t, "response.custom_tool_call_input.done", stopped[1].Type)
	assert.Equal(t, "ls -la", stopped[1].Input)
	assert.Equal(t, "response.output_item.done", stopped[2].Type)
	require.NotNil(t, stopped[2].Item)
	assert.Equal(t, "custom_tool_call", stopped[2].Item.Type)
	assert.Equal(t, "exec", stopped[2].Item.Name)
	assert.Equal(t, "ls -la", stopped[2].Item.Input)
}
