package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIWSPassthroughHandlerHarness struct {
	clientConn     *coderws.Conn
	handlerDone    <-chan struct{}
	moderationRepo *contentModerationHandlerTestRepo
	gatewayCache   service.GatewayCache
	accountRepo    *openAIWSTurnHandlerAccountRepo
	apiKey         *service.APIKey
	cfg            *config.Config
}

func TestOpenAIResponsesWebSocketV2PassthroughInvalidLaterJSONIsLocal400(t *testing.T) {
	for _, tc := range []struct {
		name        string
		messageType coderws.MessageType
	}{
		{name: "text", messageType: coderws.MessageText},
		{name: "binary", messageType: coderws.MessageBinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runOpenAIResponsesWebSocketV2PassthroughLocalRejection(t, tc.messageType, `{`, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body", "invalid websocket request payload", nil)
		})
	}
}

func TestOpenAIResponsesWebSocketV2PassthroughLaterPolicyRejectionsHaveHTTPStatus(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		status      int
		errType     string
		message     string
		closeReason string
		setup       func(*openAIWSPassthroughHandlerHarness)
	}{
		{name: "message id", payload: `{"type":"response.create","model":"gpt-5.1","previous_response_id":"msg_bad"}`, status: http.StatusBadRequest, errType: "invalid_request_error", message: "previous_response_id must be a response.id", closeReason: "previous_response_id must be a response.id (resp_*), not a message id"},
		{name: "global image policy", payload: `{"type":"response.create","model":"gpt-5.1","tools":[{"type":"image_generation"}]}`, status: http.StatusBadRequest, errType: "invalid_request_error", message: service.OpenAIResponsesImageGenerationDisabledMessage(), closeReason: service.OpenAIResponsesImageGenerationDisabledMessage(), setup: func(h *openAIWSPassthroughHandlerHarness) {
			h.cfg.Gateway.DisableOpenAIResponsesImageGeneration = true
		}},
		{name: "group image permission", payload: `{"type":"response.create","model":"gpt-5.1","tools":[{"type":"image_generation"}]}`, status: http.StatusForbidden, errType: "permission_error", message: service.ImageGenerationPermissionMessage(), closeReason: service.ImageGenerationPermissionMessage(), setup: func(h *openAIWSPassthroughHandlerHarness) {
			h.apiKey.Group = &service.Group{AllowImageGeneration: false}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runOpenAIResponsesWebSocketV2PassthroughLocalRejection(t, coderws.MessageText, tc.payload, tc.status, tc.errType, tc.message, tc.closeReason, tc.setup)
		})
	}
}

func TestOpenAIResponsesWebSocketCtxPoolLaterLocalRejectionsHaveHTTPStatus(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		status      int
		errType     string
		message     string
		closeReason string
		setup       func(*openAIWSPassthroughHandlerHarness)
	}{
		{name: "invalid JSON", payload: `{`, status: http.StatusBadRequest, errType: "invalid_request_error", message: "Failed to parse request body", closeReason: "invalid websocket request payload"},
		{name: "message id", payload: `{"type":"response.create","model":"gpt-5.1","previous_response_id":"msg_bad"}`, status: http.StatusBadRequest, errType: "invalid_request_error", message: "previous_response_id must be a response.id", closeReason: "previous_response_id must be a response.id (resp_*), not a message id"},
		{name: "response append", payload: `{"type":"response.append","model":"gpt-5.1"}`, status: http.StatusBadRequest, errType: "invalid_request_error", message: "response.append is not supported", closeReason: "response.append is not supported in ws v2; use response.create with previous_response_id"},
		{name: "global image policy", payload: `{"type":"response.create","model":"gpt-5.1","tools":[{"type":"image_generation"}]}`, status: http.StatusBadRequest, errType: "invalid_request_error", message: service.OpenAIResponsesImageGenerationDisabledMessage(), closeReason: service.OpenAIResponsesImageGenerationDisabledMessage(), setup: func(h *openAIWSPassthroughHandlerHarness) {
			h.cfg.Gateway.DisableOpenAIResponsesImageGeneration = true
		}},
		{name: "group image permission", payload: `{"type":"response.create","model":"gpt-5.1","tools":[{"type":"image_generation"}]}`, status: http.StatusForbidden, errType: "permission_error", message: service.ImageGenerationPermissionMessage(), closeReason: service.ImageGenerationPermissionMessage(), setup: func(h *openAIWSPassthroughHandlerHarness) {
			h.apiKey.Group = &service.Group{AllowImageGeneration: false}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runOpenAIResponsesWebSocketV2PassthroughLocalRejection(t, coderws.MessageText, tc.payload, tc.status, tc.errType, tc.message, tc.closeReason, tc.setup, service.OpenAIWSIngressModeCtxPool)
		})
	}
}

