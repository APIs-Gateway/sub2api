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
)

func TestProxyResponsesWebSocketFromClient_CodexCLIOnlyGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID: 951, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"codex_cli_only": true,
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := &OpenAIGatewayService{cfg: cfg, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg)}

	tests := []struct {
		name       string
		header     http.Header
		account    *Account
		wantDenied bool
	}{
		{
			name:       "unofficial client is rejected before mode routing",
			header:     http.Header{"User-Agent": []string{"ws-probe/1"}},
			account:    account,
			wantDenied: true,
		},
		{
			name:    "official Codex client reaches mode routing",
			header: http.Header{
				"User-Agent": []string{"codex_cli_rs/0.153.4 (Windows 11; x86_64) WindowsTerminal"},
				"Originator": []string{"codex_cli_rs"},
			},
			account: account,
		},
		{
			name:   "unrestricted account allows unofficial client",
			header: http.Header{"User-Agent": []string{"ws-probe/1"}},
			account: &Account{
				ID: 952, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "oauth-token"},
				Extra: map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := proxyCodexClientGateWebSocket(t, svc, tt.account, tt.header)
			var closeErr *OpenAIWSClientCloseError
			require.ErrorAs(t, err, &closeErr)
			if tt.wantDenied {
				require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
				require.Equal(t, CodexOfficialClientsOnlyMessage, closeErr.Reason())
				require.True(t, errors.Is(err, ErrCodexClientRestricted))
				return
			}
			require.Equal(t, "websocket mode is disabled for this account", closeErr.Reason())
			require.False(t, errors.Is(err, ErrCodexClientRestricted))
		})
	}
}

func proxyCodexClientGateWebSocket(t *testing.T, svc *OpenAIGatewayService, account *Account, header http.Header) error {
	t.Helper()
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			serverErr <- err
			return
		}
		serverErr <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "oauth-token", firstMessage, nil)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &coderws.DialOptions{HTTPHeader: header})
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"hello"}`)))
	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		t.Fatal("websocket ingress did not return")
		return nil
	}
}
