package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newPassthroughBeforeTurnTestService(upstreamConn *openAIWSCaptureConn) (*OpenAIGatewayService, *openAIWSCaptureDialer) {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	svc := &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: captureDialer,
	}
	return svc, captureDialer
}

func newPassthroughBeforeTurnTestAccount() *Account {
	return &Account{
		ID:          1074,
		Name:        "openai-ingress-passthrough-before-turn",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-token",
		},
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
		},
	}
}

func startPassthroughBeforeTurnTestServer(
	t *testing.T,
	svc *OpenAIGatewayService,
	account *Account,
	hooks *OpenAIWSIngressHooks,
) (*httptest.Server, <-chan error) {
	t.Helper()
	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
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
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "oauth-token", firstMessage, hooks)
	}))
	return server, serverErrCh
}

func dialPassthroughBeforeTurnTestClient(t *testing.T, server *httptest.Server) *coderws.Conn {
	t.Helper()
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	return clientConn
}

func writePassthroughBeforeTurnTestFrame(t *testing.T, conn *coderws.Conn, payload string) {
	t.Helper()
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err := conn.Write(writeCtx, coderws.MessageText, []byte(payload))
	cancelWrite()
	require.NoError(t, err)
}

func readPassthroughBeforeTurnTestFrame(t *testing.T, conn *coderws.Conn) []byte {
	t.Helper()
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, err := conn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	return event
}

// TestPassthroughIngressFollowUpTurnsReacquireSlotsViaBeforeTurn 钉死 issue #1074：
// handler 在 AfterTurn 释放用户/账号并发槽位，依赖 BeforeTurn 为后续 turn
// 重新抢占。ws_v2 passthrough 必须在每个后续 response.create 写入上游前、
// BeforeRequest 之后调用 BeforeTurn，否则第 2 个 turn 起将绕过并发限制。
func TestPassthroughIngressFollowUpTurnsReacquireSlotsViaBeforeTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamConn := &openAIWSCaptureConn{
		// 让后续 turn 的上游事件稍后到达，确保客户端 response.create 先穿过 relay。
		readDelays: []time.Duration{0, 150 * time.Millisecond, 150 * time.Millisecond},
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_before_turn_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_before_turn_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_before_turn_3","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	svc, _ := newPassthroughBeforeTurnTestService(upstreamConn)

	// 模拟 handler 的槽位语义：握手已持有首轮槽位；AfterTurn 释放，BeforeTurn 重新获取。
	var mu sync.Mutex
	var events []string
	slotHeld := true
	acquisitions := 0
	doubleAcquire := false
	hooks := &OpenAIWSIngressHooks{
		BeforeRequest: func(turn int, _ []byte, _ string) error {
			mu.Lock()
			events = append(events, fmt.Sprintf("before_request:%d", turn))
			mu.Unlock()
			return nil
		},
		BeforeTurn: func(turn int) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, fmt.Sprintf("before_turn:%d", turn))
			if slotHeld {
				doubleAcquire = true
			}
			slotHeld = true
			acquisitions++
			return nil
		},
		AfterTurn: func(turn int, _ *OpenAIForwardResult, _ error) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, fmt.Sprintf("after_turn:%d", turn))
			slotHeld = false
		},
	}

	server, serverErrCh := startPassthroughBeforeTurnTestServer(t, svc, newPassthroughBeforeTurnTestAccount(), hooks)
	defer server.Close()
	clientConn := dialPassthroughBeforeTurnTestClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	writePassthroughBeforeTurnTestFrame(t, clientConn, `{"type":"response.create","model":"gpt-5.1","input":[]}`)
	require.Equal(t, "resp_before_turn_1", gjson.GetBytes(readPassthroughBeforeTurnTestFrame(t, clientConn), "response.id").String())
	writePassthroughBeforeTurnTestFrame(t, clientConn, `{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_before_turn_1","input":[]}`)
	require.Equal(t, "resp_before_turn_2", gjson.GetBytes(readPassthroughBeforeTurnTestFrame(t, clientConn), "response.id").String())
	writePassthroughBeforeTurnTestFrame(t, clientConn, `{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_before_turn_2","input":[]}`)
	require.Equal(t, "resp_before_turn_3", gjson.GetBytes(readPassthroughBeforeTurnTestFrame(t, clientConn), "response.id").String())
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")

	select {
	case <-serverErrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("passthrough websocket did not exit")
	}

	mu.Lock()
	gotEvents := append([]string(nil), events...)
	gotAcquisitions, gotSlotHeld, gotDoubleAcquire := acquisitions, slotHeld, doubleAcquire
	mu.Unlock()

	require.GreaterOrEqual(t, len(gotEvents), 7)
	require.Equal(t, []string{
		"after_turn:1",
		"before_request:2",
		"before_turn:2",
		"after_turn:2",
		"before_request:3",
		"before_turn:3",
		"after_turn:3",
	}, gotEvents[:7], "每个后续 turn 都应先 BeforeRequest 再 BeforeTurn，并与 AfterTurn 成对")
	require.NotContains(t, gotEvents, "before_turn:1", "首轮准入由握手路径完成，不应调用 BeforeTurn")
	require.Equal(t, 2, gotAcquisitions, "turn 2/3 各应重新获取一次槽位")
	require.False(t, gotDoubleAcquire, "BeforeTurn 不应在上一 turn 槽位未释放时再次获取")
	require.False(t, gotSlotHeld, "连接结束后不应残留槽位")
	require.Len(t, upstreamConn.writes, 3, "三个 response.create 都应透传到上游")
}

