package openai_ws_v2

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWSImageInputOutput_ActualCompletedProductWithoutUsage(t *testing.T) {
	for _, tc := range []struct {
		name, event, status, output string
		generated                   bool
	}{
		{"actual_image", "response.completed", "completed", `[{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, true},
		{"done_alias", "response.done", "completed", `[{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, true},
		{"incomplete_completed_product", "response.incomplete", "incomplete", `[{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, true},
		{"incomplete_unfinished_item", "response.incomplete", "incomplete", `[{"type":"image_generation_call","status":"in_progress","result":"opaque-base64"}]`, false},
		{"incomplete_empty_output", "response.incomplete", "incomplete", `[]`, false},
		{"done_alias_unfinished", "response.done", "in_progress", `[{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, false},
		{"missing_outer_status", "response.completed", "", `[{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, true},
		{"missing_item_status", "response.completed", "completed", `[{"type":"image_generation_call","result":"opaque-base64"}]`, true},
		{"cancelled_item", "response.completed", "completed", `[{"type":"image_generation_call","status":"cancelled","result":"opaque-base64"}]`, false},
		{"image_after_text", "response.completed", "completed", `[{"type":"message","content":[]},{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, true},
		{"failed_terminal", "response.failed", "failed", `[{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, false},
		{"failed_response", "response.completed", "failed", `[{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, false},
		{"failed_item", "response.completed", "completed", `[{"type":"image_generation_call","status":"failed","result":"opaque-base64"}]`, false},
		{"in_progress_item", "response.completed", "completed", `[{"type":"image_generation_call","status":"in_progress","result":"opaque-base64"}]`, false},
		{"empty_result", "response.completed", "completed", `[{"type":"image_generation_call","status":"completed","result":""}]`, false},
		{"blank_result", "response.completed", "completed", `[{"type":"image_generation_call","status":"completed","result":"  "}]`, false},
		{"null_result", "response.completed", "completed", `[{"type":"image_generation_call","status":"completed","result":null}]`, false},
		{"numeric_result", "response.completed", "completed", `[{"type":"image_generation_call","status":"completed","result":1}]`, false},
		{"no_product", "response.completed", "completed", `[]`, false},
		{"ordinary_message", "response.completed", "completed", `[{"type":"message","status":"completed","result":"image_generation_call"}]`, false},
		{"nonterminal_item", "response.output_item.done", "completed", `[{"type":"image_generation_call","status":"completed","result":"opaque-base64"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"type":%q,"response":{"id":"response_product","status":%q,"output":%s,"usage":{"input_tokens":2,"output_tokens":1}}}`, tc.event, tc.status, tc.output))
			if tc.status == "" {
				body = []byte(fmt.Sprintf(`{"type":%q,"response":{"id":"response_product","output":%s,"usage":{"input_tokens":2,"output_tokens":1}}}`, tc.event, tc.output))
			}
			now := time.Now()
			state := &relayState{}
			observed := observeUpstreamMessage(state, body, now, func() time.Time { return now }, nil)
			require.Equal(t, tc.generated, observed.hasGeneratedImage)
			if !observed.terminal {
				return
			}
			var turns []RelayTurnResult
			emitTurnComplete(func(turn RelayTurnResult) { turns = append(turns, turn) }, state, observed)
			require.Len(t, turns, 1)
			require.Equal(t, tc.generated, turns[0].HasGeneratedImage)
			require.Equal(t, 2, turns[0].Usage.InputTokens)
			require.Equal(t, 1, turns[0].Usage.OutputTokens)
			require.Zero(t, turns[0].Usage.ImageInputTokens)
			require.Zero(t, turns[0].Usage.ImageOutputTokens, "output observation must never fabricate charged counters")
		})
	}
}
