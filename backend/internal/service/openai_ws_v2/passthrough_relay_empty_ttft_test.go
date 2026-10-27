package openai_ws_v2

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Uses unchanged relay observation APIs so the exact same fixture can prove RED.
func TestWSEmptyTTFTEmptyEventsAndTerminalUsage(t *testing.T) {
	for _, data := range []string{
		`{"type":"response.output_text.delta","delta":"","SSE-Keep-Alive":true}`,
		`{"type":"response.output_text.delta"}`,
		`{"type":"response.function_call_arguments.delta","delta":null}`,
		`{"type":"response.output_text.delta","delta":{}}`,
		`{"type":"response.output_text.delta","delta":3}`,
		`{"type":"response.output_text.done","text":""}`,
		`{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`,
	} {
		t.Run(data, func(t *testing.T) {
			start := time.Unix(100, 0)
			state := &relayState{}
			observeUpstreamMessage(state, []byte(`{"type":"response.created","response":{"id":"empty"}}`), start, func() time.Time { return start }, nil)
			observeUpstreamMessage(state, []byte(data), start, func() time.Time { return start.Add(time.Second) }, nil)
			terminal := observeUpstreamMessage(state, []byte(`{"type":"response.completed","response":{"id":"empty","output":[{"type":"message","content":[{"type":"output_text","text":"terminal snapshot"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`), start, func() time.Time { return start.Add(2 * time.Second) }, nil)
			require.Nil(t, state.firstTokenMs)
			require.Nil(t, terminal.firstToken)
			require.Equal(t, Usage{InputTokens: 2, OutputTokens: 1}, terminal.usage)
			require.Equal(t, Usage{InputTokens: 2, OutputTokens: 1}, state.usage)
		})
	}
}
func TestWSEmptyTTFTIDLessContentEachTurn(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	state := &relayState{}
	observe := func(data string) observedUpstreamEvent {
		return observeUpstreamMessage(state, []byte(data), start, func() time.Time { return now }, nil)
	}
	for _, id := range []string{"first", "second"} {
		observe(`{"type":"response.created","response":{"id":"` + id + `"}}`)
		now = now.Add(time.Second)
		observe(`{"type":"response.output_text.delta","delta":""}`)
		require.Nil(t, state.activeTurn.firstTokenMs)
		now = now.Add(time.Second)
		observe(`{"type":"response.output_text.delta","delta":" "}`)
		require.NotNil(t, state.activeTurn.firstTokenMs)
		require.Equal(t, 2000, *state.activeTurn.firstTokenMs)
		now = now.Add(time.Second)
		observe(`{"type":"response.function_call_arguments.delta","delta":"{}"}`)
		terminal := observe(`{"type":"response.completed","response":{"id":"` + id + `","usage":{"input_tokens":2,"output_tokens":1}}}`)
		require.NotNil(t, terminal.firstToken)
		require.Equal(t, 2000, *terminal.firstToken)
	}
	require.Equal(t, 2000, *state.firstTokenMs)
	require.Equal(t, Usage{InputTokens: 4, OutputTokens: 2}, state.usage)
}
func TestWSEmptyTTFTExplicitOldIDDoesNotStartCurrentTurn(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	state := &relayState{}
	observe := func(data string) {
		observeUpstreamMessage(state, []byte(data), start, func() time.Time { return now }, nil)
	}
	observe(`{"type":"response.created","response":{"id":"old"}}`)
	// Bare errors clear active lifecycle but preserve its ID timing for a trailing terminal.
	observe(`{"type":"error","error":{"message":"old failed"}}`)
	now = now.Add(time.Second)
	observe(`{"type":"response.created","response":{"id":"current"}}`)
	current := state.activeTurn
	now = now.Add(time.Second)
	observe(`{"type":"response.output_text.delta","response_id":"old","delta":"old content"}`)
	require.Nil(t, current.firstTokenMs)
	require.Equal(t, 2000, *state.turnTimingByID["old"].firstTokenMs)
	now = now.Add(time.Second)
	observe(`{"type":"response.output_text.delta","response_id":"current","delta":"new content"}`)
	require.Equal(t, 2000, *current.firstTokenMs)
	now = now.Add(time.Second)
	observe(`{"type":"response.output_text.delta","response_id":"current","delta":"later content"}`)
	require.Same(t, current, state.activeTurn)
	require.Equal(t, 2000, *current.firstTokenMs)
	require.Equal(t, 2000, *state.firstTokenMs)
}
