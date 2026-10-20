package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type imageKeepaliveFailWriter struct {
	gin.ResponseWriter
	err              error
	bytesBeforeError int
	writeSignal      chan struct{}
	once             sync.Once
}

func (w *imageKeepaliveFailWriter) Write(data []byte) (int, error) {
	if w.writeSignal != nil {
		w.once.Do(func() { close(w.writeSignal) })
	}
	return w.bytesBeforeError, w.err
}

func startImageKeepaliveForTest(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *openAIImagesJSONKeepalive) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	stop := StartOpenAIImagesJSONKeepalive(c, time.Hour)
	t.Cleanup(stop)
	k := openAIImagesJSONKeepaliveFromContext(c)
	require.NotNil(t, k)
	return c, rec, k
}

func TestStartOpenAIImagesJSONKeepalive_NoOpsForNilDisabledAndWrongContexts(t *testing.T) {
	stop := StartOpenAIImagesJSONKeepalive(nil, time.Second)
	stop()
	require.False(t, StopOpenAIImagesJSONKeepaliveCommitted(nil))
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(nil))

	bareContext := &gin.Context{}
	stop = StartOpenAIImagesJSONKeepalive(bareContext, time.Second)
	stop()
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(bareContext))

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	stop = StartOpenAIImagesJSONKeepalive(c, 0)
	stop()
	_, registered := c.Get(openAIImagesJSONKeepaliveKey)
	require.False(t, registered)
	require.Empty(t, rec.Body.String())

	c.Set(openAIImagesJSONKeepaliveKey, "wrong-type")
	require.False(t, StopOpenAIImagesJSONKeepaliveCommitted(c))
}

func TestOpenAIImagesJSONKeepalive_RequestCancellationStopsBeforeFirstBeat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(requestContext)
	stop := StartOpenAIImagesJSONKeepalive(c, time.Hour)
	t.Cleanup(stop)
	cancel()

	time.Sleep(10 * time.Millisecond)
	require.Empty(t, rec.Body.String())
	require.False(t, c.Writer.Written())
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
}

func TestOpenAIImagesJSONKeepalive_FirstBeatAndCommittedStop(t *testing.T) {
	c, rec, keepalive := startImageKeepaliveForTest(t)

	require.True(t, keepalive.beat())
	require.Equal(t, http.StatusOK, c.Writer.Status())
	require.Equal(t, "application/json; charset=utf-8", keepalive.writer.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", keepalive.writer.Header().Get("Cache-Control"))
	require.Equal(t, "no", keepalive.writer.Header().Get("X-Accel-Buffering"))
	require.Equal(t, " \n", rec.Body.String())
	require.Equal(t, len(" \n"), keepalive.bytes)

	require.True(t, StopOpenAIImagesJSONKeepaliveCommitted(c))
	require.False(t, keepalive.beat())
	require.True(t, StopOpenAIImagesJSONKeepaliveCommitted(c))
}

func TestOpenAIImagesJSONKeepalive_BeatWriteFailureStops(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	failing := &imageKeepaliveFailWriter{
		ResponseWriter:   c.Writer,
		err:              errors.New("downstream write failed"),
		bytesBeforeError: 3,
	}
	c.Writer = failing
	stop := StartOpenAIImagesJSONKeepalive(c, time.Hour)
	t.Cleanup(stop)
	keepalive := openAIImagesJSONKeepaliveFromContext(c)
	require.NotNil(t, keepalive)

	require.False(t, keepalive.beat())
	require.Equal(t, 3, keepalive.bytes)
	require.True(t, keepalive.stopped)
	require.False(t, keepalive.beat())
}

func TestOpenAIImagesJSONKeepalive_StopsTimerAfterWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	failing := &imageKeepaliveFailWriter{
		ResponseWriter: c.Writer,
		err:            errors.New("downstream write failed"),
		writeSignal:    make(chan struct{}),
	}
	c.Writer = failing
	stop := StartOpenAIImagesJSONKeepalive(c, time.Millisecond)
	t.Cleanup(stop)

	select {
	case <-failing.writeSignal:
	case <-time.After(time.Second):
		t.Fatal("timer did not attempt the image keepalive write")
	}
	require.True(t, openAIImagesJSONKeepaliveFromContext(c).stopped)
}

func TestOpenAIImagesJSONKeepaliveAdjustedWrittenSize_SeparatesHeartbeatFromApplicationBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	_, err := c.Writer.WriteString("direct")
	require.NoError(t, err)
	require.Equal(t, len("direct"), OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))

	c.Set(openAIImagesJSONKeepaliveKey, "wrong-type")
	require.Equal(t, len("direct"), OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))

	c, _, keepalive := startImageKeepaliveForTest(t)
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
	require.True(t, keepalive.beat())
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
	_, err = c.Writer.WriteString("application-json")
	require.NoError(t, err)
	require.Equal(t, len("application-json"), OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
}

