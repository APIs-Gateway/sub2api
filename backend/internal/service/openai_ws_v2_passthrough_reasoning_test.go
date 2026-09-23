package service

import (
	"context"
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

func TestNormalizeOpenAIWSV2PassthroughReasoningContentFrame(t *testing.T) {
	frame := []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"keep"}],"content":[{"type":"reasoning_text","text":"remove"}]}]}`)

	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, msgType := range []coderws.MessageType{coderws.MessageText, coderws.MessageBinary} {
			out, err := normalizeOpenAIWSV2PassthroughReasoningContentFrame(&Account{Platform: PlatformOpenAI, Type: accountType}, msgType, frame)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(out, "input.0.content").Exists())
			require.Equal(t, "keep", gjson.GetBytes(out, "input.0.summary.0.text").String())
		}
	}

	out, err := normalizeOpenAIWSV2PassthroughReasoningContentFrame(&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, coderws.MessageText, frame)
	require.NoError(t, err)
	require.Equal(t, string(frame), string(out))

	out, err = normalizeOpenAIWSV2PassthroughReasoningContentFrame(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, coderws.MessageType(0), frame)
	require.NoError(t, err)
	require.Equal(t, string(frame), string(out))

	portable := []byte(`{"type":"response.create","input":[{"type":"reasoning","content":[],"summary":[]}]}`)
	out, err = normalizeOpenAIWSV2PassthroughReasoningContentFrame(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, coderws.MessageText, portable)
	require.NoError(t, err)
	require.Equal(t, string(portable), string(out))
}

// The WS v2 passthrough adapter must strip cross-provider reasoning.content on
// the first client frame and on every follow-up frame (upstream 32064d39e runs
// the same normalization at both points of openai_ws_v2_passthrough_adapter).
func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughStripsReasoningContent(t *testing.T) {
	gin.SetMode(gin.TestMode)

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

	upstreamConn := &openAIWSCaptureConn{
		readDelays: []time.Duration{0, 150 * time.Millisecond},
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_reasoning_turn_1","model":"gpt-5.6-sol","usage":{"input_tokens":2,"output_tokens":3}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_reasoning_turn_2","model":"gpt-5.6-sol","usage":{"input_tokens":2,"output_tokens":3}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	svc := &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: captureDialer,
	}

	account := &Account{
		ID:          453,
		Name:        "openai-ingress-passthrough-reasoning",
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
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	frames := []struct {
		msgType coderws.MessageType
		payload string
		respID  string
	}{
		{
			msgType: coderws.MessageText,
			payload: `{"type":"response.create","model":"gpt-5.6-sol","stream":false,"input":[{"type":"message","role":"user","content":"hello"},{"type":"reasoning","summary":[{"type":"summary_text","text":"keep-1"}],"content":[{"type":"reasoning_text","text":"remove-1"}]}]}`,
			respID:  "resp_reasoning_turn_1",
		},
		{
			msgType: coderws.MessageBinary,
			payload: `{"type":"response.create","model":"gpt-5.6-sol","stream":false,"previous_response_id":"resp_reasoning_turn_1","input":[{"type":"message","role":"user","content":"again"},{"type":"reasoning","summary":[{"type":"summary_text","text":"keep-2"}],"content":[{"type":"reasoning_text","text":"remove-2"}]}]}`,
			respID:  "resp_reasoning_turn_2",
		},
	}
	for _, frame := range frames {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, frame.msgType, []byte(frame.payload))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		_, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, frame.respID, gjson.GetBytes(event, "response.id").String())
	}
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		if serverErr != nil {
			require.Contains(t, serverErr.Error(), "StatusNormalClosure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for passthrough websocket to finish")
	}

	require.Len(t, upstreamConn.writes, 2)
	for i, want := range []string{"keep-1", "keep-2"} {
		written := requestToJSONString(upstreamConn.writes[i])
		require.Equal(t, "reasoning", gjson.Get(written, "input.1.type").String())
		require.False(t, gjson.Get(written, "input.1.content").Exists(), "frame %d reasoning.content must be stripped", i)
		require.Equal(t, want, gjson.Get(written, "input.1.summary.0.text").String())
	}
}
