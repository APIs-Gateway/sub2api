//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// The WS v2 upstream sends partial output and then stalls; the client cancels
// mid-stream, so Forward returns a ClientDisconnect result together with an
// incomplete-stream error. The handler must treat it as a client disconnect
// (submit observed usage, no synthetic error body) rather than an upstream
// failure.
func TestOpenAIGatewayHandlerResponses_WSv2ClientDisconnectSubmitsUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var request map[string]any
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"type":     "response.created",
			"response": map[string]any{"id": "resp_handler_disconnect", "model": "gpt-5.4"},
		})
		_ = conn.WriteJSON(map[string]any{"type": "response.output_text.delta", "delta": "partial"})
		// Client goes away while the upstream is still generating.
		time.AfterFunc(100*time.Millisecond, cancel)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	accountRepo := openAIImagesFailoverAccountRepo{accounts: []service.Account{{
		ID:          7,
		Name:        "responses-ws-v2",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": wsServer.URL},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}}}
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingService.Stop)
	h := NewOpenAIGatewayHandler(
		gatewayService,
		service.NewConcurrencyService(nil),
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil,
		cfg,
	)

	c, rec := newOpenAIFailoverTestContext(t, ctx, "/v1/responses", `{"model":"gpt-5.4","stream":true,"input":"hello"}`, false)

	h.Responses(c)

	require.Error(t, ctx.Err(), "client must have disconnected during the stream")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "partial")
	require.NotContains(t, rec.Body.String(), "response.failed")
	require.NotContains(t, rec.Body.String(), `"error"`)
}
