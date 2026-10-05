package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const refusalText = "I can't help with that."

func decodeResponsesEvent(t *testing.T, raw string) ResponsesStreamEvent {
	t.Helper()
	var evt ResponsesStreamEvent
	require.NoError(t, json.Unmarshal([]byte(raw), &evt))
	return evt
}

func chatMessageJSON(t *testing.T, resp *ChatCompletionsResponse) map[string]any {
	t.Helper()
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	var decoded struct {
		Choices []struct {
			Message map[string]any `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded.Choices, 1)
	return decoded.Choices[0].Message
}

// A refusal arrives as a "refusal" content part, not output_text. Dropping it
// leaves the Chat client with an empty assistant message.
func TestResponsesToChatCompletions_RefusalPreserved(t *testing.T) {
	var resp ResponsesResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"resp_refusal",
		"status":"completed",
		"output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"`+refusalText+`"}]}]
	}`), &resp))

	chat := ResponsesToChatCompletions(&resp, "gpt-4o")

	msg := chatMessageJSON(t, chat)
	require.Equal(t, refusalText, msg["refusal"])
	require.NotContains(t, msg, "content")
	require.Equal(t, "stop", chat.Choices[0].FinishReason)
}

func TestResponsesEventToChatChunks_RefusalDelta(t *testing.T) {
	state := NewResponsesEventToChatState()
	events := []string{
		`{"type":"response.created","response":{"id":"resp_refusal","model":"gpt-4o"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant"}}`,
		`{"type":"response.refusal.delta","output_index":0,"content_index":0,"delta":"I can't "}`,
		`{"type":"response.refusal.delta","output_index":0,"content_index":0,"delta":"help with that."}`,
		`{"type":"response.refusal.done","output_index":0,"content_index":0,"refusal":"` + refusalText + `"}`,
		`{"type":"response.completed","response":{"id":"resp_refusal","status":"completed"}}`,
	}

	var refusal strings.Builder
	var finishReason string
	for _, raw := range events {
		evt := decodeResponsesEvent(t, raw)
		for _, chunk := range ResponsesEventToChatChunks(&evt, state) {
			encoded, err := json.Marshal(chunk)
			require.NoError(t, err)
			var decoded struct {
				Choices []struct {
					Delta struct {
						Refusal *string `json:"refusal"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			for _, choice := range decoded.Choices {
				if choice.Delta.Refusal != nil {
					_, _ = refusal.WriteString(*choice.Delta.Refusal)
				}
				if choice.FinishReason != nil {
					finishReason = *choice.FinishReason
				}
			}
		}
	}

	require.Equal(t, refusalText, refusal.String())
	require.Equal(t, "stop", finishReason)
}

// Non-streaming Chat requests are served from a forced upstream stream; when
// the terminal event carries an empty output array the message is rebuilt from
// the accumulated deltas, which must include refusal deltas.
func TestBufferedResponseAccumulator_RefusalRebuiltFromDeltas(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant"}}`,
		`{"type":"response.refusal.delta","output_index":0,"content_index":0,"delta":"I can't "}`,
		`{"type":"response.refusal.delta","output_index":0,"content_index":0,"delta":"help with that."}`,
	} {
		evt := decodeResponsesEvent(t, raw)
		acc.ProcessEvent(&evt)
	}
	require.True(t, acc.HasContent())

	final := &ResponsesResponse{ID: "resp_refusal", Status: "completed"}
	acc.SupplementResponseOutput(final)
	chat := ResponsesToChatCompletions(final, "gpt-4o")

	require.Equal(t, refusalText, chatMessageJSON(t, chat)["refusal"])
}

func TestResponsesRefusal_PartialTerminalAndMixedContent(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		expected     string
	}{
		{"empty message", `[{"type":"message","role":"assistant","content":[]}]`, "streamed refusal"},
		{"empty refusal part", `[{"type":"message","content":[{"type":"refusal","refusal":""}]}]`, "streamed refusal"},
		{"text-only message", `[{"type":"message","content":[{"type":"output_text","text":"terminal text"}]}]`, "streamed refusal"},
		{"tools-only", `[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]`, "streamed refusal"},
		{"reasoning-only", `[{"type":"reasoning","summary":[{"type":"summary_text","text":"reason"}]}]`, "streamed refusal"},
		{"authoritative refusal", `[{"type":"message","content":[{"type":"refusal","refusal":"terminal refusal"}]}]`, "terminal refusal"},
		{"nonmessage refusal not authority", `[{"type":"other","content":[{"type":"refusal","refusal":"not an answer"}]}]`, "streamed refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := NewBufferedResponseAccumulator()
			e := decodeResponsesEvent(t, `{"type":"response.refusal.delta","delta":"streamed refusal"}`)
			acc.ProcessEvent(&e)
			var resp ResponsesResponse
			require.NoError(t, json.Unmarshal([]byte(`{"status":"completed","output":`+tc.output+`}`), &resp))
			acc.SupplementResponseOutput(&resp)
			first, err := json.Marshal(resp.Output)
			require.NoError(t, err)
			acc.SupplementResponseOutput(&resp)
			again, err := json.Marshal(resp.Output)
			require.NoError(t, err)
			require.Equal(t, string(first), string(again), "supplement must not duplicate refusal")
			msg := chatMessageJSON(t, ResponsesToChatCompletions(&resp, "gpt-5.5"))
			require.Equal(t, tc.expected, msg["refusal"])
			if tc.name == "text-only message" {
				require.Equal(t, "terminal text", msg["content"])
			}
			if tc.name == "tools-only" {
				require.Equal(t, "message", resp.Output[0].Type)
				require.Equal(t, "function_call", resp.Output[1].Type)
				require.Equal(t, "{}", resp.Output[1].Arguments)
			}
		})
	}
}

func TestResponsesRefusal_MixedOutputAndEmptyControls(t *testing.T) {
	var resp ResponsesResponse
	require.NoError(t, json.Unmarshal([]byte(`{"status":"completed","usage":{"input_tokens":10,"output_tokens":3},"output":[{"type":"message","content":[{"type":"output_text","text":"safe text"},{"type":"refusal","refusal":"not "},{"type":"refusal","refusal":"allowed"},{"type":"refusal","text":"wrong field"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"reason"}]},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}`), &resp))
	chat := ResponsesToChatCompletions(&resp, "gpt-5.5")
	msg := chatMessageJSON(t, chat)
	require.Equal(t, "not allowed", msg["refusal"])
	require.Equal(t, "safe text", msg["content"])
	require.Equal(t, "reason", msg["reasoning_content"])
	require.Equal(t, "tool_calls", chat.Choices[0].FinishReason)
	require.Equal(t, "call_1", chat.Choices[0].Message.ToolCalls[0].ID)
	require.Equal(t, 10, chat.Usage.PromptTokens)
	require.Equal(t, 3, chat.Usage.CompletionTokens)
	state := NewResponsesEventToChatState()
	empty := decodeResponsesEvent(t, `{"type":"response.refusal.delta","delta":""}`)
	require.Empty(t, ResponsesEventToChatChunks(&empty, state))
	acc := NewBufferedResponseAccumulator()
	acc.ProcessEvent(&empty)
	require.False(t, acc.HasContent())
	acc.SupplementResponseOutput(nil)
	require.Empty(t, acc.BuildOutput())
	plain := &ResponsesResponse{Status: "completed"}
	require.NotContains(t, chatMessageJSON(t, ResponsesToChatCompletions(plain, "gpt-5.5")), "refusal")
}
