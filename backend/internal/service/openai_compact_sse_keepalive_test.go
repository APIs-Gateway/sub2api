package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type compactKeepaliveFailWriter struct {
	gin.ResponseWriter
	err               error
	bytesBeforeError  int
	writesBeforeError int
	writeSignal       chan struct{}
	once              sync.Once
}

func (w *compactKeepaliveFailWriter) Write(data []byte) (int, error) {
	w.writesBeforeError++
	if w.writeSignal != nil {
		w.once.Do(func() { close(w.writeSignal) })
	}
	return w.bytesBeforeError, w.err
}

type compactKeepaliveSignalWriter struct {
	gin.ResponseWriter
	flushed chan struct{}
	once    sync.Once
}

func (w *compactKeepaliveSignalWriter) Flush() {
	w.ResponseWriter.Flush()
	w.once.Do(func() { close(w.flushed) })
}

func startCompactKeepaliveForTest(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *openAICompactSSEKeepalive) {
	t.Helper()
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	t.Cleanup(stop)
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	require.True(t, ok)
	keepalive, ok := value.(*openAICompactSSEKeepalive)
	require.True(t, ok)
	return c, rec, keepalive
}

func TestStartOpenAICompactSSEKeepalive_NoOpsForNilDisabledAndUnmarkedContexts(t *testing.T) {
	stop := StartOpenAICompactSSEKeepalive(nil, time.Second)
	stop()
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(nil))
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(nil))
	_, hasOpsError := GetOpsStreamError(nil)
	require.False(t, hasOpsError)

	bareContext := &gin.Context{}
	stop = StartOpenAICompactSSEKeepalive(bareContext, time.Second)
	stop()
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(bareContext))

	for _, tt := range []struct {
		name       string
		markClient bool
		interval   time.Duration
	}{
		{name: "unmarked client", interval: time.Second},
		{name: "disabled interval", markClient: true, interval: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newCompactBridgeTestContext(t, tt.markClient)
			stop := StartOpenAICompactSSEKeepalive(c, tt.interval)
			stop()
			_, registered := c.Get(openAICompactSSEKeepaliveKey)
			require.False(t, registered)
			_, wrapped := c.Writer.(*openAICompactKeepaliveWriter)
			require.False(t, wrapped)
			require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
			require.Empty(t, rec.Body.String())
		})
	}

	c, _ := newCompactBridgeTestContext(t, false)
	c.Set(openAICompactSSEKeepaliveKey, "wrong-type")
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

func TestStartOpenAICompactSSEKeepalive_WritesFirstDelayedBeat(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	signal := &compactKeepaliveSignalWriter{
		ResponseWriter: c.Writer,
		flushed:        make(chan struct{}),
	}
	c.Writer = signal
	stop := StartOpenAICompactSSEKeepalive(c, time.Millisecond)
	t.Cleanup(stop)

	select {
	case <-signal.flushed:
	case <-time.After(time.Second):
		t.Fatal("first compact SSE keepalive was not flushed")
	}

	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.Equal(t, http.StatusOK, c.Writer.Status())
	require.Equal(t, ": keepalive\n\n", rec.Body.String())
}

func TestStartOpenAICompactSSEKeepalive_RequestCancellationStopsBeforeFirstBeat(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = c.Request.WithContext(requestContext)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	t.Cleanup(stop)
	cancel()

	time.Sleep(10 * time.Millisecond)
	require.Equal(t, "", rec.Body.String(), "a cancelled request must not commit a delayed heartbeat")
	require.False(t, c.Writer.Written())
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
}

