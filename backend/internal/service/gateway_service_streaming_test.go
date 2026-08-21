package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type upstreamContextTestKey string

func newStreamingResponseTestGatewayService() *GatewayService {
	return &GatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				StreamDataIntervalTimeout: 0,
				MaxLineSize:               defaultMaxLineSize,
			},
		},
		rateLimitService: &RateLimitService{},
	}
}

func TestGatewayService_StreamingReusesScannerBufferAndStillParsesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		// Minimal SSE event to trigger parseSSEUsage
		_, _ = pw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 7, result.usage.OutputTokens)
}

func TestGatewayService_StreamingKeepaliveUsesIdleTimer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()
	svc.cfg.Gateway.StreamKeepaliveInterval = 1

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: ping")
}

func TestGatewayService_StreamingKeepaliveUsesNoopDeltaForAffectedClaudeCodeVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()
	svc.cfg.Gateway.StreamKeepaliveInterval = 1

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.198 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: content_block_delta")
	require.Contains(t, body, `"delta":{"type":"text_delta","text":""}`)
}

func TestGatewayService_StreamingKeepaliveUsesNoopDeltaDuringToolUseForAffectedClaudeCodeVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()
	svc.cfg.Gateway.StreamKeepaliveInterval = 1

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.198 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Edit\",\"input\":{}}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: content_block_delta")
	require.Contains(t, body, `"index":1`)
	require.Contains(t, body, `"delta":{"type":"input_json_delta","partial_json":""}`)
}

func TestGatewayService_StreamingKeepaliveKeepsPingForOlderClaudeCodeVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()
	svc.cfg.Gateway.StreamKeepaliveInterval = 1

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.187 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: ping")
	require.NotContains(t, body, `"delta":{"type":"text_delta","text":""}`)
}

func TestDetachUpstreamContextIgnoresClientCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), upstreamContextTestKey("test-key"), "test-value"))
	upstreamCtx, release := detachUpstreamContext(parent)
	defer release()

	cancel()

	require.NoError(t, upstreamCtx.Err())
	require.Equal(t, "test-value", upstreamCtx.Value(upstreamContextTestKey("test-key")))
}

func TestShouldUseClaudeCodeNoopDeltaKeepalive(t *testing.T) {
	require.False(t, shouldUseClaudeCodeNoopDeltaKeepalive(""), "no CLI version in UA must not opt in")
	require.False(t, shouldUseClaudeCodeNoopDeltaKeepalive("curl/8.0.0"), "non-Claude-Code UA must not opt in")
	require.False(t, shouldUseClaudeCodeNoopDeltaKeepalive("claude-cli/2.1.187 (external, cli)"), "below threshold must not opt in")
	require.True(t, shouldUseClaudeCodeNoopDeltaKeepalive("claude-cli/2.1.193 (external, cli)"), "exact threshold must opt in")
	require.True(t, shouldUseClaudeCodeNoopDeltaKeepalive("claude-cli/2.2.0 (external, cli)"), "above threshold must opt in")
}

func TestClaudeCodeKeepaliveDeltaTypeForContentBlock(t *testing.T) {
	require.Equal(t, "text_delta", claudeCodeKeepaliveDeltaTypeForContentBlock("text"))
	require.Equal(t, "input_json_delta", claudeCodeKeepaliveDeltaTypeForContentBlock("tool_use"))
	require.Equal(t, "thinking_delta", claudeCodeKeepaliveDeltaTypeForContentBlock("thinking"))
	require.Equal(t, "", claudeCodeKeepaliveDeltaTypeForContentBlock("redacted_thinking"), "unknown block types must not get a noop delta")
}

func TestClaudeCodeKeepaliveFieldForDeltaType(t *testing.T) {
	require.Equal(t, "text", claudeCodeKeepaliveFieldForDeltaType("text_delta"))
	require.Equal(t, "partial_json", claudeCodeKeepaliveFieldForDeltaType("input_json_delta"))
	require.Equal(t, "thinking", claudeCodeKeepaliveFieldForDeltaType("thinking_delta"))
	require.Equal(t, "", claudeCodeKeepaliveFieldForDeltaType("signature_delta"), "unmapped delta types must not get a noop field")
}

func TestBuildClaudeCodeNoopDeltaKeepalive(t *testing.T) {
	block, ok := buildClaudeCodeNoopDeltaKeepalive(2, "text_delta")
	require.True(t, ok)
	require.Contains(t, block, `"index":2`)
	require.Contains(t, block, `"delta":{"type":"text_delta","text":""}`)

	_, ok = buildClaudeCodeNoopDeltaKeepalive(0, "signature_delta")
	require.False(t, ok, "delta types with no known field must not build a keepalive block")
}

func TestSSEEventIndex(t *testing.T) {
	idx, ok := sseEventIndex(map[string]any{"index": float64(3)})
	require.True(t, ok)
	require.Equal(t, 3, idx)

	idx, ok = sseEventIndex(map[string]any{"index": int(4)})
	require.True(t, ok)
	require.Equal(t, 4, idx)

	idx, ok = sseEventIndex(map[string]any{"index": int64(5)})
	require.True(t, ok)
	require.Equal(t, 5, idx)

	idx, ok = sseEventIndex(map[string]any{"index": json.Number("6")})
	require.True(t, ok)
	require.Equal(t, 6, idx)

	_, ok = sseEventIndex(map[string]any{"index": json.Number("not-a-number")})
	require.False(t, ok, "unparseable json.Number must report ok=false")

	_, ok = sseEventIndex(map[string]any{"index": "7"})
	require.False(t, ok, "unsupported types must report ok=false")

	_, ok = sseEventIndex(map[string]any{})
	require.False(t, ok, "missing index key must report ok=false")
}

// gatewayStreamFailAfterWritesRecorder is a minimal http.ResponseWriter whose
// Write fails once writes >= failAfterWrites, simulating a client that
// disconnects partway through a stream.
type gatewayStreamFailAfterWritesRecorder struct {
	header          http.Header
	mu              sync.Mutex
	writes          int
	failAfterWrites int
}

func newGatewayStreamFailAfterWritesRecorder(failAfterWrites int) *gatewayStreamFailAfterWritesRecorder {
	return &gatewayStreamFailAfterWritesRecorder{header: make(http.Header), failAfterWrites: failAfterWrites}
}

func (w *gatewayStreamFailAfterWritesRecorder) Header() http.Header { return w.header }

func (w *gatewayStreamFailAfterWritesRecorder) WriteHeader(int) {}

func (w *gatewayStreamFailAfterWritesRecorder) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writes >= w.failAfterWrites {
		return 0, errors.New("client disconnected")
	}
	w.writes++
	return len(data), nil
}

func (w *gatewayStreamFailAfterWritesRecorder) Flush() {}

// 客户端从第一次写出就断开(failAfterWrites=0)；后续事件(含终态 usage)仍必须继续
// 被解析并合并进最终结果用于计费，而不能因为写失败就整体丢弃剩余用量。
func TestGatewayService_StreamingKeepsMergingUsageAfterClientWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()

	rec := newGatewayStreamFailAfterWritesRecorder(0)
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
		_, _ = pw.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.clientDisconnect)
	require.NotNil(t, result.usage)
	require.Equal(t, 5, result.usage.InputTokens, "usage from the event delivered at disconnect time must still be counted")
	require.Equal(t, 9, result.usage.OutputTokens, "usage from events after the failed write must still be merged, not dropped")
}
