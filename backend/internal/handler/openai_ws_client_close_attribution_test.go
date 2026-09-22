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

// issue #945（上游 #6105）：入站 Responses WebSocket 的正常结束会被记成账号故障。
//
// 归因发生在 openai_gateway_handler.go 的 ingress 收尾处：只有
// *service.OpenAIWSClientCloseError 且状态码为 1000 被认作正常关闭，裸 close
// error 与取消会落到账号失败归因。于是客户端干净关闭（底层直接回裸 coderws.CloseError{1000}）
// 与客户端中途断开（context.Canceled，收尾用 1001 关闭）都会喂给
// ObserveOpenAIAPIKeyHealthFailure 与 scheduler.ReportResult(success=false)，
// 累积到阈值即把上游账号熔断出调度池。
//
// 这些用例钉住判定本身，与既有的 TestShouldReportOpenAIWSProxyAccountFailure 同一层级：
// 调用点位于一个需要真实上游 WS 才能进入的巨型 handler 循环内，仓库既有约定就是直接测判定函数。

// 缺陷主复现之一：客户端干净关闭。底层 conn.Read 的错误被 ReadOpenAIWSClientMessage
// 原样返回，没有任何地方把它包成 *OpenAIWSClientCloseError，所以旧断言看不见它。
func TestOpenAIWSIngressEndedByClient_BareNormalClosureIsNotAccountFailure(t *testing.T) {
	err := coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"}

	// 前提：这正是旧判据漏掉它的原因——类型不匹配，不是状态码不匹配。
	var closeErr *service.OpenAIWSClientCloseError
	require.False(t, errors.As(err, &closeErr),
		"裸 coderws.CloseError 不是 *OpenAIWSClientCloseError，旧的 errors.As 必然为假")
	// 而按关闭码读，它确实是 1000。
	require.Equal(t, coderws.StatusNormalClosure, coderws.CloseStatus(err))

	require.True(t, openAIWSIngressEndedByClient(err))
	require.False(t, shouldReportOpenAIWSProxyAccountFailure(err))
}

// 同一形状被包一层（例如 ingress 把 read 错误裹进上下文）时也必须认得。
func TestOpenAIWSIngressEndedByClient_WrappedBareNormalClosureIsNotAccountFailure(t *testing.T) {
	err := fmt.Errorf("ingress turn 3: %w",
		coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"})

	require.True(t, openAIWSIngressEndedByClient(err))
	require.False(t, shouldReportOpenAIWSProxyAccountFailure(err))
}

// 缺陷主复现之二：客户端中途断开。ReadOpenAIWSClientMessage 在 controlCtx.Done()
// 分支用 StatusGoingAway 收尾并把 context.Canceled 作为 cause，所以「只认 1000」
// 这一条判据根本匹配不到它。
func TestOpenAIWSIngressEndedByClient_ClientCancelDuringTurnIsNotAccountFailure(t *testing.T) {
	err := service.NewOpenAIWSClientCloseError(
		coderws.StatusGoingAway, "websocket request canceled", context.Canceled)

	// 前提：状态码是 1001 不是 1000，旧判据必然放行到账号归因。
	var closeErr *service.OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusGoingAway, closeErr.StatusCode())
	require.NotEqual(t, coderws.StatusNormalClosure, closeErr.StatusCode())

	require.True(t, openAIWSIngressEndedByClient(err))
	require.False(t, shouldReportOpenAIWSProxyAccountFailure(err))
}

// 既有行为不得回退：网关自己用 1000 收尾（inter-turn idle timeout 就是这条）
// 原本就被认作正常关闭。
func TestOpenAIWSIngressEndedByClient_GatewayNormalClosureStillRecognised(t *testing.T) {
	err := service.NewOpenAIWSClientCloseError(
		coderws.StatusNormalClosure, "websocket idle timeout", context.DeadlineExceeded)

	require.True(t, openAIWSIngressEndedByClient(err))
	require.False(t, shouldReportOpenAIWSProxyAccountFailure(err))
}

