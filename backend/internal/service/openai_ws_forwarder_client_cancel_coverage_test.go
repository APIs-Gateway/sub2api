package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenAIWSClientCancelTestService(conn openAIWSClientConn, readTimeoutSeconds int) (*OpenAIGatewayService, *Account) {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = readTimeoutSeconds
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSClientConnCancelDialer{conn: conn})
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          9102,
		Name:        "openai-ws-client-cancel-coverage",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			"openai_responses_supported":                 true,
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
		},
	}
	return svc, account
}

func openAIWSClientCancelTestEvents() [][]byte {
	return [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_cancel_cov","model":"gpt-5.5"}}`),
		[]byte(`{"type":"response.output_text.delta","delta":"partial"}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_cancel_cov","model":"gpt-5.5","usage":{"input_tokens":3,"output_tokens":5}}}`),
	}
}

// Client cancels while forwardOpenAIWSV2 is blocked on an upstream read that
// still uses the request context. The canceled read is classified as a client
// disconnect (not an upstream failure) and retried with a detached context;
// as upstream, the lease is already marked broken at that point, so the retry
// ends the drain and Forward reports the incomplete stream as a cancellation.
func TestForwardOpenAIWSV2_CancelDuringInFlightReadIsClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)

	conn := &openAIWSCancelSafeConn{openAIWSCaptureConn: &openAIWSCaptureConn{
		events:     openAIWSClientCancelTestEvents(),
		readDelays: []time.Duration{0, 0, 300 * time.Millisecond},
	}}
	svc, account := newOpenAIWSClientCancelTestService(conn, 5)
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()

	_, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":[{"type":"input_text","text":"hello"}]}`))

	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, rec.Body.String(), "response.failed")
}

// openAIWSCancelAfterReadConn cancels the request context right after handing
// out the Nth event, i.e. between two upstream reads.
type openAIWSCancelAfterReadConn struct {
	*openAIWSCaptureConn
	cancelAfter int
	reads       int
	cancel      context.CancelFunc
}

func (c *openAIWSCancelAfterReadConn) ReadMessage(ctx context.Context) ([]byte, error) {
	event, err := c.openAIWSCaptureConn.ReadMessage(ctx)
	if err == nil {
		c.reads++
		if c.reads == c.cancelAfter && c.cancel != nil {
			c.cancel()
		}
	}
	return event, err
}

// A client that disconnects between reads of a non-streaming request: the
// upstream is drained to the terminal event with a detached context and the
// result is returned without writing the (unwanted) JSON body.
func TestForwardOpenAIWSV2_NonStreamClientDisconnectDrainsToTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)

	conn := &openAIWSCancelAfterReadConn{
		openAIWSCaptureConn: &openAIWSCaptureConn{events: openAIWSClientCancelTestEvents()},
		cancelAfter:         2,
		cancel:              cancel,
	}
	svc, account := newOpenAIWSClientCancelTestService(conn, 5)

	result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.5","stream":false,"input":[{"type":"input_text","text":"hello"}]}`))

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, "resp_cancel_cov", result.RequestID)
	require.NotContains(t, rec.Body.String(), "resp_cancel_cov")
}

type openAIWSFailingWriteResponseWriter struct {
	header http.Header
}

func (w *openAIWSFailingWriteResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *openAIWSFailingWriteResponseWriter) WriteHeader(int) {}

func (w *openAIWSFailingWriteResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("write: broken pipe")
}

func (w *openAIWSFailingWriteResponseWriter) Flush() {}

// A failed downstream write marks the client as disconnected; the upstream is
// still drained to the terminal event and the result carries its usage.
func TestForwardOpenAIWSV2_DownstreamWriteErrorDrainsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(&openAIWSFailingWriteResponseWriter{})
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	conn := &openAIWSCancelSafeConn{openAIWSCaptureConn: &openAIWSCaptureConn{
		events: openAIWSClientCancelTestEvents(),
	}}
	svc, account := newOpenAIWSClientCancelTestService(conn, 5)

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":[{"type":"input_text","text":"hello"}]}`))

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

// openAIWSContextIgnoringConn returns queued events after their delay even if
// the read context expires, so a single slow event can consume the whole
// post-disconnect drain budget.
type openAIWSContextIgnoringConn struct {
	*openAIWSCaptureConn
}

func (c *openAIWSContextIgnoringConn) ReadMessage(context.Context) ([]byte, error) {
	c.mu.Lock()
	if len(c.events) == 0 {
		c.mu.Unlock()
		return nil, io.EOF
	}
	delay := time.Duration(0)
	if len(c.readDelays) > 0 {
		delay = c.readDelays[0]
		c.readDelays = c.readDelays[1:]
	}
	event := c.events[0]
	c.events = c.events[1:]
	c.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return event, nil
}

// After the client disconnects, draining is bounded by the read timeout; once
// the budget is spent without a terminal event forwardOpenAIWSV2 reports an
// incomplete stream caused by the client cancellation.
func TestForwardOpenAIWSV2_DrainBudgetExhaustedAfterClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &cancelOnFirstWriteResponseWriter{cancel: cancel}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)

	conn := &openAIWSContextIgnoringConn{openAIWSCaptureConn: &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_drain_budget","model":"gpt-5.5"}}`),
			[]byte(`{"type":"response.output_text.delta","delta":"partial"}`),
			[]byte(`{"type":"response.output_text.delta","delta":" slow"}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_drain_budget","model":"gpt-5.5","usage":{"input_tokens":3,"output_tokens":5}}}`),
		},
		readDelays: []time.Duration{0, 0, 1200 * time.Millisecond, 0},
	}}
	svc, account := newOpenAIWSClientCancelTestService(conn, 1)

	result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":[{"type":"input_text","text":"hello"}]}`))

	// Forward drops the partial result on error (same as upstream); the
	// handler classifies the canceled request via failoverClientGone.
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	conn.mu.Lock()
	remaining := len(conn.events)
	conn.mu.Unlock()
	require.Equal(t, 1, remaining, "drain must stop before reading the terminal event")
}
