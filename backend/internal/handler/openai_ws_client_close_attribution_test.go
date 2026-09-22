package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesWebSocket_ProxyExitAttributionReportsOnlyAccountFailures(t *testing.T) {
	tests := []struct {
		name        string
		proxyErr    error
		wantFailure bool
	}{
		{name: "bare_normal_1000_is_not_reported", proxyErr: coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"}},
		{name: "request_cancellation_is_not_reported", proxyErr: context.Canceled},
		{name: "upstream_1001_is_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusGoingAway, "upstream going away", errors.New("upstream closed session")), wantFailure: true},
		{name: "upstream_1011_is_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "upstream proxy failed", errors.New("upstream failed")), wantFailure: true},
		{name: "upstream_deadline_is_reported", proxyErr: fmt.Errorf("upstream stalled: %w", context.DeadlineExceeded), wantFailure: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reports := make(chan bool, 1)
			h := newOpenAIResponsesWebSocketAttributionHandler(t, tt.proxyErr, reports)
			server := newOpenAIResponsesWebSocketAttributionServer(t, h)
			defer server.Close()

			dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
			client, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
			cancelDial()
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()

			writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
			err = client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","stream":false}`))
			cancelWrite()
			require.NoError(t, err)

			readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
			_, _, err = client.Read(readCtx)
			cancelRead()
			require.Error(t, err)
			var closeErr coderws.CloseError
			require.ErrorAs(t, err, &closeErr)

			select {
			case success := <-reports:
				require.True(t, tt.wantFailure, "normal close and cancellation must not report account failure")
				require.False(t, success)
			default:
				require.False(t, tt.wantFailure, "upstream 1001, 1011, and deadline failures must be reported")
			}
		})
	}
}

func newOpenAIResponsesWebSocketAttributionHandler(t *testing.T, proxyErr error, reports chan<- bool) *OpenAIGatewayHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true

	account := service.Account{
		ID:          9451,
		Name:        "openai-ws-attribution",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-attribution", "base_url": "https://api.openai.example"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
		},
	}
	accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingCacheSvc.Stop)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCacheSvc, nil, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
			return true, nil
		},
	}
	return &OpenAIGatewayHandler{
		gatewayService:      gatewaySvc,
		billingCacheService: billingCacheSvc,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		cfg:                 cfg,
		responsesWebSocketProxy: func(context.Context, *gin.Context, *coderws.Conn, *service.Account, string, []byte, *service.OpenAIWSIngressHooks) error {
			return proxyErr
		},
		onOpenAIAccountScheduleResult: func(_ int64, success bool) { reports <- success },
	}
}

func newOpenAIResponsesWebSocketAttributionServer(t *testing.T, h *OpenAIGatewayHandler) *httptest.Server {
	t.Helper()
	groupID := int64(945)
	apiKey := &service.APIKey{
		ID:      9451,
		GroupID: &groupID,
		User:    &service.User{ID: 9451, Status: service.StatusActive},
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.GET("/openai/v1/responses", h.ResponsesWebSocket)
	return httptest.NewServer(router)
}