func TestOpenAICompactSSEKeepalive_FirstBeatAndCommittedStop(t *testing.T) {
	c, rec, keepalive := startCompactKeepaliveForTest(t)

	require.True(t, keepalive.beat())
	require.Equal(t, http.StatusOK, c.Writer.Status())
	require.Equal(t, "text/event-stream", keepalive.writer.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", keepalive.writer.Header().Get("Cache-Control"))
	require.Equal(t, "keep-alive", keepalive.writer.Header().Get("Connection"))
	require.Equal(t, "no", keepalive.writer.Header().Get("X-Accel-Buffering"))
	require.Equal(t, ": keepalive\n\n", rec.Body.String())
	require.Equal(t, len(": keepalive\n\n"), keepalive.bytes)

	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.False(t, keepalive.beat())
	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

func TestOpenAICompactSSEKeepalive_BeatWriteFailureStopsAndRetainsPartialSize(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	failing := &compactKeepaliveFailWriter{
		ResponseWriter:   c.Writer,
		err:              errors.New("downstream write failed"),
		bytesBeforeError: 3,
	}
	c.Writer = failing
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	t.Cleanup(stop)
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	require.True(t, ok)
	keepalive, ok := value.(*openAICompactSSEKeepalive)
	require.True(t, ok)

	require.False(t, keepalive.beat())
	require.Equal(t, 1, failing.writesBeforeError)
	require.Equal(t, 3, keepalive.bytes)
	require.True(t, keepalive.stopped)
	require.Equal(t, "text/event-stream", failing.Header().Get("Content-Type"))
	require.False(t, keepalive.beat())
}

func TestStartOpenAICompactSSEKeepalive_StopsTimerAfterWriteFailure(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	failing := &compactKeepaliveFailWriter{
		ResponseWriter:   c.Writer,
		err:              errors.New("downstream write failed"),
		bytesBeforeError: 3,
		writeSignal:      make(chan struct{}),
	}
	c.Writer = failing
	stop := StartOpenAICompactSSEKeepalive(c, time.Millisecond)
	t.Cleanup(stop)

	select {
	case <-failing.writeSignal:
	case <-time.After(time.Second):
		t.Fatal("timer did not attempt the compact keepalive write")
	}
	stop()
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	require.True(t, ok)
	keepalive, ok := value.(*openAICompactSSEKeepalive)
	require.True(t, ok)
	require.True(t, keepalive.stopped)
	require.Equal(t, 1, failing.writesBeforeError)
	require.False(t, keepalive.beat())
}

func TestOpenAICompactKeepaliveAdjustedWrittenSize_SeparatesHeartbeatFromApplicationBytes(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, false)
	_, err := c.Writer.WriteString("direct")
	require.NoError(t, err)
	require.Equal(t, len("direct"), OpenAICompactKeepaliveAdjustedWrittenSize(c))

	c, _ = newCompactBridgeTestContext(t, false)
	c.Set(openAICompactSSEKeepaliveKey, "wrong-type")
	_, err = c.Writer.WriteString("direct")
	require.NoError(t, err)
	require.Equal(t, len("direct"), OpenAICompactKeepaliveAdjustedWrittenSize(c))

	c, _, keepalive := startCompactKeepaliveForTest(t)
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.True(t, keepalive.beat())
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	_, err = c.Writer.WriteString("application-event")
	require.NoError(t, err)
	require.Equal(t, len("application-event"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
}

func TestOpenAICompactKeepaliveWriter_SuspendsApplicationWritesAndKeepsSnapshotsReadOnly(t *testing.T) {
	c, _, keepalive := startCompactKeepaliveForTest(t)
	writer, ok := c.Writer.(*openAICompactKeepaliveWriter)
	require.True(t, ok)
	require.Equal(t, http.StatusOK, writer.Status())
	require.Equal(t, -1, writer.Size())
	require.False(t, writer.Written())
	require.False(t, keepalive.stopped)

	tests := []struct {
		name string
		run  func(t *testing.T, writer *openAICompactKeepaliveWriter)
	}{
		{
			name: "header",
			run: func(t *testing.T, writer *openAICompactKeepaliveWriter) {
				writer.Header().Set("X-Application", "started")
				require.Equal(t, "started", writer.Header().Get("X-Application"))
			},
		},
		{
			name: "write",
			run: func(t *testing.T, writer *openAICompactKeepaliveWriter) {
				n, err := writer.Write([]byte("body"))
				require.NoError(t, err)
				require.Equal(t, len("body"), n)
			},
		},
		{
			name: "write string",
			run: func(t *testing.T, writer *openAICompactKeepaliveWriter) {
				n, err := writer.WriteString("body")
				require.NoError(t, err)
				require.Equal(t, len("body"), n)
			},
		},
		{
			name: "write header",
			run: func(t *testing.T, writer *openAICompactKeepaliveWriter) {
				writer.WriteHeader(http.StatusAccepted)
				require.Equal(t, http.StatusAccepted, writer.Status())
			},
		},
		{
			name: "write header now",
			run: func(t *testing.T, writer *openAICompactKeepaliveWriter) {
				writer.WriteHeaderNow()
				require.True(t, writer.Written())
			},
		},
		{
			name: "flush",
			run: func(t *testing.T, writer *openAICompactKeepaliveWriter) {
				writer.Flush()
				require.True(t, writer.Written())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, keepalive := startCompactKeepaliveForTest(t)
			writer, ok := c.Writer.(*openAICompactKeepaliveWriter)
			require.True(t, ok)

			tt.run(t, writer)
			require.True(t, keepalive.stopped)
			require.False(t, keepalive.beat())
		})
	}
}

func TestOpsStreamError_PreservesFirstFailureAndRejectsInvalidValues(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	MarkOpsStreamError(nil, "ignored", "ignored", http.StatusTeapot)
	MarkOpsStreamError(c, "upstream_error", "first failure", http.StatusBadGateway)
	MarkOpsStreamError(c, "upstream_error", "later failure", http.StatusServiceUnavailable)

	opsError, recorded := GetOpsStreamError(c)
	require.True(t, recorded)
	require.Equal(t, "upstream_error", opsError.ErrType)
	require.Equal(t, "first failure", opsError.Message)
	require.Equal(t, http.StatusBadGateway, opsError.IntendedStatus)

	c.Set(OpsStreamErrorKey, "wrong-type")
	_, recorded = GetOpsStreamError(c)
	require.False(t, recorded)
}
