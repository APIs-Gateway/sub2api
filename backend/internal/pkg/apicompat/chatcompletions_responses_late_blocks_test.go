package apicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatCompletionsStreamLateBlocksKeepAnthropicLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		chunks     []string
		blockTypes []string
		toolCall   bool
		toolJSON   string
	}{
		{
			name: "text then reasoning then tool",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`{"choices":[{"index":0,"delta":{"reasoning_content":"late thought"}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			},
			blockTypes: []string{"text", "thinking", "tool_use"},
			toolCall:   true,
			toolJSON:   `{"cmd":"ls"}`,
		},
		{
			name: "text then tool",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			},
			blockTypes: []string{"text", "tool_use"},
			toolCall:   true,
			toolJSON:   `{"cmd":"ls"}`,
		},
		{
			name: "text then tool with arguments only at finalize",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":""}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			},
			blockTypes: []string{"text", "tool_use"},
			toolCall:   true,
			toolJSON:   `{}`,
		},
		{
			name: "text then reasoning",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`{"choices":[{"index":0,"delta":{"reasoning_content":"late thought"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			},
			blockTypes: []string{"text", "thinking"},
		},
		{
			name: "reasoning before text remains valid",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"reasoning_content":"early thought"}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			},
			blockTypes: []string{"thinking", "text"},
		},
		{
			name: "text only remains valid",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			},
			blockTypes: []string{"text"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responsesEvents := collectStreamEvents(t, tt.chunks)
			anthropicState := NewResponsesEventToAnthropicState()
			var anthropicEvents []AnthropicStreamEvent
			textDoneCount, textDoneAt, laterItemAt := 0, -1, -1
			var toolArguments string
			for i, responseEvent := range responsesEvents {
				if responseEvent.Type == "response.output_text.done" {
					textDoneCount++
					textDoneAt = i
					require.Equal(t, "answer", responseEvent.Text)
				}
				if responseEvent.Type == "response.output_item.added" && responseEvent.Item != nil &&
					(responseEvent.Item.Type == "reasoning" || responseEvent.Item.Type == "function_call") &&
					laterItemAt == -1 && tt.blockTypes[0] == "text" && len(tt.blockTypes) > 1 {
					laterItemAt = i
				}
				if responseEvent.Type == "response.function_call_arguments.done" {
					toolArguments = responseEvent.Arguments
				}
				anthropicEvents = append(anthropicEvents, ResponsesEventToAnthropicEvents(&responseEvent, anthropicState)...)
			}
			require.Equal(t, 1, textDoneCount)
			if laterItemAt >= 0 {
				require.Less(t, textDoneAt, laterItemAt, "text must close before a later item opens")
			}
			if tt.toolCall {
				require.Equal(t, tt.toolJSON, toolArguments)
			}

			var started []string
			openIndex := -1
			stopped := 0
			var receivedToolJSON string
			for _, event := range anthropicEvents {
				switch event.Type {
				case "content_block_start":
					require.NotNil(t, event.Index)
					require.NotNil(t, event.ContentBlock)
					require.Equal(t, -1, openIndex, "previous content block was not closed")
					openIndex = *event.Index
					started = append(started, event.ContentBlock.Type)
				case "content_block_delta":
					require.NotNil(t, event.Index)
					require.Equal(t, openIndex, *event.Index, "delta must target the open content block")
					if event.Delta != nil && event.Delta.Type == "input_json_delta" {
						receivedToolJSON += event.Delta.PartialJSON
					}
				case "content_block_stop":
					require.NotNil(t, event.Index)
					require.Equal(t, openIndex, *event.Index, "stop must target the open content block")
					openIndex = -1
					stopped++
				case "message_stop":
					require.Equal(t, -1, openIndex, "message ended with a content block open")
				}
			}
			require.Equal(t, tt.blockTypes, started)
			require.Equal(t, len(started), stopped)
			require.Equal(t, -1, openIndex)
			if tt.toolCall {
				require.Equal(t, tt.toolJSON, receivedToolJSON, "Anthropic client must receive the tool arguments")
			}
		})
	}
}