// 收窄证明：1001 本身不足以豁免。网关也会因自身原因用 GoingAway 收场，
// 客户端取消那一支已由 context.Canceled 覆盖，无需整类放行。
func TestOpenAIWSIngressEndedByClient_GoingAwayWithoutCancellationStillReported(t *testing.T) {
	err := service.NewOpenAIWSClientCloseError(
		coderws.StatusGoingAway, "upstream going away", errors.New("upstream closed session"))

	require.False(t, openAIWSIngressEndedByClient(err))
	require.True(t, shouldReportOpenAIWSProxyAccountFailure(err), "真实上游故障仍须归因账号")
}

// 契约没有丢：真正的故障仍然惩罚账号。判定组合与调用点一致——
// openAIWSIngressEndedByClient 为假才会走到 shouldReportOpenAIWSProxyAccountFailure。
func TestOpenAIWSIngressEndedByClient_AbnormalClosuresStillReportAccountFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "upstream_policy_violation",
			err: service.NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation, "upstream websocket authentication failed",
				errors.New("upstream rejected credentials")),
		},
		{
			name: "upstream_internal_error",
			err: service.NewOpenAIWSClientCloseError(
				coderws.StatusInternalError, "upstream websocket proxy failed", nil),
		},
		{
			name: "bare_abnormal_closure",
			err:  coderws.CloseError{Code: coderws.StatusAbnormalClosure, Reason: "connection reset"},
		},
		{
			name: "generic_read_failure",
			err:  errors.New("upstream websocket read failed"),
		},
		{
			// 空闲超时之外的 deadline 是真实停滞：不豁免。
			name: "deadline_without_normal_close",
			err:  fmt.Errorf("upstream stalled: %w", context.DeadlineExceeded),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, openAIWSIngressEndedByClient(tc.err))
			require.True(t, shouldReportOpenAIWSProxyAccountFailure(tc.err))
		})
	}
}

// 不变式：同一条错误，日志侧与归因侧必须给出一致的结论。
// summarizeWSCloseErrorForLog 一直用 coderws.CloseStatus 读关闭码，这正是缺陷时期
// WARN 打印 close_status=1000(StatusNormalClosure) 却同时把账号记为故障的原因。
// 以后任何一侧改了读法，这条会红。
func TestOpenAIWSIngressEndedByClient_MatchesCloseCodeReportedInLog(t *testing.T) {
	errs := []error{
		coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"},
		fmt.Errorf("ingress turn 3: %w", coderws.CloseError{Code: coderws.StatusNormalClosure}),
		service.NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "websocket idle timeout", context.DeadlineExceeded),
		service.NewOpenAIWSClientCloseError(coderws.StatusGoingAway, "websocket request canceled", context.Canceled),
		coderws.CloseError{Code: coderws.StatusAbnormalClosure, Reason: "connection reset"},
		errors.New("upstream websocket read failed"),
	}

	for _, err := range errs {
		t.Run(err.Error(), func(t *testing.T) {
			closeStatus, _ := summarizeWSCloseErrorForLog(err)
			if closeStatus == "1000(StatusNormalClosure)" {
				require.True(t, openAIWSIngressEndedByClient(err),
					"日志按 1000 归类为正常关闭，归因侧不得同时判为账号故障")
			}
		})
	}
}

func TestOpenAIResponsesWebSocket_ProxyExitAttributionReportsOnlyAccountFailures(t *testing.T) {
	tests := []struct {
		name        string
		proxyErr    error
		wantFailure bool
	}{
		{name: "bare_normal_1000_is_not_reported", proxyErr: coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"}},
		{name: "request_cancellation_is_not_reported", proxyErr: context.Canceled},
		{name: "going_away_without_cancellation_is_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusGoingAway, "upstream going away", errors.New("upstream closed session")), wantFailure: true},
		{name: "upstream_internal_error_is_reported", proxyErr: service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "upstream proxy failed", errors.New("upstream failed")), wantFailure: true},
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
