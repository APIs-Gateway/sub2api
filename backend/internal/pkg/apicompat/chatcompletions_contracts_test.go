package apicompat

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChatResponsesContracts_LegacyMixedHistory(t *testing.T) {
	for _, tt := range []struct {
		name     string
		messages []ChatMessage
		ids      []string
	}{
		{"multiple pending same name FIFO", []ChatMessage{
			{Role: "assistant", FunctionCall: &ChatFunctionCall{Name: "lookup", Arguments: `{"n":1}`}},
			{Role: "assistant", FunctionCall: &ChatFunctionCall{Name: "lookup", Arguments: `{"n":2}`}},
			{Role: "function", Name: "lookup", Content: json.RawMessage(`"one"`)},
			{Role: "function", Name: "lookup", Content: json.RawMessage(`"two"`)},
		}, []string{"call_legacy_1", "call_legacy_2", "call_legacy_1", "call_legacy_2"}},
		{"existing output ID reserved", []ChatMessage{
			{Role: "tool", ToolCallID: "call_legacy_1", Content: json.RawMessage(`"prior"`)},
			{Role: "assistant", FunctionCall: &ChatFunctionCall{Name: "lookup"}},
			{Role: "function", Name: "lookup", Content: json.RawMessage(`null`)},
		}, []string{"call_legacy_1", "call_legacy_2", "call_legacy_2"}},
		{"modern tool calls take precedence", []ChatMessage{
			{Role: "assistant", FunctionCall: &ChatFunctionCall{Name: "ignored"}, ToolCalls: []ChatToolCall{{ID: "modern", Type: "function", Function: ChatFunctionCall{Name: "lookup", Arguments: `{"n":7}`}}}},
			{Role: "tool", ToolCallID: "modern", Content: json.RawMessage(`"done"`)},
		}, []string{"modern", "modern"}},
		{"explicit legacy output ID kept", []ChatMessage{{Role: "function", Name: "old-name", ToolCallID: "explicit", Content: json.RawMessage(`"done"`)}}, []string{"explicit"}},
		{"unmatched name does not steal pending call", []ChatMessage{
			{Role: "assistant", FunctionCall: &ChatFunctionCall{Name: "lookup"}},
			{Role: "function", Name: "other", Content: json.RawMessage(`"orphan"`)},
			{Role: "function", Name: "lookup", Content: json.RawMessage(`"paired"`)},
		}, []string{"call_legacy_1", "other", "call_legacy_1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before, err := json.Marshal(tt.messages)
			require.NoError(t, err)
			items := convertLegacyFunctionHistory(t, tt.messages)
			var ids []string
			for _, item := range items {
				ids = append(ids, item.CallID)
			}
			require.Equal(t, tt.ids, ids)
			require.Equal(t, items, convertLegacyFunctionHistory(t, tt.messages))
			after, err := json.Marshal(tt.messages)
			require.NoError(t, err)
			require.Equal(t, before, after, "converter must not mutate caller history")
			if tt.name == "modern tool calls take precedence" {
				require.Equal(t, "lookup", items[0].Name)
				require.Equal(t, `{"n":7}`, items[0].Arguments)
			}
			if tt.name == "existing output ID reserved" {
				require.Equal(t, "{}", items[1].Arguments)
				require.Equal(t, "(empty)", items[2].Output)
			}
		})
	}
}

func TestChatResponsesContracts_DeveloperKeepsTypedMedia(t *testing.T) {
	for _, role := range []string{"developer", "system", "user"} {
		t.Run(role, func(t *testing.T) {
			req := &ChatCompletionsRequest{Model: "gpt-4o", Messages: []ChatMessage{{Role: role, Content: json.RawMessage(`[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]`)}}}
			resp, err := ChatCompletionsToResponses(req)
			require.NoError(t, err)
			var items []ResponsesInputItem
			require.NoError(t, json.Unmarshal(resp.Input, &items))
			require.Len(t, items, 1)
			require.Equal(t, "message", items[0].Type)
			require.Equal(t, role, items[0].Role)
			require.JSONEq(t, `[{"type":"input_text","text":"inspect"},{"type":"input_image","image_url":"https://example.com/image.png"}]`, string(items[0].Content))
		})
	}
}

func TestChatResponsesContracts_ChoiceControls(t *testing.T) {
	for _, raw := range []string{`{"type":"function","function":{"name":" "}}`, `{"type":"function","function":null}`, `{"type":"function","function":{"name":7}}`, `{"type":"extension","function":{"name":"lookup"},"opaque":{"name":"keep"}}`, `null`, `"required"`} {
		t.Run(raw, func(t *testing.T) {
			resp, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Model: "gpt-4o", ToolChoice: json.RawMessage(raw), FunctionCall: json.RawMessage(`{"name":"legacy"}`)})
			require.NoError(t, err)
			require.JSONEq(t, raw, string(resp.ToolChoice), "explicit choice still takes precedence and unrecognized shapes are untouched")
		})
	}
}