func TestOpenAIImagesJSONKeepaliveWriter_SuspendsApplicationWritesAndSnapshots(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, writer *openAIImagesJSONKeepaliveWriter)
	}{
		{
			name: "header",
			run: func(t *testing.T, writer *openAIImagesJSONKeepaliveWriter) {
				writer.Header().Set("X-Application", "started")
				require.Equal(t, "started", writer.Header().Get("X-Application"))
			},
		},
		{
			name: "write",
			run: func(t *testing.T, writer *openAIImagesJSONKeepaliveWriter) {
				n, err := writer.Write([]byte("body"))
				require.NoError(t, err)
				require.Equal(t, len("body"), n)
			},
		},
		{
			name: "write string",
			run: func(t *testing.T, writer *openAIImagesJSONKeepaliveWriter) {
				n, err := writer.WriteString("body")
				require.NoError(t, err)
				require.Equal(t, len("body"), n)
			},
		},
		{
			name: "write header",
			run: func(t *testing.T, writer *openAIImagesJSONKeepaliveWriter) {
				writer.WriteHeader(http.StatusAccepted)
				require.Equal(t, http.StatusAccepted, writer.Status())
			},
		},
		{
			name: "write header now",
			run: func(t *testing.T, writer *openAIImagesJSONKeepaliveWriter) {
				writer.WriteHeaderNow()
				require.True(t, writer.Written())
			},
		},
		{
			name: "flush",
			run: func(t *testing.T, writer *openAIImagesJSONKeepaliveWriter) {
				writer.Flush()
				require.True(t, writer.Written())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, keepalive := startImageKeepaliveForTest(t)
			writer, ok := c.Writer.(*openAIImagesJSONKeepaliveWriter)
			require.True(t, ok)
			require.Equal(t, http.StatusOK, writer.Status())
			require.Equal(t, -1, writer.Size())
			require.False(t, writer.Written())

			tt.run(t, writer)
			require.True(t, keepalive.stopped)
			require.False(t, keepalive.beat())
		})
	}
}

func TestOpenAIImagesJSONKeepalive_PreservesValidJSONResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	originalWriter := c.Writer

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	waitForOpenAIImagesJSONKeepalive(t, c)
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))

	c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"b64_json": "aW1hZ2U="}}})
	stop()

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.True(t, rec.Flushed)
	require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
	require.Equal(t, "aW1hZ2U=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Greater(t, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c), 0)
	require.Same(t, originalWriter, c.Writer)
}

func TestOpenAIImagesJSONKeepalive_DisabledIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	originalWriter := c.Writer

	stop := StartOpenAIImagesJSONKeepalive(c, 0)
	stop()
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid request"}})

	require.Same(t, originalWriter, c.Writer)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid request", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestOpenAIImagesJSONKeepalive_FastErrorPreservesStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, time.Second)
	wrote := writeOpenAIImagesUpstreamErrorResponse(c, &OpenAIImagesUpstreamError{
		StatusCode: http.StatusBadRequest,
		ErrorType:  "invalid_request_error",
		Message:    "invalid size",
	})
	stop()

	require.True(t, wrote)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, strings.HasPrefix(rec.Body.String(), " \n"))
	require.Equal(t, "invalid size", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestOpenAIImagesJSONKeepalive_LateErrorRemainsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	defer stop()
	waitForOpenAIImagesJSONKeepalive(t, c)

	wrote := writeOpenAIImagesUpstreamErrorResponse(c, &OpenAIImagesUpstreamError{
		StatusCode: http.StatusBadRequest,
		ErrorType:  "image_generation_user_error",
		Code:       "moderation_blocked",
		Message:    "request rejected",
	})

	require.True(t, wrote)
	require.Equal(t, http.StatusOK, rec.Code, "heartbeat already committed the status")
	require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
	require.Equal(t, "moderation_blocked", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, "request rejected", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestOpenAIImagesJSONKeepalive_DoesNotBlockFailoverDetection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	waitForOpenAIImagesJSONKeepalive(t, c)

	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
	require.True(t, c.Writer.Written())
	stop()
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
	require.True(t, strings.TrimSpace(rec.Body.String()) == "")
}

func TestOpenAIImagesJSONKeepalive_KeepsOAuthNonStreamResponseValid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	reader, writer := io.Pipe()
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(writer,
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\",\"output_format\":\"png\"}]}}\n\n"+
				"data: [DONE]\n\n",
		)
		_ = writer.Close()
	}()

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}
	svc := &OpenAIGatewayService{}
	_, imageCount, _, err := svc.handleOpenAIImagesOAuthNonStreamingResponse(resp, c, "b64_json", "gpt-image-2")
	stop()

	require.NoError(t, err)
	require.Equal(t, 1, imageCount)
	require.True(t, rec.Flushed)
	require.True(t, strings.HasPrefix(rec.Body.String(), " \n"), rec.Body.String())
	require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
	require.Equal(t, "aW1hZ2U=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

func TestOpenAIImagesJSONKeepaliveWriter_NilGuards(t *testing.T) {
	w := &openAIImagesJSONKeepaliveWriter{}
	require.NotPanics(t, func() {
		require.NotNil(t, w.Header())
		_, _ = w.Write([]byte("test"))
		_, _ = w.WriteString("test")
		w.WriteHeader(http.StatusOK)
		w.WriteHeaderNow()
		w.Flush()
		require.Equal(t, 0, w.Status())
		require.Equal(t, 0, w.Size())
		require.False(t, w.Written())
		require.Nil(t, w.Pusher())
	})

	conn, _, err := w.Hijack()
	require.Error(t, err)
	require.Nil(t, conn)
	select {
	case <-w.CloseNotify():
	default:
		t.Fatal("nil writer CloseNotify channel should be closed")
	}
}

func waitForOpenAIImagesJSONKeepalive(t *testing.T, c *gin.Context) {
	t.Helper()
	k := openAIImagesJSONKeepaliveFromContext(c)
	require.NotNil(t, k)
	require.Eventually(t, func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return k.started
	}, time.Second, time.Millisecond)
}