func runOpenAIResponsesWebSocketV2PassthroughLocalRejection(t *testing.T, messageType coderws.MessageType, payload string, status int, errType, message, closeReason string, setup func(*openAIWSPassthroughHandlerHarness), ingressModes ...string) {
	upstreamDone := make(chan struct{})
	secondUpstreamFrame := make(chan []byte, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		_, _, err = conn.Read(ctx)
		require.NoError(t, err)
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_turn_1","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`)))
		if _, second, readErr := conn.Read(ctx); readErr == nil {
			secondUpstreamFrame <- append([]byte(nil), second...)
		}
	}))
	defer upstreamServer.Close()
	harness := newOpenAIWSPassthroughHandlerHarness(t, upstreamServer.URL, ingressModes...)
	if setup != nil {
		setup(harness)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, harness.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"first"}`)))
	_, firstEvent, err := harness.clientConn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "resp_turn_1", gjson.GetBytes(firstEvent, "response.id").String())
	require.NoError(t, harness.clientConn.Write(ctx, messageType, []byte(payload)))
	closeErr := readOpenAIWSRejectionStatus(t, harness.clientConn, status, errType, message)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Equal(t, closeReason, closeErr.Reason)
	select {
	case <-harness.handlerDone:
	case <-ctx.Done():
		t.Fatal("websocket handler did not exit")
	}
	select {
	case <-upstreamDone:
	case <-ctx.Done():
		t.Fatal("upstream websocket did not exit")
	}
	select {
	case second := <-secondUpstreamFrame:
		t.Fatalf("invalid later turn reached upstream: %s", second)
	default:
	}
}

type openAIWSTurnHandlerAccountRepo struct {
	openAIWSUsageHandlerAccountRepoStub
	disabled atomic.Bool
}

func (r *openAIWSTurnHandlerAccountRepo) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	account, err := r.openAIWSUsageHandlerAccountRepoStub.GetByID(ctx, id)
	if account != nil && r.disabled.Load() {
		account.Status = service.StatusDisabled
	}
	return account, err
}

func newOpenAIWSPassthroughHandlerHarness(t *testing.T, upstreamURL string, ingressModes ...string) *openAIWSPassthroughHandlerHarness {
	t.Helper()
	gatewayCache := testutil.NewRedisGatewayCache(t)
	ingressMode := service.OpenAIWSIngressModePassthrough
	if len(ingressModes) > 0 {
		ingressMode = ingressModes[0]
	}

	settingRepo := &contentModerationHandlerSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled:          "true",
		service.SettingKeyCyberSessionBlockEnabled:    "true",
		service.SettingKeyCyberSessionBlockTTLSeconds: "60",
	}}
	moderationRepo := &contentModerationHandlerTestRepo{}
	moderationSvc := service.NewContentModerationService(settingRepo, moderationRepo, nil, nil, nil, nil, nil, nil)
	settingSvc := service.NewSettingService(settingRepo, nil)

	groupID := int64(4301)
	account := service.Account{
		ID:          9951,
		Name:        "openai-ws-passthrough-cyber",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": upstreamURL},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    ingressMode,
		},
	}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 3

	accountRepo := &openAIWSTurnHandlerAccountRepo{openAIWSUsageHandlerAccountRepoStub: openAIWSUsageHandlerAccountRepoStub{account: account}}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo, usageRepo, nil, nil, nil, nil, gatewayCache, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCacheSvc, nil, &service.DeferredService{},
		nil, nil, nil, nil, nil, settingSvc, nil, nil, nil,
	)
	concurrencyCache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := &OpenAIGatewayHandler{
		gatewayService:           gatewaySvc,
		billingCacheService:      billingCacheSvc,
		apiKeyService:            &service.APIKeyService{},
		contentModerationService: moderationSvc,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(concurrencyCache), SSEPingFormatNone, time.Second),
	}

	apiKey := &service.APIKey{
		ID:      1851,
		Name:    "ws-cyber-key",
		Key:     "sk-handler-cyber-test",
		GroupID: &groupID,
		User:    &service.User{ID: 1751, Status: service.StatusActive},
	}
	handlerDone := make(chan struct{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.GET("/openai/v1/responses", func(c *gin.Context) {
		h.ResponsesWebSocket(c)
		close(handlerDone)
	})
	handlerServer := httptest.NewServer(router)
	t.Cleanup(handlerServer.Close)

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(handlerServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.CloseNow() })

	return &openAIWSPassthroughHandlerHarness{
		clientConn:     clientConn,
		handlerDone:    handlerDone,
		moderationRepo: moderationRepo,
		gatewayCache:   gatewayCache,
		accountRepo:    accountRepo,
		apiKey:         apiKey,
		cfg:            cfg,
	}
}

