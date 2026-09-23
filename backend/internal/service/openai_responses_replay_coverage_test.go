package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const replayReasoningWithContent = `{"type":"reasoning","summary":[{"type":"summary_text","text":"keep"}],"content":[{"type":"reasoning_text","text":"remove"}]}`

func TestNormalizeOpenAIResponsesReasoningContentReplayEdgeCases(t *testing.T) {
	t.Run("skips reasoning items without content and strips the rest", func(t *testing.T) {
		body := []byte(`{"input":["raw",{"type":"reasoning","summary":[]},` + replayReasoningWithContent + `]}`)
		normalized, changed, err := normalizeOpenAIResponsesReasoningContentReplay(body)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "raw", gjson.GetBytes(normalized, "input.0").String())
		require.False(t, gjson.GetBytes(normalized, "input.1.content").Exists())
		require.False(t, gjson.GetBytes(normalized, "input.2.content").Exists())
		require.Equal(t, "keep", gjson.GetBytes(normalized, "input.2.summary.0.text").String())
	})

	t.Run("non-array input is untouched", func(t *testing.T) {
		body := []byte(`{"input":"hello"}`)
		normalized, changed, err := normalizeOpenAIResponsesReasoningContentReplay(body)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(body), string(normalized))
	})

	t.Run("trailing data is rejected", func(t *testing.T) {
		body := []byte(`{"input":[` + replayReasoningWithContent + `]} {}`)
		normalized, changed, err := normalizeOpenAIResponsesReasoningContentReplay(body)
		require.Error(t, err)
		require.False(t, changed)
		require.Equal(t, string(body), string(normalized))
	})

	// gjson reads the first duplicate key while encoding/json keeps the last
	// one, so these bodies pass the prescan but decode to a shape that needs no
	// change; the original body must be returned unchanged.
	t.Run("decoded input is not an array", func(t *testing.T) {
		body := []byte(`{"input":[` + replayReasoningWithContent + `],"input":"override"}`)
		normalized, changed, err := normalizeOpenAIResponsesReasoningContentReplay(body)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(body), string(normalized))
	})

	t.Run("decoded content is already empty", func(t *testing.T) {
		body := []byte(`{"input":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"x"}],"content":[]}]}`)
		normalized, changed, err := normalizeOpenAIResponsesReasoningContentReplay(body)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(body), string(normalized))
	})
}

func TestNormalizeOpenAIWSV2PassthroughReasoningContentFrameRejectsUndecodableFrame(t *testing.T) {
	frame := []byte(`{"type":"response.create","input":[` + replayReasoningWithContent + `]} {}`)
	out, err := normalizeOpenAIWSV2PassthroughReasoningContentFrame(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, coderws.MessageText, frame)
	require.Error(t, err)
	var closeErr *OpenAIWSClientCloseError
	require.True(t, errors.As(err, &closeErr))
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	require.Equal(t, string(frame), string(out))
}

func TestOpenAIGatewayServiceForwardRejectsUndecodableReasoningReplay(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"input":[` + replayReasoningWithContent + `]} {}`)
	upstream := &httpUpstreamRecorder{}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		newOpenAIRejectedFieldTestAccount(),
		body,
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "normalize OpenAI Responses reasoning content replay")
	require.Nil(t, result)
	require.Empty(t, upstream.bodies, "nothing may be sent upstream when normalization fails")
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodySkipsSameTypeItemsWithoutStatus(t *testing.T) {
	body := []byte(`{"input":[` +
		`{"type":"tool_search_output","call_id":"call_0","tools":[]},` +
		`"raw",` +
		`{"type":"tool_search_output","status":"completed","call_id":"call_2","tools":[]}` +
		`]}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[2].status'.","param":"input[2].status"}}`)

	retryBody, reason, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotEmpty(t, reason)
	require.False(t, gjson.GetBytes(retryBody, "input.0.status").Exists())
	require.Equal(t, "call_0", gjson.GetBytes(retryBody, "input.0.call_id").String())
	require.Equal(t, "raw", gjson.GetBytes(retryBody, "input.1").String())
	require.False(t, gjson.GetBytes(retryBody, "input.2.status").Exists())
}

// openAIWSReasoningReplayTestServer runs ProxyResponsesWebSocketFromClient for
// one client connection and reports its return value on the returned channel.
func openAIWSReasoningReplayTestServer(t *testing.T, svc *OpenAIGatewayService, account *Account) (*httptest.Server, chan error) {
	t.Helper()
	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "oauth-token", firstMessage, nil)
	}))
	return wsServer, serverErrCh
}

func openAIWSReasoningReplayTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	return cfg
}

func dialOpenAIWSReasoningReplayTestServer(t *testing.T, wsServer *httptest.Server) *coderws.Conn {
	t.Helper()
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	return clientConn
}

func writeOpenAIWSReasoningReplayFrame(t *testing.T, conn *coderws.Conn, msgType coderws.MessageType, payload string) {
	t.Helper()
	writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(writeCtx, msgType, []byte(payload)))
}

