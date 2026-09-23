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
		{name: "wrapped_normal_1000_is_not_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "client done", nil)},
		{name: "request_cancellation_is_not_reported", proxyErr: context.Canceled},
		{name: "upstream_1001_is_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusGoingAway, "upstream going away", errors.New("upstream closed session")), wantFailure: true},
		{name: "upstream_1011_is_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "upstream proxy failed", errors.New("upstream failed")), wantFailure: true},
		{name: "upstream_deadline_is_reported", proxyErr: fmt.Errorf("upstream stalled: %w", context.DeadlineExceeded), wantFailure: true},
		// 网关自身的准入拒绝（后续 turn 的用户/账号并发槽位、cyber gate、审计）不是账号故障。
		{name: "gateway_admission_user_slot_1013_is_not_reported", proxyErr: newOpenAIWSGatewayAdmissionCloseError(coderws.StatusTryAgainLater, "too many concurrent requests, please retry later", nil)},
		{name: "gateway_admission_account_busy_1013_is_not_reported", proxyErr: newOpenAIWSGatewayAdmissionCloseError(coderws.StatusTryAgainLater, "account is busy, please retry later", nil)},
		{name: "gateway_admission_cyber_1008_is_not_reported", proxyErr: newOpenAIWSGatewayAdmissionCloseError(coderws.StatusPolicyViolation, cyberSessionBlockedClientMsg, nil)},
		{name: "gateway_admission_slot_error_1011_is_not_reported", proxyErr: newOpenAIWSGatewayAdmissionCloseError(coderws.StatusInternalError, "failed to acquire user concurrency slot", errors.New("redis down"))},
		// 同样是 1013/1008，但来自上游（429 忙、握手鉴权失败）的必须照常上报，防止按状态码一刀切。
		{name: "upstream_busy_1013_is_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "upstream websocket is busy, please retry later", errors.New("upstream 429")), wantFailure: true},
		{name: "upstream_auth_1008_is_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "upstream websocket authentication failed", errors.New("upstream 401")), wantFailure: true},
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
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
			return true, nil
		},
	}
	return newOpenAIResponsesWebSocketAttributionHandlerWithProxy(t, cache, func(context.Context, *gin.Context, *coderws.Conn, *service.Account, string, []byte, *service.OpenAIWSIngressHooks) error {
		return proxyErr
	}, reports)
}

func newOpenAIResponsesWebSocketAttributionHandlerWithProxy(
	t *testing.T,
	cache *concurrencyCacheMock,
	proxy func(context.Context, *gin.Context, *coderws.Conn, *service.Account, string, []byte, *service.OpenAIWSIngressHooks) error,
	reports chan<- bool,
) *OpenAIGatewayHandler {
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
	return &OpenAIGatewayHandler{
		gatewayService:                gatewaySvc,
		billingCacheService:           billingCacheSvc,
		apiKeyService:                 &service.APIKeyService{},
		concurrencyHelper:             NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		cfg:                           cfg,
		responsesWebSocketProxy:       proxy,
		onOpenAIAccountScheduleResult: func(_ int64, success bool) { reports <- success },
	}
}

func newOpenAIResponsesWebSocketAttributionServer(t *testing.T, h *OpenAIGatewayHandler) *httptest.Server {
	t.Helper()
	return newOpenAIResponsesWebSocketAttributionServerWithDone(t, h, nil)
}

// newOpenAIResponsesWebSocketAttributionServerWithDone 在 handler 返回后关闭 handlerDone（非 nil 时）。
func newOpenAIResponsesWebSocketAttributionServerWithDone(t *testing.T, h *OpenAIGatewayHandler, handlerDone chan<- struct{}) *httptest.Server {
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
	router.GET("/openai/v1/responses", func(c *gin.Context) {
		if handlerDone != nil {
			defer close(handlerDone)
		}
		h.ResponsesWebSocket(c)
	})
	return httptest.NewServer(router)
}
