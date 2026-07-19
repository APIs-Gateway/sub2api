package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type passthroughLifecycleTestFrameConn struct {
	frames    chan []byte
	writes    chan []byte
	closed    chan struct{}
	writeErr  error
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
	if c.writeErr != nil {
		return c.writeErr
	}
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

func TestOpenAIWSPassthroughDeadlineAndLifecycleHelpers(t *testing.T) {
	var nilLifecycle *openAIWSPassthroughTurnLifecycle
	require.False(t, nilLifecycle.beginResponseCreate(nil))
	nilLifecycle.cancelResponseCreate()
	nilLifecycle.beginTerminalWrite()
	nilLifecycle.finishTerminalWrite(true, nil)

	lifecycle := newOpenAIWSPassthroughTurnLifecycle(false)
	require.True(t, lifecycle.beginResponseCreate(nil))
	require.False(t, lifecycle.beginResponseCreate(nil))
	lifecycle.cancelResponseCreate()
	require.True(t, lifecycle.beginResponseCreate(nil))
	lifecycle.beginTerminalWrite()
	lifecycle.finishTerminalWrite(false, nil)

	firstOutputErr := &openAIWSPassthroughFirstOutputTimeoutError{}
	require.Equal(t, "openai websocket passthrough first output timeout", firstOutputErr.Error())
	require.ErrorIs(t, firstOutputErr, errOpenAIWSPassthroughFirstOutputTimeout)
	activeErr := &openAIWSPassthroughActiveTurnTimeoutError{}
	require.Equal(t, "openai websocket passthrough active turn read timeout", activeErr.Error())
	require.ErrorIs(t, activeErr, errOpenAIWSPassthroughActiveTurnTimeout)

	var nilConn *openAIWSPassthroughFirstOutputFrameConn
	_, _, err := nilConn.ReadFrame(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.ErrorIs(t, nilConn.WriteFrame(context.Background(), coderws.MessageText, nil), errOpenAIWSConnClosed)
	require.NoError(t, nilConn.Close())

	conn := newPassthroughLifecycleTestFrameConn()
	wrapper := &openAIWSPassthroughFirstOutputFrameConn{inner: conn}
	require.Equal(t, uint64(0), wrapper.armDeadline([]byte(`{"type":"response.create"}`)))
	wrapper.armActiveReadDeadline()
	wrapper.observeUpstreamActivity(coderws.MessageText, []byte(`{"type":"response.output_text.delta"}`))
	wrapper.notifyDeadlineChanged()
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"noop"}`)))
	conn.frames <- []byte(`{"type":"response.created"}`)
	_, _, err = wrapper.ReadFrame(context.TODO())
	require.NoError(t, err)

	conn = newPassthroughLifecycleTestFrameConn()
	wrapper = &openAIWSPassthroughFirstOutputFrameConn{
		inner:             conn,
		activeReadTimeout: time.Second,
		deadlineChanged:   make(chan struct{}, 1),
		now:               time.Now,
		resolveDeadline: func([]byte) openAIWSPassthroughFirstOutputDeadline {
			return openAIWSPassthroughFirstOutputDeadline{timeout: time.Second}
		},
	}
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	state := wrapper.deadlineState()
	require.True(t, state.armed)
	wrapper.disarmDeadline(state.generation + 1)
	require.True(t, wrapper.deadlineState().armed)
	conn.writeErr = errors.New("write failed")
	require.Error(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	require.False(t, wrapper.deadlineState().armed)

	wrapper.observeUpstreamActivity(coderws.MessageText, []byte(`{"type":"response.output_text.delta"}`))
	state = wrapper.deadlineState()
	require.True(t, state.armed)
	wrapper.observeUpstreamActivity(coderws.MessageText, []byte(`{"type":"response.done"}`))
	require.False(t, wrapper.deadlineState().armed)

	var nilClient *openAIWSClientFrameConn
	nilClient.markTurnStarted()
	nilClient.markTurnCompleted()
	_, _, err = nilClient.ReadFrame(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	client := &openAIWSClientFrameConn{}
	client.markTurnCompleted()
	client.markTurnStarted()
	client.interTurnStarted = make(chan struct{}, 1)
	client.interTurnStarted <- struct{}{}
	client.markTurnCompleted()
	require.Len(t, client.interTurnStarted, 1)

	service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIWS: config.GatewayOpenAIWSConfig{IngressInterTurnIdleTimeoutSeconds: 7}}}}
	require.Equal(t, 7*time.Second, service.openAIWSIngressInterTurnIdleTimeout())
	require.Zero(t, (*OpenAIGatewayService)(nil).openAIWSIngressInterTurnIdleTimeout())
	require.Zero(t, (&OpenAIGatewayService{}).openAIWSIngressInterTurnIdleTimeout())
}

func TestOpenAIWSClientFrameConn_DelayedInterTurnTimeout(t *testing.T) {
	serverReady := make(chan *openAIWSClientFrameConn, 1)
	serverResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverResult <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		frameConn := &openAIWSClientFrameConn{
			conn:                 conn,
			interTurnIdleTimeout: 25 * time.Millisecond,
			interTurnStarted:     make(chan struct{}, 1),
			controlCtx:           context.Background(),
		}
		serverReady <- frameConn
		_, _, err = frameConn.ReadFrame(context.Background())
		serverResult <- err
	}))
	defer server.Close()

	conn, _, err := coderws.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	frameConn := <-serverReady
	frameConn.markTurnCompleted()

	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, _, err = conn.Read(readCtx)
	cancel()
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusNormalClosure, closeErr.Code)

	select {
	case readErr := <-serverResult:
		var serverCloseErr *OpenAIWSClientCloseError
		require.ErrorAs(t, readErr, &serverCloseErr)
	case <-time.After(time.Second):
		t.Fatal("client frame reader did not exit after inter-turn timeout")
	}
}

func TestOpenAIWSPassthroughEventClassification(t *testing.T) {
	for _, eventType := range []string{
		"response.completed", "response.done", "response.failed", "response.incomplete",
		"response.cancelled", "response.canceled",
	} {
		payload := []byte(`{"type":"` + eventType + `"}`)
		require.True(t, openAIWSPassthroughStartsSemanticOutput(payload), eventType)
		require.True(t, openAIWSPassthroughIsTerminalOutput(payload), eventType)
	}
	for _, eventType := range []string{"", "response.created", "response.in_progress", "response.output_item.added", "response.output_item.done", "response.other"} {
		payload := []byte(`{"type":"` + eventType + `"}`)
		require.False(t, openAIWSPassthroughStartsSemanticOutput(payload), eventType)
		require.False(t, openAIWSPassthroughIsTerminalOutput(payload), eventType)
	}
	for _, eventType := range []string{"response.function_call_arguments.delta", "response.output_text.done", "response.output_audio.delta"} {
		require.True(t, openAIWSPassthroughStartsSemanticOutput([]byte(`{"type":"`+eventType+`"}`)), eventType)
	}
	require.False(t, openAIWSPassthroughStartsSemanticOutput([]byte(`not-json`)))
}