// TestPassthroughIngressBeforeTurnRejectionDoesNotForwardFollowUp 验证槽位满
// （BeforeTurn 返回 TryAgainLater）时后续 response.create 不会写入上游，连接以
// 该错误结束，且被拒 turn 仍经 AfterTurn 收尾一次。
func TestPassthroughIngressBeforeTurnRejectionDoesNotForwardFollowUp(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamConn := &openAIWSCaptureConn{
		// 第二个事件长时间挂起，确保被拒的 turn 2 不会因上游 EOF 提前结束连接。
		readDelays: []time.Duration{0, 5 * time.Second},
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_reject_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_reject_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	svc, _ := newPassthroughBeforeTurnTestService(upstreamConn)

	rejection := NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "too many concurrent requests, please retry later", nil)
	var mu sync.Mutex
	beforeTurnCalls := []int{}
	afterTurnCalls := 0
	var finalTurnErr error
	hooks := &OpenAIWSIngressHooks{
		BeforeTurn: func(turn int) error {
			mu.Lock()
			beforeTurnCalls = append(beforeTurnCalls, turn)
			mu.Unlock()
			return rejection
		},
		AfterTurn: func(_ int, _ *OpenAIForwardResult, turnErr error) {
			mu.Lock()
			afterTurnCalls++
			if turnErr != nil {
				finalTurnErr = turnErr
			}
			mu.Unlock()
		},
	}

	server, serverErrCh := startPassthroughBeforeTurnTestServer(t, svc, newPassthroughBeforeTurnTestAccount(), hooks)
	defer server.Close()
	clientConn := dialPassthroughBeforeTurnTestClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	writePassthroughBeforeTurnTestFrame(t, clientConn, `{"type":"response.create","model":"gpt-5.1","input":[]}`)
	require.Equal(t, "resp_reject_1", gjson.GetBytes(readPassthroughBeforeTurnTestFrame(t, clientConn), "response.id").String())
	writePassthroughBeforeTurnTestFrame(t, clientConn, `{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_reject_1","input":[]}`)

	var serverErr error
	select {
	case serverErr = <-serverErrCh:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough ingress did not exit after BeforeTurn rejection")
	}
	require.Error(t, serverErr)
	var closeErr *OpenAIWSClientCloseError
	require.True(t, errors.As(serverErr, &closeErr), "BeforeTurn 拒绝应以 client close error 结束连接, got %v", serverErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())

	mu.Lock()
	gotBeforeTurn, gotAfter, gotFinalErr := append([]int(nil), beforeTurnCalls...), afterTurnCalls, finalTurnErr
	mu.Unlock()
	require.Equal(t, []int{2}, gotBeforeTurn)
	require.Equal(t, 2, gotAfter, "每个开始的 turn 都应恰好收尾一次")
	require.ErrorIs(t, gotFinalErr, rejection)

	upstreamConn.mu.Lock()
	upstreamWrites := len(upstreamConn.writes)
	upstreamConn.mu.Unlock()
	require.Equal(t, 1, upstreamWrites, "被 BeforeTurn 拒绝的 response.create 不应写入上游")
}
