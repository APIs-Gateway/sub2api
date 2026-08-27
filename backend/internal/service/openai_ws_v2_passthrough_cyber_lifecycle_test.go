package service

// 上游 f4e3eb1c5 的 passthrough cyber 生命周期用例。上游把它们放在
// openai_ws_v2_passthrough_lifecycle_test.go，并依赖上游早先引入的 staged
// passthrough 测试桩；fork 的同名文件是另一套结构，因此单独成文件，连同所需的
// 测试桩一起带入。上游依赖"失败事件账号副作用"（fork 无此机制）的两条用例未带入。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stagedPassthroughFrame struct {
	messageType coderws.MessageType
	payload     []byte
	err         error
}

type stagedPassthroughConn struct {
	frames    chan stagedPassthroughFrame
	writes    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func newStagedPassthroughConn() *stagedPassthroughConn {
	return &stagedPassthroughConn{
		frames: make(chan stagedPassthroughFrame, 4),
		writes: make(chan []byte, 4),
		closed: make(chan struct{}),
	}
}

func (c *stagedPassthroughConn) Send(payload string) {
	c.frames <- stagedPassthroughFrame{messageType: coderws.MessageText, payload: []byte(payload)}
}

func (c *stagedPassthroughConn) Fail(err error) {
	c.frames <- stagedPassthroughFrame{err: err}
}

func (c *stagedPassthroughConn) WriteJSON(context.Context, any) error { return nil }

func (c *stagedPassthroughConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, payload, err := c.ReadFrame(ctx)
	return payload, err
}

func (c *stagedPassthroughConn) Ping(context.Context) error { return nil }

func (c *stagedPassthroughConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	case <-c.closed:
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	case frame := <-c.frames:
		return frame.messageType, append([]byte(nil), frame.payload...), frame.err
	}
}

func (c *stagedPassthroughConn) WriteFrame(ctx context.Context, _ coderws.MessageType, payload []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errOpenAIWSConnClosed
	default:
	}
	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return err
	}
	select {
	case c.writes <- append([]byte(nil), payload...):
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errOpenAIWSConnClosed
	}
	return nil
}

func (c *stagedPassthroughConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

type stagedPassthroughDialer struct {
	conn openAIWSClientConn
}

func (d *stagedPassthroughDialer) Dial(context.Context, string, http.Header, string) (openAIWSClientConn, int, http.Header, error) {
	return d.conn, http.StatusSwitchingProtocols, http.Header{}, nil
}

func newPassthroughLifecycleService(cfg *config.Config, upstream *stagedPassthroughConn) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: &stagedPassthroughDialer{conn: upstream},
	}
}

func passthroughLifecycleConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	return cfg
}

func passthroughLifecycleAccount() *Account {
	return &Account{
		ID:          901,
		Name:        "passthrough-lifecycle",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
		},
	}
}

func startPassthroughLifecycleServer(
	t *testing.T,
	controlCtx context.Context,
	svc *OpenAIGatewayService,
	account *Account,
) (*httptest.Server, <-chan error) {
	return startPassthroughLifecycleServerWithHooks(t, controlCtx, svc, account, nil)
}