func TestOpenAIResponsesWebSocketV2PassthroughRejectsDisabledAccountBeforeSecondUpstreamTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamDone := make(chan struct{})
	secondUpstreamFrame := make(chan []byte, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, _, err = conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)
		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_turn_1","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead = context.WithTimeout(r.Context(), 3*time.Second)
		_, second, err := conn.Read(readCtx)
		cancelRead()
		if err == nil {
			secondUpstreamFrame <- append([]byte(nil), second...)
		}
	}))
	defer upstreamServer.Close()
	harness := newOpenAIWSPassthroughHandlerHarness(t, upstreamServer.URL)

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err := harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"first"}`))
	cancelWrite()
	require.NoError(t, err)
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, firstEvent, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "resp_turn_1", gjson.GetBytes(firstEvent, "response.id").String())

	harness.accountRepo.disabled.Store(true)
	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"second"}`))
	cancelWrite()
	require.NoError(t, err)
	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = harness.clientConn.Read(readCtx)
	cancelRead()
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)

	select {
	case <-harness.handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("websocket handler did not exit")
	}
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream websocket did not exit")
	}
	select {
	case second := <-secondUpstreamFrame:
		t.Fatalf("disabled account received second upstream turn: %s", second)
	default:
	}
}

func TestOpenAIResponsesWebSocketV2PassthroughCyberMarkIsConsumedAfterTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamDone := make(chan struct{})
	secondUpstreamFrame := make(chan []byte, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, _, err = conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		failed := []byte(`{"type":"response.failed","response":{"id":"resp_cyber_handler","model":"gpt-5.1","error":{"code":"cyber_policy","message":"blocked by upstream policy"},"usage":{"input_tokens":11,"output_tokens":3}}}`)
		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, failed)
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead = context.WithTimeout(r.Context(), 3*time.Second)
		_, second, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			return
		}
		secondUpstreamFrame <- append([]byte(nil), second...)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp_cyber_handler_turn_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
		writeCtx, cancelWrite = context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, completed)
		cancelWrite()
		require.NoError(t, err)
	}))
	defer upstreamServer.Close()
	harness := newOpenAIWSPassthroughHandlerHarness(t, upstreamServer.URL)

	requestPayload := `{"type":"response.create","model":"gpt-5.1","prompt_cache_key":"cyber-session-1","input":"test"}`
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err := harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(requestPayload))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "response.failed", gjson.GetBytes(event, "type").String())

	require.Eventually(t, func() bool {
		logs := harness.moderationRepo.logSnapshot()
		return len(logs) == 1 && logs[0].Action == service.ContentModerationActionCyberPolicy &&
			strings.Contains(logs[0].Error, "upstream_usage=in:11,out:3")
	}, 3*time.Second, 10*time.Millisecond, "handler AfterTurn must call recordCyberPolicyIfMarked and write the risk-control event")

	// fork 的 WS 会话屏蔽 key 只取握手 header 的显式会话标识（F5a），不按每轮 body 的
	// prompt_cache_key 派生（上游的逐轮 body 派生来自本批次以外的提交），因此这里不断言
	// 屏蔽表写入，只验证连接级 cyber 闸门拦截下一轮。

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","prompt_cache_key":"cyber-session-1","input":"follow-up"}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, rejection, err := harness.clientConn.Read(readCtx)
	require.NoError(t, err)
	require.Equal(t, "error", gjson.GetBytes(rejection, "type").String())
	require.Equal(t, int64(http.StatusForbidden), gjson.GetBytes(rejection, "status").Int())
	require.Equal(t, "session_blocked_by_cyber_policy", gjson.GetBytes(rejection, "error.code").String())
	_, _, err = harness.clientConn.Read(readCtx)
	cancelRead()
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	// closeOpenAIClientWS caps close reasons at 120 bytes; passthrough must expose
	// the same client-visible prefix rather than dropping the close frame.
	require.Equal(t, "该会话已被网络安全策略屏蔽，请开启新会话 / This session is blocked by cyber-security policy, please ", closeErr.Reason)
	select {
	case <-harness.handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("websocket handler did not exit")
	}
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream websocket did not exit")
	}
	select {
	case second := <-secondUpstreamFrame:
		t.Fatalf("blocked follow-up reached upstream: %s", second)
	default:
	}
}

