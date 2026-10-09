package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func ownedMessageStateEvent(t *testing.T, acc *BufferedResponseAccumulator, raw string) *ResponsesStreamEvent {
	t.Helper()
	var e ResponsesStreamEvent
	require.NoError(t, json.Unmarshal([]byte(raw), &e))
	acc.ProcessEvent(&e)
	return &e
}

func TestOwnedResponsesMessage_StateIdentity(t *testing.T) {
	t.Run("repeat_build_and_unexposed_late_id", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		ownedMessageStateEvent(t, acc, `{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`)
		id := acc.BuildOutputForStatus("completed")[0].ID
		require.NotEmpty(t, id)
		require.Equal(t, id, acc.BuildOutputForStatus("completed")[0].ID)
		ownedMessageStateEvent(t, acc, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_late","status":"in_progress"}}`)
		require.Equal(t, "msg_late", acc.BuildOutputForStatus("completed")[0].ID)
	})
	t.Run("queued_terminal_locks_generated_id", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		ownedMessageStateEvent(t, acc, `{"type":"response.output_text.delta","delta":"hello"}`)
		items := acc.BuildOutputForStatus("completed")
		id := items[0].ID
		acc.CommitWireEvent(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Output: items}})
		ownedMessageStateEvent(t, acc, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_conflict","status":"in_progress"}}`)
		require.Equal(t, id, acc.BuildOutputForStatus("completed")[0].ID)
		require.Len(t, acc.BuildOutput(), 1)
	})
	t.Run("queued_provider_added_id_survives_conflict_and_duplicate_terminal", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		first := ownedMessageStateEvent(t, acc, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_first","status":"in_progress"}}`)
		acc.CommitWireEvent(first)
		ownedMessageStateEvent(t, acc, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_first","delta":"hello"}`)
		ownedMessageStateEvent(t, acc, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_conflict","status":"in_progress"}}`)
		resp := &ResponsesResponse{Status: "completed"}
		acc.SupplementResponseOutput(resp)
		acc.SupplementResponseOutput(resp)
		require.Len(t, resp.Output, 1)
		require.Equal(t, "msg_first", resp.Output[0].ID)
		require.Equal(t, "msg_first", acc.BuildOutputForStatus("completed")[0].ID)
	})
	t.Run("terminal_context_and_done_authority", func(t *testing.T) {
		acc := NewBufferedResponseAccumulator()
		ownedMessageStateEvent(t, acc, `{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`)
		require.Equal(t, "in_progress", acc.BuildOutput()[0].Status)
		ownedMessageStateEvent(t, acc, `{"type":"response.incomplete","response":{"status":"completed","output":[]}}`)
		require.Equal(t, "incomplete", acc.BuildOutputForStatus("completed")[0].Status)
		ownedMessageStateEvent(t, acc, `{"type":"response.output_item.done","output_index":0,"item":{"type":"message","status":"completed","content":[{"type":"output_text","text":"done"}]}}`)
		require.Equal(t, "completed", acc.BuildOutputForStatus("incomplete")[0].Status)
		require.Equal(t, "done", acc.BuildOutput()[0].Content[0].Text)
	})
}
