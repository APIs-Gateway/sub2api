package service

import (
	"bufio"
	"errors"
	"net"
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

// openAIStreamKeepaliveBytesKey 记录流式路径（Responses 主路径 / Anthropic 入站 / Chat 入站）
// 在等待上游期间按 gateway.stream_keepalive_interval 写给客户端的心跳字节数。
const openAIStreamKeepaliveBytesKey = "openai_stream_keepalive_bytes"

// openAIStreamKeepaliveBytes 是 per-context 心跳字节计数。主路径的心跳与流处理在同一
// goroutine，但 compact keepalive 有独立 goroutine，保守加锁。
type openAIStreamKeepaliveBytes struct {
	mu    sync.Mutex
	bytes int
}

// addOpenAIStreamKeepaliveBytes 在一次心跳（SSE 注释行 ":\n\n"、Anthropic ping 事件）
// 真正写到 ResponseWriter 之后累加其字节数。心跳是客户端会丢弃的非语义字节，
// 「只发过心跳」不等于「内容已交付」：OpenAICompactKeepaliveAdjustedWrittenSize 会把
// 这些字节从 gin 的已写大小里扣掉，使上游模型不一致等 pre-output failover 在心跳后仍可拦截并切号。
func addOpenAIStreamKeepaliveBytes(c *gin.Context, n int) {
	if c == nil || n <= 0 {
		return
	}
	var counter *openAIStreamKeepaliveBytes
	if value, ok := c.Get(openAIStreamKeepaliveBytesKey); ok {
		counter, _ = value.(*openAIStreamKeepaliveBytes)
	}
	if counter == nil {
		counter = &openAIStreamKeepaliveBytes{}
		c.Set(openAIStreamKeepaliveBytesKey, counter)
	}
	counter.mu.Lock()
	counter.bytes += n
	counter.mu.Unlock()
}

// openAIStreamKeepaliveBytesWritten 返回已累计的流式心跳字节数（未记录返回 0）。
func openAIStreamKeepaliveBytesWritten(c *gin.Context) int {
	if c == nil {
		return 0
	}
	value, ok := c.Get(openAIStreamKeepaliveBytesKey)
	if !ok {
		return 0
	}
	counter, ok := value.(*openAIStreamKeepaliveBytes)
	if !ok || counter == nil {
		return 0
	}
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.bytes
}

// OpenAICompactKeepaliveAdjustedWrittenSize excludes keepalive comment bytes
// from handler failover decisions. A response containing only comments is
// equivalent to gin's unwritten sentinel (-1).
//
// 扣除顺序：先扣 compact keepalive 的字节（openAICompactSSEKeepalive.bytes），再扣流式
// 心跳字节（addOpenAIStreamKeepaliveBytes）；扣完 <= 0 返回 -1。没有任何心跳时行为不变。
func OpenAICompactKeepaliveAdjustedWrittenSize(c *gin.Context) int {
	if c == nil || c.Writer == nil {
		return -1
	}
	size := openAICompactKeepaliveOnlyAdjustedWrittenSize(c)
	if size < 0 {
		return size
	}
	streamKeepalive := openAIStreamKeepaliveBytesWritten(c)
	if streamKeepalive <= 0 {
		return size
	}
	if real := size - streamKeepalive; real > 0 {
		return real
	}
	return -1
}

// openAICompactKeepaliveOnlyAdjustedWrittenSize 只扣 compact keepalive 字节（原有语义）。
func openAICompactKeepaliveOnlyAdjustedWrittenSize(c *gin.Context) int {
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

// suspend 停拍心跳；幂等。任何响应构造（含 Header 访问——写响应必先操作
// 响应头）都视为请求侧接管 ResponseWriter。
func (w *openAICompactKeepaliveWriter) suspend() {
	if w.k == nil {
		return
	}
	w.k.Stop()
}

func (w *openAICompactKeepaliveWriter) Header() http.Header {
	w.suspend()
	if w.ResponseWriter == nil {
		return http.Header{}
	}
	return w.ResponseWriter.Header()
}

func (w *openAICompactKeepaliveWriter) Write(data []byte) (int, error) {
	w.suspend()
	if w.ResponseWriter == nil {
		return 0, nil
	}
	return w.ResponseWriter.Write(data)
}

func (w *openAICompactKeepaliveWriter) WriteString(s string) (int, error) {
	w.suspend()
	if w.ResponseWriter == nil {
		return 0, nil
	}
	return w.ResponseWriter.WriteString(s)
}

func (w *openAICompactKeepaliveWriter) WriteHeader(code int) {
	w.suspend()
	if w.ResponseWriter == nil {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *openAICompactKeepaliveWriter) WriteHeaderNow() {
	w.suspend()
	if w.ResponseWriter == nil {
		return
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *openAICompactKeepaliveWriter) Flush() {
	w.suspend()
	if w.ResponseWriter == nil {
		return
	}
	w.ResponseWriter.Flush()
}

func (w *openAICompactKeepaliveWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.ResponseWriter == nil {
		return nil, nil, errors.New("response writer released")
	}
	return w.ResponseWriter.Hijack()
}

func (w *openAICompactKeepaliveWriter) CloseNotify() <-chan bool {
	if w.ResponseWriter == nil {
		ch := make(chan bool)
		close(ch)
		return ch
	}
	return w.ResponseWriter.CloseNotify()
}

func (w *openAICompactKeepaliveWriter) Pusher() http.Pusher {
	if w.ResponseWriter == nil {
		return nil
	}
	return w.ResponseWriter.Pusher()
}

func (w *openAICompactKeepaliveWriter) Status() int {
	if w.k == nil || w.ResponseWriter == nil {
		return 0
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Status()
}

func (w *openAICompactKeepaliveWriter) Size() int {
	if w.k == nil || w.ResponseWriter == nil {
		return 0
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Size()
}

func (w *openAICompactKeepaliveWriter) Written() bool {
	if w.k == nil || w.ResponseWriter == nil {
		return false
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Written()
}