func TestOpenAIResponsesWebSocketV2PassthroughNonCyberTurnAllowsFollowup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamDone := make(chan struct{})
	secondUpstreamFrame := make(chan []byte, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, _, err = conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		firstCompleted := []byte(`{"type":"response.completed","response":{"id":"resp_non_cyber_handler_turn_1","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`)
		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, firstCompleted)
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead = context.WithTimeout(r.Context(), 3*time.Second)
		_, second, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)
		secondUpstreamFrame <- append([]byte(nil), second...)

		secondCompleted := []byte(`{"type":"response.completed","response":{"id":"resp_non_cyber_handler_turn_2","model":"gpt-5.1","usage":{"input_tokens":3,"output_tokens":1}}}`)
		writeCtx, cancelWrite = context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, secondCompleted)
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead = context.WithTimeout(r.Context(), 3*time.Second)
		_, _, _ = conn.Read(readCtx)
		cancelRead()
	}))
	defer upstreamServer.Close()
	harness := newOpenAIWSPassthroughHandlerHarness(t, upstreamServer.URL)

	firstPayload := `{"type":"response.create","model":"gpt-5.1","prompt_cache_key":"non-cyber-session-1","input":"first"}`
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err := harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(firstPayload))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, firstEvent, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "resp_non_cyber_handler_turn_1", gjson.GetBytes(firstEvent, "response.id").String())

	secondPayload := `{"type":"response.create","model":"gpt-5.1","prompt_cache_key":"non-cyber-session-1","input":"follow-up"}`
	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(secondPayload))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, secondEvent, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "resp_non_cyber_handler_turn_2", gjson.GetBytes(secondEvent, "response.id").String())
	require.Empty(t, harness.moderationRepo.logSnapshot())

	keyCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	keyCtx.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(firstPayload))
	blockKey := service.CyberSessionBlockKey(harness.apiKey.ID, keyCtx, []byte(firstPayload))
	require.NotEmpty(t, blockKey)
	store, ok := harness.gatewayCache.(service.CyberSessionBlockStore)
	require.True(t, ok)
	blocked, findErr := store.IsCyberSessionBlocked(context.Background(), blockKey)
	require.NoError(t, findErr)
	require.False(t, blocked)

	require.NoError(t, harness.clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-harness.handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("non-cyber websocket handler did not exit")
	}
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("non-cyber upstream websocket did not exit")
	}
	select {
	case second := <-secondUpstreamFrame:
		require.JSONEq(t, secondPayload, string(second))
	default:
		t.Fatal("non-cyber follow-up did not reach upstream")
	}
}