func waitOpenAIWSReasoningReplayServer(t *testing.T, serverErrCh chan error) error {
	t.Helper()
	select {
	case serverErr := <-serverErrCh:
		return serverErr
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket proxy to finish")
		return nil
	}
}

// The ctx_pool (non-passthrough) ingress path normalizes client payloads in
// parseClientPayload; reasoning.content must be stripped before the frame is
// written upstream.
func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CtxPoolStripsReasoningContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := openAIWSReasoningReplayTestConfig()

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.output_item.done","item":{"id":"msg_ctx_pool_reasoning","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_pool_reasoning","model":"gpt-5.1","output":[{"id":"msg_ctx_pool_reasoning","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: captureConn})
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          454,
		Name:        "openai-ingress-ctx-pool-reasoning",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra:       map[string]any{"openai_oauth_responses_websockets_v2_enabled": true},
	}

	wsServer, serverErrCh := openAIWSReasoningReplayTestServer(t, svc, account)
	defer wsServer.Close()
	clientConn := dialOpenAIWSReasoningReplayTestServer(t, wsServer)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeOpenAIWSReasoningReplayFrame(t, clientConn, coderws.MessageText,
		`{"type":"response.create","model":"gpt-5.1","stream":false,"input":[{"type":"message","role":"user","content":"hi"},`+replayReasoningWithContent+`]}`)
	var completed []byte
	for i := 0; i < 3 && completed == nil; i++ {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		_, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			require.NoError(t, readErr, "server error: %v", waitOpenAIWSReasoningReplayServer(t, serverErrCh))
		}
		if gjson.GetBytes(event, "type").String() == "response.completed" {
			completed = event
		}
	}
	require.Equal(t, "resp_ctx_pool_reasoning", gjson.GetBytes(completed, "response.id").String())
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")
	_ = waitOpenAIWSReasoningReplayServer(t, serverErrCh)

	require.NotEmpty(t, captureConn.writes)
	written := requestToJSONString(captureConn.writes[0])
	require.Equal(t, "reasoning", gjson.Get(written, "input.1.type").String())
	require.False(t, gjson.Get(written, "input.1.content").Exists())
	require.Equal(t, "keep", gjson.Get(written, "input.1.summary.0.text").String())
}

func newOpenAIWSPassthroughReasoningReplayTestService(upstreamConn *openAIWSCaptureConn) (*OpenAIGatewayService, *Account) {
	cfg := openAIWSReasoningReplayTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	svc := &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: &openAIWSCaptureDialer{conn: upstreamConn},
	}
	account := &Account{
		ID:          456,
		Name:        "openai-ingress-passthrough-reasoning-invalid",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra:       map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough},
	}
	return svc, account
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughRejectsUndecodableFirstFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamConn := &openAIWSCaptureConn{}
	svc, account := newOpenAIWSPassthroughReasoningReplayTestService(upstreamConn)

	wsServer, serverErrCh := openAIWSReasoningReplayTestServer(t, svc, account)
	defer wsServer.Close()
	clientConn := dialOpenAIWSReasoningReplayTestServer(t, wsServer)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeOpenAIWSReasoningReplayFrame(t, clientConn, coderws.MessageText,
		`{"type":"response.create","model":"gpt-5.6-sol","stream":false,"input":[`+replayReasoningWithContent+`]} {}`)
	require.Error(t, waitOpenAIWSReasoningReplayServer(t, serverErrCh))
	require.Empty(t, upstreamConn.writes, "an undecodable first frame must not reach upstream")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughRejectsUndecodableFollowupFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamConn := &openAIWSCaptureConn{
		readDelays: []time.Duration{0, 150 * time.Millisecond},
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_passthrough_invalid_1","model":"gpt-5.6-sol","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_passthrough_invalid_2","model":"gpt-5.6-sol","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	svc, account := newOpenAIWSPassthroughReasoningReplayTestService(upstreamConn)

	wsServer, serverErrCh := openAIWSReasoningReplayTestServer(t, svc, account)
	defer wsServer.Close()
	clientConn := dialOpenAIWSReasoningReplayTestServer(t, wsServer)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeOpenAIWSReasoningReplayFrame(t, clientConn, coderws.MessageText,
		`{"type":"response.create","model":"gpt-5.6-sol","stream":false,"input":[{"type":"message","role":"user","content":"hi"}]}`)
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_passthrough_invalid_1", gjson.GetBytes(event, "response.id").String())

	writeOpenAIWSReasoningReplayFrame(t, clientConn, coderws.MessageText,
		`{"type":"response.create","model":"gpt-5.6-sol","stream":false,"previous_response_id":"resp_passthrough_invalid_1","input":[`+replayReasoningWithContent+`]} {}`)
	// The relay rejects the frame; closing the client lets the proxy finish
	// even if the rejection is only logged.
	readCtx, cancelRead = context.WithTimeout(context.Background(), time.Second)
	_, _, _ = clientConn.Read(readCtx)
	cancelRead()
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")
	_ = waitOpenAIWSReasoningReplayServer(t, serverErrCh)
	require.Len(t, upstreamConn.writes, 1, "the undecodable follow-up frame must not reach upstream")
}
