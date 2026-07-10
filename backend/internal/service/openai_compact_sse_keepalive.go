package service

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const openAICompactSSEKeepaliveKey = "openai_compact_sse_keepalive"

// openAICompactSSEKeepalive writes ignorable SSE comments while a unary compact
// request is pending, so downstream proxies do not timeout an otherwise silent
// long-running request.
type openAICompactSSEKeepalive struct {
	mu      sync.Mutex
	writer  gin.ResponseWriter
	started bool
	stopped bool
	bytes   int
	stop    chan struct{}
}

// StartOpenAICompactSSEKeepalive starts an SSE keepalive only for body-signalled
// compact streams. The first beat is delayed so fast failures retain their HTTP
// status-code response.
func StartOpenAICompactSSEKeepalive(c *gin.Context, interval time.Duration) func() {
	if c == nil || c.Writer == nil || interval <= 0 || !openAICompactClientWantsStream(c) {
		return func() {}
	}
	k := &openAICompactSSEKeepalive{writer: c.Writer, stop: make(chan struct{})}
	c.Set(openAICompactSSEKeepaliveKey, k)
	c.Writer = &openAICompactKeepaliveWriter{ResponseWriter: c.Writer, k: k}

	var requestDone <-chan struct{}
	if c.Request != nil {
		requestDone = c.Request.Context().Done()
	}
	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-k.stop:
				return
			case <-requestDone:
				return
			case <-timer.C:
			}
			if !k.beat() {
				return
			}
			timer.Reset(interval)
		}
	}()
	return k.Stop
}

func (k *openAICompactSSEKeepalive) beat() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		return false
	}
	if !k.started {
		header := k.writer.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		k.writer.WriteHeader(http.StatusOK)
		k.started = true
	}
	n, err := k.writer.Write([]byte(": keepalive\n\n"))
	k.bytes += n
	if err != nil {
		k.stopped = true
		return false
	}
	k.writer.Flush()
	return true
}

func (k *openAICompactSSEKeepalive) Stop() {
	k.mu.Lock()
	k.markStoppedLocked()
	k.mu.Unlock()
}

func (k *openAICompactSSEKeepalive) markStoppedLocked() {
	if k.stopped {
		return
	}
	k.stopped = true
	close(k.stop)
}

// StopOpenAICompactSSEKeepaliveCommitted stops the current compact keepalive
// and reports whether it already committed a 200 SSE response.
func StopOpenAICompactSSEKeepaliveCommitted(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	if !ok {
		return false
	}
	k, ok := value.(*openAICompactSSEKeepalive)
	if !ok || k == nil {
		return false
	}
	k.mu.Lock()
	k.markStoppedLocked()
	committed := k.started
	k.mu.Unlock()
	return committed
}

// OpenAICompactKeepaliveAdjustedWrittenSize excludes keepalive comment bytes
// from handler failover decisions. A response containing only comments is
// equivalent to gin's unwritten sentinel (-1).
func OpenAICompactKeepaliveAdjustedWrittenSize(c *gin.Context) int {
	if c == nil || c.Writer == nil {
		return -1
	}
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	if !ok {
		return c.Writer.Size()
	}
	k, ok := value.(*openAICompactSSEKeepalive)
	if !ok || k == nil {
		return c.Writer.Size()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	size := k.writer.Size()
	if size < 0 {
		return size
	}
	if real := size - k.bytes; real > 0 {
		return real
	}
	return -1
}

// openAICompactKeepaliveWriter serializes response construction with the
// keepalive goroutine. Reads stay non-destructive so status snapshots do not
// prematurely stop the heartbeat.
type openAICompactKeepaliveWriter struct {
	gin.ResponseWriter
	k *openAICompactSSEKeepalive
}

func (w *openAICompactKeepaliveWriter) suspend() { w.k.Stop() }

func (w *openAICompactKeepaliveWriter) Header() http.Header {
	w.suspend()
	return w.ResponseWriter.Header()
}

func (w *openAICompactKeepaliveWriter) Write(data []byte) (int, error) {
	w.suspend()
	return w.ResponseWriter.Write(data)
}

func (w *openAICompactKeepaliveWriter) WriteString(s string) (int, error) {
	w.suspend()
	return w.ResponseWriter.WriteString(s)
}

func (w *openAICompactKeepaliveWriter) WriteHeader(code int) {
	w.suspend()
	w.ResponseWriter.WriteHeader(code)
}

func (w *openAICompactKeepaliveWriter) WriteHeaderNow() {
	w.suspend()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *openAICompactKeepaliveWriter) Flush() {
	w.suspend()
	w.ResponseWriter.Flush()
}

func (w *openAICompactKeepaliveWriter) Status() int {
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Status()
}

func (w *openAICompactKeepaliveWriter) Size() int {
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Size()
}

func (w *openAICompactKeepaliveWriter) Written() bool {
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Written()
}
