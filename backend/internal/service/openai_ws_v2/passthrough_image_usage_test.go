package openai_ws_v2

import (
	"context"
	"encoding/json"
	"fmt"
	coderws "github.com/coder/websocket"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Field lookup through the existing public JSON representation also makes this
// exact fixture runnable on the pre-fix relay, without new API compile failures.
func imageUsageFields(t *testing.T, usage Usage) map[string]int {
	t.Helper()
	body, err := json.Marshal(usage)
	require.NoError(t, err)
	var fields map[string]int
	require.NoError(t, json.Unmarshal(body, &fields))
	return fields
}

func TestWSImageInputUsage_PrecedenceAndPairedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, details, hosted, outer string
		in, out                      int
	}{
		{"direct", `"input_tokens_details":{"image_tokens":3},"output_tokens_details":{"image_tokens":4}`, ``, ``, 3, 4},
		{"prompt_completion", `"prompt_tokens_details":{"image_tokens":3},"completion_tokens_details":{"image_tokens":4}`, ``, ``, 3, 4},
		{"zero_falls_back", `"input_tokens_details":{"image_tokens":0},"prompt_tokens_details":{"image_tokens":3},"output_tokens_details":{"image_tokens":0},"completion_tokens_details":{"image_tokens":4}`, ``, ``, 3, 4},
		{"null_hosted", `"input_tokens_details":{"image_tokens":null}`, `,"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":5},"output_tokens_details":{"image_tokens":6}}}`, ``, 5, 6},
		{"positive_wins", `"input_tokens_details":{"image_tokens":3},"output_tokens_details":{"image_tokens":4}`, `,"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":5},"output_tokens_details":{"image_tokens":6}}}`, ``, 3, 4},
		{"negative_matches_http", `"input_tokens_details":{"image_tokens":-3},"output_tokens_details":{"image_tokens":-4}`, `,"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":5},"output_tokens_details":{"image_tokens":6}}}`, ``, -3, -4},
		{"negative_hosted_ignored", `"input_tokens_details":{}`, `,"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":-5},"output_tokens_details":{"image_tokens":-6}}}`, ``, 0, 0},
		{"wrong_envelope_ignored", `"input_tokens_details":{}`, ``, `,"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":5},"output_tokens_details":{"image_tokens":6}}}`, 0, 0},
		{"hosted_not_object", `"input_tokens_details":{}`, `,"tool_usage":{"image_gen":null}`, ``, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &relayState{}
			body := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":10,%s}%s}%s}`, tc.details, tc.hosted, tc.outer))
			usage := parseUsageAndAccumulate(state, body, "response.completed", func(_, _ string) { t.Fatal("valid totals rejected") })
			fields := imageUsageFields(t, usage)
			require.Equal(t, tc.in, fields["ImageInputTokens"])
			require.Equal(t, tc.out, fields["ImageOutputTokens"])
			require.Equal(t, 20, usage.InputTokens)
			require.Equal(t, 10, usage.OutputTokens)
		})
	}
}

func TestWSImageInputUsage_TerminalTurnsAndSessionAggregate(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled"} {
		t.Run(terminal, func(t *testing.T) {
			state := &relayState{}
			body := []byte(fmt.Sprintf(`{"type":%q,"response":{"usage":{"input_tokens":9,"output_tokens":7,"input_tokens_details":{"image_tokens":4},"output_tokens_details":{"image_tokens":2}}}}`, terminal))
			first := parseUsageAndAccumulate(state, body, terminal, nil)
			require.Equal(t, 4, imageUsageFields(t, first)["ImageInputTokens"])
			second := parseUsageAndAccumulate(state, []byte(`{"response":{"usage":{"input_tokens":3,"output_tokens":1}}}`), terminal, nil)
			require.Zero(t, imageUsageFields(t, second)["ImageInputTokens"], "following text usage is not sticky")
			require.Equal(t, 4, imageUsageFields(t, state.usage)["ImageInputTokens"])
			require.Equal(t, 12, state.usage.InputTokens)
			require.Equal(t, 8, state.usage.OutputTokens)
			parseUsageAndAccumulate(state, body, "response.created", nil)
			require.Equal(t, 12, state.usage.InputTokens, "nonterminal usage remains excluded")
		})
	}
}

func TestWSImageInputUsage_InvalidTotalsDoNotPartiallyAccumulate(t *testing.T) {
	state := &relayState{}
	parseFailures := 0
	for _, body := range []string{`{"response":{"usage":null}}`, `{"response":{"usage":{"input_tokens":"9","output_tokens":1,"input_tokens_details":{"image_tokens":3}}}}`, `{"response":{"usage":{"input_tokens":9,"input_tokens_details":{"image_tokens":3}}}}`} {
		usage := parseUsageAndAccumulate(state, []byte(body), "response.completed", func(_, _ string) { parseFailures++ })
		require.Zero(t, usage.InputTokens)
		require.Zero(t, imageUsageFields(t, usage)["ImageInputTokens"])
	}
	require.Equal(t, 3, parseFailures)
	require.Zero(t, state.usage.InputTokens)
}

func TestWSImageInputUsage_RelayCallbackAndAggregate(t *testing.T) {
	client := newPassthroughTestFrameConn(nil, false)
	upstream := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_img","usage":{"input_tokens":3,"output_tokens":1,"input_tokens_details":{"image_tokens":2}}}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_text","usage":{"input_tokens":4,"output_tokens":1}}}`)},
	}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var turns []RelayTurnResult
	result, exit := Relay(ctx, client, upstream, []byte(`{"type":"response.create","model":"frozen","input":[]}`), RelayOptions{OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) }})
	require.Nil(t, exit)
	require.Len(t, turns, 2)
	require.Equal(t, 2, imageUsageFields(t, turns[0].Usage)["ImageInputTokens"])
	require.Zero(t, imageUsageFields(t, turns[1].Usage)["ImageInputTokens"])
	require.Equal(t, 2, imageUsageFields(t, result.Usage)["ImageInputTokens"])
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, "frozen", result.RequestModel)
}
