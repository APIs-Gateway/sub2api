package service

// Fork port of upstream 96884dd1a (isolate HTTP bridge connection state).
// Upstream's test drives its http_bridge ingress mode, which the fork does not
// have; here the size-threshold bridge is exercised instead, with a state
// store that already holds another connection's session turn state and conn.

import (
	"context"
	"io"
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

type bridgeIsolationStateStore struct {
	OpenAIWSStateStore
	mu               sync.Mutex
	sessionTurnBinds []string
	sessionConnBinds []string
	turnStateReads   int
	sessionConnReads int
}

func (s *bridgeIsolationStateStore) GetSessionTurnState(int64, string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnStateReads++
	return "sentinel-turn-state", true
}

func (s *bridgeIsolationStateStore) GetSessionConn(int64, string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionConnReads++
	return "sentinel-native-conn", true
}

func (s *bridgeIsolationStateStore) BindSessionTurnState(_ int64, _ string, turnState string, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionTurnBinds = append(s.sessionTurnBinds, turnState)
}

func (s *bridgeIsolationStateStore) BindSessionConn(_ int64, _ string, connID string, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionConnBinds = append(s.sessionConnBinds, connID)
}

func bridgeIsolationSSE(responseID string) string {
	return strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"` + responseID + `","model":"gpt-5"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"` + responseID + `","model":"gpt-5","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
}

func TestOpenAIWSHTTPBridgeDoesNotInheritOrPublishSessionTurnState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newResp := func(responseID, turnState string) *http.Response {
		header := http.Header{"Content-Type": []string{"text/event-stream"}}
		header.Set(openAIWSTurnStateHeader, turnState)
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(bridgeIsolationSSE(responseID)))}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newResp("resp_bridge_1", "turn-bridge-1"),
		newResp("resp_bridge_2", "turn-bridge-2"),
	}}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
			OpenAIWS: config.GatewayOpenAIWSConfig{
				Enabled:                  true,
				APIKeyEnabled:            true,
				ResponsesWebsocketsV2:    true,
				ModeRouterV2Enabled:      true,
				IngressModeDefault:       OpenAIWSIngressModeCtxPool,
				HTTPBridgeEnabled:        true,
				HTTPBridgeThresholdBytes: 1,
			},
		},
	}
	store := &bridgeIsolationStateStore{OpenAIWSStateStore: NewOpenAIWSStateStore(nil)}
	svc := &OpenAIGatewayService{
		cfg:                cfg,
		httpUpstream:       upstream,
		toolCorrector:      NewCodexToolCorrector(),
		openaiWSStateStore: store,
	}
	account := &Account{
		ID:          19,
		Name:        "api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    OpenAIWSIngressModeCtxPool,
		},
		Concurrency: 1,
		Status:      StatusActive,
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		readCtx, cancelRead := context.WithTimeout(r.Context(), 5*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("session_id", "shared-session")
		ginCtx.Request = req
		proxyCtx, cancelProxy := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancelProxy()
		errCh <- svc.ProxyResponsesWebSocketFromClient(proxyCtx, ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientConn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	readUntilCompleted := func() string {
		for {
			_, event, readErr := clientConn.Read(ctx)
			require.NoError(t, readErr)
			if gjson.GetBytes(event, "type").String() == "response.completed" {
				return gjson.GetBytes(event, "response.id").String()
			}
		}
	}
	require.NoError(t, clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5","prompt_cache_key":"shared-cache","input":"first"}`)))
	require.Equal(t, "resp_bridge_1", readUntilCompleted())
	require.NoError(t, clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5","prompt_cache_key":"shared-cache","input":"second"}`)))
	require.Equal(t, "resp_bridge_2", readUntilCompleted())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	require.Len(t, upstream.requests, 2)
	require.Empty(t, upstream.requests[0].Header.Get(openAIWSTurnStateHeader), "bridge must not inherit another connection's session turn state")
	require.Equal(t, "turn-bridge-1", upstream.requests[1].Header.Get(openAIWSTurnStateHeader), "bridge follow-up keeps its own turn state")

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Zero(t, store.turnStateReads, "bridge must not read session turn state")
	require.Zero(t, store.sessionConnReads, "bridge must not read session conn binding")
	require.Empty(t, store.sessionTurnBinds, "bridge must not publish its turn state by session hash")
	require.Empty(t, store.sessionConnBinds)
}
