package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// openAIRecordUsageBillingRepoErrStub795Msg makes RecordUsage's
// applyUsageBilling step fail with a real (non-pricing-unavailable) error, so
// h.gatewayService.RecordUsage(...) returns a non-nil error and the
// "openai_messages.record_usage_failed" zap-logging branch executes. Only
// reachable when cfg.RunMode is NOT RunModeSimple (Simple mode returns before
// ever calling applyUsageBilling).
type openAIRecordUsageBillingRepoErrStub795Msg struct {
	service.UsageBillingRepository
	applied chan struct{}
}

func (s *openAIRecordUsageBillingRepoErrStub795Msg) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	if s.applied != nil {
		s.applied <- struct{}{}
	}
	return nil, errors.New("simulated usage billing repo failure")
}

// openAIRecordUsageUserRepoStub795Msg backs BillingCacheService.CheckBillingEligibility's
// DB balance fallback (s.cache == nil) so it doesn't nil-deref on s.userRepo;
// a positive balance lets the pure-wallet admission check pass cleanly.
type openAIRecordUsageUserRepoStub795Msg struct {
	service.UserRepository
	user service.User
}

func (s *openAIRecordUsageUserRepoStub795Msg) GetByID(ctx context.Context, id int64) (*service.User, error) {
	u := s.user
	return &u, nil
}

