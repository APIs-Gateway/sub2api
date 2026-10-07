package service

import (
	"context"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// Unchanged adapter/frame APIs, copied verbatim onto OLD production for RED.
func TestWSEmptyTTFTFirstOutputDeadlineSurvivesEmptyFrames(t *testing.T) {
	for _, data := range []string{
		`{"type":"response.output_text.delta","delta":"","SSE-Keep-Alive":true}`,
		`{"type":"response.output_text.delta"}`,
		`{"type":"response.function_call_arguments.delta","delta":null}`,
		`{"type":"response.function_call_arguments.delta","delta":3}`,
		`{"type":"response.output_text.delta","delta":{}}`,
	} {
		t.Run(data, func(t *testing.T) {
			conn := newPassthroughLifecycleTestFrameConn()
			wrapper := &openAIWSPassthroughFirstOutputFrameConn{inner: conn, deadlineChanged: make(chan struct{}, 1), now: time.Now, resolveDeadline: func([]byte) openAIWSPassthroughFirstOutputDeadline {
				return openAIWSPassthroughFirstOutputDeadline{timeout: 200 * time.Millisecond}
			}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			require.NoError(t, wrapper.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create"}`)))
			conn.frames <- []byte(data)
			_, got, err := wrapper.ReadFrame(ctx)
			require.NoError(t, err)
			require.Equal(t, data, string(got), "transport frame must still reach client")
			_, _, err = wrapper.ReadFrame(ctx)
			var firstOutputErr *openAIWSPassthroughFirstOutputTimeoutError
			require.ErrorAs(t, err, &firstOutputErr, "empty frame must not disarm first-output waiting")
		})
	}
}
func TestWSEmptyTTFTRealContentAndTerminalKeepDeadlineContract(t *testing.T) {
	for _, data := range []string{
		`{"type":"response.output_text.delta","delta":" "}`,
		`{"type":"response.function_call_arguments.delta","delta":"{}"}`,
		`{"type":"response.completed","response":{"id":"done","usage":{"input_tokens":1,"output_tokens":1}}}`,
	} {
		t.Run(data, func(t *testing.T) {
			conn := newPassthroughLifecycleTestFrameConn()
			wrapper := &openAIWSPassthroughFirstOutputFrameConn{inner: conn, deadlineChanged: make(chan struct{}, 1), now: time.Now, resolveDeadline: func([]byte) openAIWSPassthroughFirstOutputDeadline {
				return openAIWSPassthroughFirstOutputDeadline{timeout: 200 * time.Millisecond}
			}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			require.NoError(t, wrapper.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create"}`)))
			conn.frames <- []byte(data)
			_, got, err := wrapper.ReadFrame(ctx)
			require.NoError(t, err)
			require.Equal(t, data, string(got))
			short, stop := context.WithTimeout(ctx, 30*time.Millisecond)
			defer stop()
			_, _, err = wrapper.ReadFrame(short)
			require.ErrorIs(t, err, context.DeadlineExceeded, "real content/terminal with no active-read timer must not retain first-output timeout")
		})
	}
}
