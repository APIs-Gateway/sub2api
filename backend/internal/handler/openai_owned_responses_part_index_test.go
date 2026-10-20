//go:build unit

package handler

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Same-candidate proof: faff741 already owns message IDs and fields. These
// public requests isolate out-of-order content parts from the earlier OLD main
// defect. Repository/usage controls remain mocks, not funding evidence.
func TestOwnedResponsesPartIndex_PublicHTTP(t *testing.T) {
	added := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_parts","role":"assistant","status":"in_progress","content":[]}}`
	second := `{"type":"response.output_text.delta","output_index":0,"content_index":1,"item_id":"msg_parts","delta":"B"}`
	first := `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_parts","delta":"A"}`
	emptyMessage := `[{"type":"message","id":"msg_parts","status":"completed","role":"assistant","content":[]}]`
	for _, mode := range []string{"normal-stream", "normal-unary", "passthrough-unary"} {
		t.Run(mode, func(t *testing.T) {
			for _, output := range []struct {
				name string
				wire string
			}{
				{name: "empty_output_orders_explicit_parts", wire: "[]"},
				{name: "nonempty_terminal_output_keeps_raw_empty_content", wire: emptyMessage},
			} {
				t.Run(output.name, func(t *testing.T) {
					rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, second, first, ownedMessageTerminal("response.completed", "completed", output.wire)), "text/event-stream")
					if output.wire != "[]" {
						// These native consumers preserve any nonempty terminal
						// output; only converted consumers supplement its parts.
						require.JSONEq(t, emptyMessage, ownedMessageFinals(t, rec, mode)[0].Get("output").Raw)
						return
					}
					requireOwnedMessageWire(t, ownedMessageFinals(t, rec, mode)[0].Get("output.0"), "msg_parts", "completed", "A", "B")
				})
			}
			t.Run("complete_raw_terminal_is_authoritative", func(t *testing.T) {
				item := `{"type":"message","id":"msg_parts","status":"completed","role":"assistant","content":[{"type":"output_text","text":"AUTHORITATIVE","annotations":null,"logprobs":null,"vendor":9007199254740993}]}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, second, first, ownedMessageTerminal("response.completed", "completed", "["+item+"]")), "text/event-stream")
				require.Equal(t, item, ownedMessageFinals(t, rec, mode)[0].Get("output.0").Raw)
			})
		})
	}
	for _, route := range []string{"chat/completions", "messages"} {
		t.Run("converted_"+route+"_empty_terminal_matches_before_part_insertion", func(t *testing.T) {
			rec, _, _ := ownedMessagePublicRequest(t, route, "normal-unary", ownedMessageSSE(added, second, first, ownedMessageTerminal("response.completed", "completed", emptyMessage)), "text/event-stream")
			text := gjson.Get(rec.Body.String(), "choices.0.message.content").String()
			if route == "messages" {
				text = ""
				for _, part := range gjson.Get(rec.Body.String(), "content").Array() {
					if part.Get("type").String() == "text" {
						text += part.Get("text").String()
					}
				}
			}
			require.Equal(t, "AB", text)
		})
	}
	for _, kind := range []string{"build_explicit_parts", "supplement_empty_terminal", "supplement_partial_terminal"} {
		t.Run("public_accumulator_"+kind, func(t *testing.T) {
			acc := apicompat.NewBufferedResponseAccumulator()
			frames := []string{added, second, first}
			texts := []string{"A", "B"}
			if kind == "supplement_partial_terminal" {
				third := `{"type":"response.output_text.delta","output_index":0,"content_index":2,"item_id":"msg_parts","delta":"C"}`
				frames = []string{added, third, first, second}
				texts = []string{"A", "B", "C"}
			}
			for _, frame := range frames {
				var event apicompat.ResponsesStreamEvent
				require.NoError(t, json.Unmarshal([]byte(frame), &event))
				acc.ProcessEvent(&event)
			}
			response := &apicompat.ResponsesResponse{Status: "completed"}
			if kind == "build_explicit_parts" {
				response.Output = acc.BuildOutputForStatus("completed")
			} else {
				response.Output = []apicompat.ResponsesOutput{{Type: "message", ID: "msg_parts", Role: "assistant", Status: "completed"}}
				if kind == "supplement_partial_terminal" {
					response.Output[0].Content = []apicompat.ResponsesContentPart{{Type: "output_text", Text: ""}}
				}
				acc.SupplementResponseOutput(response)
			}
			wire, err := json.Marshal(response)
			require.NoError(t, err)
			requireOwnedMessageWire(t, gjson.ParseBytes(wire).Get("output.0"), "msg_parts", "completed", texts...)
		})
	}
	t.Run("public_accumulator_reversed_mixed_parts_keep_opaque_metadata", func(t *testing.T) {
		acc := apicompat.NewBufferedResponseAccumulator()
		for _, frame := range []string{
			`{"type":"response.content_part.added","output_index":0,"content_index":2,"item_id":"msg_parts","part":{"type":"output_text","text":"","vendor":9007199254740993}}`,
			`{"type":"response.refusal.delta","output_index":0,"content_index":1,"item_id":"msg_parts","delta":"declined"}`,
			`{"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_parts","part":{"type":"vendor_media","payload":{"id":9223372036854775807}}}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":2,"item_id":"msg_parts","delta":"B"}`,
		} {
			var event apicompat.ResponsesStreamEvent
			require.NoError(t, json.Unmarshal([]byte(frame), &event))
			acc.ProcessEvent(&event)
		}
		wire, err := json.Marshal(acc.BuildOutputForStatus("completed"))
		require.NoError(t, err)
		parts := gjson.ParseBytes(wire).Get("0.content").Array()
		require.Len(t, parts, 3)
		require.Equal(t, "vendor_media", parts[0].Get("type").String())
		require.Equal(t, "9223372036854775807", parts[0].Get("payload.id").Raw)
		require.False(t, parts[0].Get("annotations").Exists())
		require.Equal(t, "refusal", parts[1].Get("type").String())
		require.Equal(t, "declined", parts[1].Get("refusal").String())
		require.False(t, parts[1].Get("annotations").Exists())
		require.Equal(t, "B", parts[2].Get("text").String())
		require.Equal(t, "9007199254740993", parts[2].Get("vendor").Raw)
		require.True(t, parts[2].Get("annotations").IsArray())
	})
	t.Run("public_accumulator_unindexed_legacy_keeps_its_arrival_slot", func(t *testing.T) {
		acc := apicompat.NewBufferedResponseAccumulator()
		for _, frame := range []string{
			`{"type":"response.output_text.delta","output_index":0,"content_index":2,"item_id":"msg_parts","delta":"C"}`,
			`{"type":"response.refusal.delta","output_index":0,"item_id":"msg_parts","delta":"legacy"}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_parts","delta":"A"}`,
		} {
			var event apicompat.ResponsesStreamEvent
			require.NoError(t, json.Unmarshal([]byte(frame), &event))
			acc.ProcessEvent(&event)
		}
		for repeat := 0; repeat < 2; repeat++ {
			wire, err := json.Marshal(acc.BuildOutputForStatus("completed"))
			require.NoError(t, err)
			parts := gjson.ParseBytes(wire).Get("0.content").Array()
			require.Len(t, parts, 3)
			require.Equal(t, "A", parts[0].Get("text").String())
			require.Equal(t, "legacy", parts[1].Get("refusal").String())
			require.Equal(t, "C", parts[2].Get("text").String())
		}
	})
}