// TestOpenAIMessages_RawChatFallbackStreamReadErrorRecordsPartialUsage mirrors
// TestOpenAIResponses_RawChatFallbackStreamReadErrorRecordsPartialUsage for the
// Messages() handler: ForwardAsAnthropic's raw-chat-completions fallback path
// (see service.TestForwardAsAnthropic_ForceChatCompletionsStreamReadErrorSkipsFinalize)
// returns a non-nil *OpenAIForwardResult alongside a plain "stream usage
// incomplete" error (not *UpstreamFailoverError) when the upstream SSE body
// dies mid-transfer. Before PR #795 this partial usage was dropped on the
// generic error path; submitMessagesUsage(result) must now still fire.
func TestOpenAIMessages_RawChatFallbackStreamReadErrorRecordsPartialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseChunk := "data: {\"id\":\"cc1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"
	httpUpstream := openAIHandlerHTTPUpstreamStub{
		do: func(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
			respBody := io.NopCloser(io.MultiReader(
				strings.NewReader(sseChunk),
				erroringReaderGW795{err: errors.New("simulated upstream read failure")},
			))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       respBody,
			}, nil
		},
	}

	groupID := int64(4211)
	account := service.Account{
		ID:          9911,
		Name:        "openai-raw-messages-stream-error",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.openai.example",
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
		},
	}

	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true

	accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	defer billingCacheSvc.Stop()
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheSvc,
		httpUpstream,
		&service.DeferredService{},
		nil, // grokTokenProvider
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // userPlatformQuotaRepo
		nil, // stableStore
		nil, // groupRepo
	)

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}
	h := &OpenAIGatewayHandler{
		gatewayService:      gatewaySvc,
		billingCacheService: billingCacheSvc,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}

	apiKey := &service.APIKey{
		ID:      1811,
		GroupID: &groupID,
		User:    &service.User{ID: 1711, Status: service.StatusActive},
		Group: &service.Group{
			ID:                    groupID,
			Platform:              service.PlatformOpenAI,
			Status:                service.StatusActive,
			RateMultiplier:        1,
			AllowMessagesDispatch: true,
		},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.POST("/openai/v1/messages", h.Messages)

	req := httptest.NewRequest(
		http.MethodPost,
		"/openai/v1/messages",
		strings.NewReader(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	select {
	case usageLog := <-usageRepo.created:
		require.NotNil(t, usageLog)
	case <-time.After(3 * time.Second):
		t.Fatal("等待 partial usage 写入超时——submitMessagesUsage 未在流式读取错误的错误路径上被调用")
	}
}

// failingWriteResponseWriter is a minimal http.ResponseWriter whose Write
// always fails, simulating a client that disconnected mid-stream. WriteHeader
// still succeeds (matching real net/http behavior: headers are buffered
// locally and only the body write hits the broken connection).
type failingWriteResponseWriter struct {
	header     http.Header
	statusCode int
}

func (w *failingWriteResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingWriteResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *failingWriteResponseWriter) Write(p []byte) (int, error) {
	return 0, errors.New("simulated client disconnect: broken pipe")
}

// TestOpenAIMessages_RawChatFallbackClientDisconnectDuringStreamReadErrorRecordsPartialUsage
// covers the narrower sibling of the test above: streamChatCompletionsAsAnthropic
// sets result.ClientDisconnect=true when writing an SSE event to the client
// fails (see openai_gateway_messages_chat_fallback.go's clientDisconnected
// flag), and the scanner then keeps draining upstream for billing until it
// also hits a read error — producing a non-nil result with ClientDisconnect
// true alongside the same "stream usage incomplete" error. Messages() has a
// dedicated branch for this exact combination (result.ClientDisconnect==true)
// that must still call submitMessagesUsage(result) instead of silently
// dropping the partial usage, per PR #795's fix for issue #5148.
func TestOpenAIMessages_RawChatFallbackClientDisconnectDuringStreamReadErrorRecordsPartialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseChunk := "data: {\"id\":\"cc1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"
	httpUpstream := openAIHandlerHTTPUpstreamStub{
		do: func(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
			respBody := io.NopCloser(io.MultiReader(
				strings.NewReader(sseChunk),
				erroringReaderGW795{err: errors.New("simulated upstream read failure")},
			))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       respBody,
			}, nil
		},
	}

	groupID := int64(4212)
	account := service.Account{
		ID:          9912,
		Name:        "openai-raw-messages-client-disconnect",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.openai.example",
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
		},
	}

	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true

	accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	defer billingCacheSvc.Stop()
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheSvc,
		httpUpstream,
		&service.DeferredService{},
		nil, // grokTokenProvider
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // userPlatformQuotaRepo
		nil, // stableStore
		nil, // groupRepo
	)

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}
	h := &OpenAIGatewayHandler{
		gatewayService:      gatewaySvc,
		billingCacheService: billingCacheSvc,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}

	apiKey := &service.APIKey{
		ID:      1812,
		GroupID: &groupID,
		User:    &service.User{ID: 1712, Status: service.StatusActive},
		Group: &service.Group{
			ID:                    groupID,
			Platform:              service.PlatformOpenAI,
			Status:                service.StatusActive,
			RateMultiplier:        1,
			AllowMessagesDispatch: true,
		},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.POST("/openai/v1/messages", h.Messages)

	req := httptest.NewRequest(
		http.MethodPost,
		"/openai/v1/messages",
		strings.NewReader(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	// 用一个 Write 恒失败的 ResponseWriter 模拟客户端中途断开——这样
	// streamChatCompletionsAsAnthropic 里第一次 fmt.Fprint(c.Writer, sse) 就会
	// 失败并把 clientDisconnected 置真，之后扫描器继续排水直到遇到上游读取错误，
	// 最终返回 ClientDisconnect==true 且非 nil 的 err，命中 Messages() 里那个更窄的分支。
	w := &failingWriteResponseWriter{}
	router.ServeHTTP(w, req)

	select {
	case usageLog := <-usageRepo.created:
		require.NotNil(t, usageLog)
	case <-time.After(3 * time.Second):
		t.Fatal("等待 partial usage 写入超时——submitMessagesUsage 未在 ClientDisconnect 错误路径上被调用")
	}
}

// TestOpenAIMessages_RecordUsageBillingErrorLogsFailure covers the
// previously-uncovered branch in Messages() where
// h.gatewayService.RecordUsage(ctx, ...) itself returns a non-nil error
// (applyUsageBilling's repo.Apply failed) and the handler must log
// "openai_messages.record_usage_failed" instead of silently dropping the
// failure. Mirrors TestOpenAIResponses_RecordUsageBillingErrorLogsFailure.
func TestOpenAIMessages_RecordUsageBillingErrorLogsFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	httpUpstream := openAIHandlerHTTPUpstreamStub{
		do: func(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
			body := `{"id":"chatcmpl_billing_err","object":"chat.completion","model":"gpt-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		},
	}

	groupID := int64(4215)
	account := service.Account{
		ID:          9915,
		Name:        "openai-raw-messages-record-usage-billing-error",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.openai.example",
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
		},
	}

	cfg := &config.Config{}
	// 关键：不设 cfg.RunMode = RunModeSimple —— RecordUsage 在 Simple 模式下会在
	// applyUsageBilling 之前就 return nil，永远走不到 billingErr 分支。
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true

	accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
	billingRepo := &openAIRecordUsageBillingRepoErrStub795Msg{applied: make(chan struct{}, 1)}
	userRepo := &openAIRecordUsageUserRepoStub795Msg{user: service.User{ID: 1715, Balance: 1000, Status: service.StatusActive}}
	billingCacheSvc := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil, nil)
	defer billingCacheSvc.Stop()
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		billingRepo,
		userRepo,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheSvc,
		httpUpstream,
		&service.DeferredService{},
		nil, // grokTokenProvider
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // userPlatformQuotaRepo
		nil, // stableStore
		nil, // groupRepo
	)

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}
	h := &OpenAIGatewayHandler{
		gatewayService:      gatewaySvc,
		billingCacheService: billingCacheSvc,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}

	apiKey := &service.APIKey{
		ID:      1815,
		GroupID: &groupID,
		User:    &service.User{ID: 1715, Status: service.StatusActive},
		Group: &service.Group{
			ID:                    groupID,
			Platform:              service.PlatformOpenAI,
			Status:                service.StatusActive,
			RateMultiplier:        1,
			AllowMessagesDispatch: true,
		},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.POST("/openai/v1/messages", h.Messages)

	req := httptest.NewRequest(
		http.MethodPost,
		"/openai/v1/messages",
		// gpt-5.1 有真实的 fallback 定价，保证成本计算本身不出错、不会被
		// isUsagePricingUnavailableError 吞掉。
		strings.NewReader(`{"model":"gpt-5.1","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "RecordUsage 失败只应记日志，不应影响已经写回客户端的响应")

	select {
	case <-billingRepo.applied:
	case <-time.After(3 * time.Second):
		t.Fatal("等待 usageBillingRepo.Apply 被调用超时——RecordUsage 未走到 billingErr 分支")
	}
}
