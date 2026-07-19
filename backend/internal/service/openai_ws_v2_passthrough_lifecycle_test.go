package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type passthroughLifecycleTestFrameConn struct {
	frames    chan []byte
	writes    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func newPassthroughLifecycleTestFrameConn() *passthroughLifecycleTestFrameConn {
	return &passthroughLifecycleTestFrameConn{
		frames: make(chan []byte, 4),
		writes: make(chan []byte, 4),
		closed: make(chan struct{}),
	}
}

func (c *passthroughLifecycleTestFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	case <-c.closed:
		return coderws.MessageText, nil, errors.New("frame connection closed")
	case payload := <-c.frames:
		return coderws.MessageText, payload, nil
	}
}

func (c *passthroughLifecycleTestFrameConn) WriteFrame(ctx context.Context, _ coderws.MessageType, payload []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errors.New("frame connection closed")
	case c.writes <- append([]byte(nil), payload...):
		return nil
	}
}

func (c *passthroughLifecycleTestFrameConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

var _ openaiwsv2.FrameConn = (*passthroughLifecycleTestFrameConn)(nil)

func TestOpenAIWSPassthroughTurnLifecycleSerializesTerminalCommit(t *testing.T) {
	client := &openAIWSClientFrameConn{interTurnStarted: make(chan struct{}, 1)}
	client.markTurnCompleted()
	lifecycle := newOpenAIWSPassthroughTurnLifecycle(true)
	lifecycle.beginTerminalWrite()

	admitted := make(chan bool, 1)
	go func() { admitted <- lifecycle.beginResponseCreate(client.markTurnStarted) }()
	select {
	case <-admitted:
		t.Fatal("next response.create was admitted before terminal write completed")
	case <-time.After(20 * time.Millisecond):
	}

	lifecycle.finishTerminalWrite(true, client.markTurnCompleted)
	require.True(t, <-admitted)
	require.False(t, client.waitingForNextTurn.Load())

	lifecycle = newOpenAIWSPassthroughTurnLifecycle(true)
	lifecycle.beginTerminalWrite()
	admitted = make(chan bool, 1)
	go func() { admitted <- lifecycle.beginResponseCreate(nil) }()
	lifecycle.finishTerminalWrite(false, func() { t.Fatal("failed terminal write released the turn") })
	require.False(t, <-admitted)
}

func TestOpenAIWSPassthroughFirstOutputTimeoutTransitionsToActiveRead(t *testing.T) {
	conn := newPassthroughLifecycleTestFrameConn()
	wrapper := &openAIWSPassthroughFirstOutputFrameConn{
		inner:             conn,
		activeReadTimeout: 35 * time.Millisecond,
		deadlineChanged:   make(chan struct{}, 1),
		now:               time.Now,
		resolveDeadline: func([]byte) openAIWSPassthroughFirstOutputDeadline {
			return openAIWSPassthroughFirstOutputDeadline{timeout: 20 * time.Millisecond}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, wrapper.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create"}`)))

	_, _, err := wrapper.ReadFrame(ctx)
	var firstOutputErr *openAIWSPassthroughFirstOutputTimeoutError
	require.ErrorAs(t, err, &firstOutputErr)

	conn = newPassthroughLifecycleTestFrameConn()
	wrapper.inner = conn
	require.NoError(t, wrapper.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create"}`)))
	conn.frames <- []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	_, _, err = wrapper.ReadFrame(ctx)
	require.NoError(t, err)
	_, _, err = wrapper.ReadFrame(ctx)
	var activeErr *openAIWSPassthroughActiveTurnTimeoutError
	require.ErrorAs(t, err, &activeErr)
}

func TestOpenAIWSPassthroughTerminalDisarmsActiveReadTimeout(t *testing.T) {
	conn := newPassthroughLifecycleTestFrameConn()
	wrapper := &openAIWSPassthroughFirstOutputFrameConn{
		inner:             conn,
		activeReadTimeout: 20 * time.Millisecond,
		deadlineChanged:   make(chan struct{}, 1),
		resolveDeadline: func([]byte) openAIWSPassthroughFirstOutputDeadline {
			return openAIWSPassthroughFirstOutputDeadline{timeout: time.Second}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, wrapper.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create"}`)))
	conn.frames <- []byte(`{"type":"response.completed","response":{"id":"resp_1"}}`)
	_, _, err := wrapper.ReadFrame(ctx)
	require.NoError(t, err)

	shortCtx, cancelShort := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelShort()
	_, _, err = wrapper.ReadFrame(shortCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestOpenAIWSPassthroughSemanticAndTerminalEventClassification(t *testing.T) {
	require.False(t, openAIWSPassthroughStartsSemanticOutput([]byte(`{"type":"response.created"}`)))
	require.True(t, openAIWSPassthroughStartsSemanticOutput([]byte(`{"type":"response.output_text.delta"}`)))
	require.True(t, openAIWSPassthroughStartsSemanticOutput([]byte(`{"type":"response.done"}`)))
	require.False(t, openAIWSPassthroughIsTerminalOutput([]byte(`{"type":"response.output_text.delta"}`)))
	require.True(t, openAIWSPassthroughIsTerminalOutput([]byte(`{"type":"response.cancelled"}`)))
}

func TestOpenAIWSPassthroughRelayClientCloseMapping(t *testing.T) {
	closeErr := NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "blocked", nil)
	status, reason, ok := openAIWSPassthroughRelayClientClose(openaiwsv2.RelayExit{Err: closeErr}, 0)
	require.Equal(t, coderws.StatusPolicyViolation, status)
	require.Equal(t, "blocked", reason)
	require.True(t, ok)

	status, reason, ok = openAIWSPassthroughRelayClientClose(openaiwsv2.RelayExit{
		Stage: "read_upstream",
		Err:   errors.New("broken upstream"),
	}, 0)
	require.Equal(t, coderws.StatusInternalError, status)
	require.Equal(t, "upstream websocket proxy failed", reason)
	require.True(t, ok)

	status, reason, ok = openAIWSPassthroughRelayClientClose(openaiwsv2.RelayExit{
		Err: &openAIWSPassthroughFirstOutputTimeoutError{},
	}, 0)
	require.Zero(t, status)
	require.Empty(t, reason)
	require.False(t, ok)

	status, reason, ok = openAIWSPassthroughRelayClientClose(openaiwsv2.RelayExit{
		Err:             &openAIWSPassthroughFirstOutputTimeoutError{},
		WroteDownstream: true,
	}, 1)
	require.Equal(t, coderws.StatusGoingAway, status)
	require.Equal(t, "upstream produced no semantic output; please reconnect", reason)
	require.True(t, ok)
}