func startPassthroughLifecycleServerWithHooks(
	t *testing.T,
	controlCtx context.Context,
	svc *OpenAIGatewayService,
	account *Account,
	hooksFactory func(*gin.Context) *OpenAIWSIngressHooks,
) (*httptest.Server, <-chan error) {
	t.Helper()
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		msgType, firstMessage, err := ReadOpenAIWSClientMessage(
			controlCtx,
			conn,
			3*time.Second,
			coderws.StatusPolicyViolation,
			"missing first response.create message",
		)
		if err != nil {
			serverErr <- err
			return
		}
		if msgType != coderws.MessageText {
			serverErr <- errors.New("first message was not text")
			return
		}

		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		req := r.Clone(controlCtx)
		req.Header = req.Header.Clone()
		ginCtx.Request = req
		var hooks *OpenAIWSIngressHooks
		if hooksFactory != nil {
			hooks = hooksFactory(ginCtx)
		}
		serverErr <- svc.ProxyResponsesWebSocketFromClient(controlCtx, ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	return server, serverErr
}

func TestPassthroughLifecycle_CyberTerminalEventsMarkBeforeAfterTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		events      []string
		wantBody    string
		wantMessage string
		wantInput   int
		wantOutput  int
	}{
		{
			name: "error",
			events: []string{
				`{"type":"error","error":{"code":"cyber_policy","message":"blocked by error event"},"usage":{"input_tokens":5,"output_tokens":1}}`,
				`{"type":"response.failed","response":{"id":"resp_error","error":{"code":"cyber_policy","message":"blocked by paired failed event"},"usage":{"input_tokens":9,"output_tokens":2}}}`,
			},
			wantBody:    `"type":"error"`,
			wantMessage: "blocked by error event",
			wantInput:   5,
			wantOutput:  1,
		},
		{
			name: "response_failed",
			events: []string{
				`{"type":"response.failed","response":{"id":"resp_failed","error":{"code":"cyber_policy","message":"blocked by failed event"},"usage":{"input_tokens":9,"output_tokens":2}}}`,
			},
			wantBody:    `"type":"response.failed"`,
			wantMessage: "blocked by failed event",
			wantInput:   9,
			wantOutput:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controlCtx, cancelControl := context.WithCancelCause(context.Background())
			defer cancelControl(context.Canceled)
			upstream := newStagedPassthroughConn()
			for _, event := range tt.events {
				upstream.Send(event)
			}

			markSeen := make(chan CyberPolicyMark, 1)
			afterTurnCalls := atomic.Int32{}
			server, serverErr := startPassthroughLifecycleServerWithHooks(
				t,
				controlCtx,
				newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream),
				passthroughLifecycleAccount(),
				func(c *gin.Context) *OpenAIWSIngressHooks {
					return &OpenAIWSIngressHooks{AfterTurn: func(_ int, _ *OpenAIForwardResult, _ error) {
						afterTurnCalls.Add(1)
						if mark := GetOpsCyberPolicy(c); mark != nil {
							select {
							case markSeen <- *mark:
							default:
							}
						}
					}}
				},
			)
			defer server.Close()
			clientConn := dialPassthroughLifecycleClient(t, server)
			defer func() { _ = clientConn.CloseNow() }()

			for range tt.events {
				_, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
				require.NoError(t, err)
			}

			select {
			case mark := <-markSeen:
				require.Equal(t, "cyber_policy", mark.Code)
				require.Equal(t, tt.wantMessage, mark.Message)
				require.Contains(t, mark.Body, tt.wantBody)
				require.Equal(t, http.StatusOK, mark.UpstreamStatus)
				require.Equal(t, tt.wantInput, mark.UpstreamInTok)
				require.Equal(t, tt.wantOutput, mark.UpstreamOutTok)
			case <-time.After(3 * time.Second):
				t.Fatal("cyber mark was not visible to AfterTurn")
			}
			require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
			select {
			case <-serverErr:
			case <-time.After(3 * time.Second):
				t.Fatal("cyber passthrough test did not exit")
			}
			require.Equal(t, int32(1), afterTurnCalls.Load(), "error/response.failed pair must complete and record once")
		})
	}
}

func TestPassthroughLifecycle_CloseReasonTruncationPreservesUTF8(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	originalReason := strings.Repeat("a", 119) + "界"
	upstream.Fail(NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, originalReason, errors.New("policy rejected")))

	server, serverErr := startPassthroughLifecycleServer(
		t,
		controlCtx,
		newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream),
		passthroughLifecycleAccount(),
	)
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	_, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.True(t, utf8.ValidString(closeErr.Reason))
	require.LessOrEqual(t, len(closeErr.Reason), 120)
	require.Equal(t, strings.Repeat("a", 119), closeErr.Reason)

	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough close reason test did not exit")
	}
}

func dialPassthroughLifecycleClient(t *testing.T, server *httptest.Server) *coderws.Conn {
	t.Helper()
	return dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-5.1","stream":false}`)
}

func dialPassthroughLifecycleClientWithPayload(t *testing.T, server *httptest.Server, payload string) *coderws.Conn {
	t.Helper()
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(payload))
	cancelWrite()
	require.NoError(t, err)
	return clientConn
}

func readPassthroughLifecycleFrame(t *testing.T, clientConn *coderws.Conn, timeout time.Duration) ([]byte, error) {
	t.Helper()
	readCtx, cancelRead := context.WithTimeout(context.Background(), timeout)
	_, payload, err := clientConn.Read(readCtx)
	cancelRead()
	return payload, err
}
