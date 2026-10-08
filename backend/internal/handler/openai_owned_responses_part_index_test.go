//go:build unit

package handler

import (
	"testing"

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
				{name: "empty_terminal_matches_before_part_insertion", wire: emptyMessage},
			} {
				t.Run(output.name, func(t *testing.T) {
					rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, second, first, ownedMessageTerminal("response.completed", "completed", output.wire)), "text/event-stream")
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
}
