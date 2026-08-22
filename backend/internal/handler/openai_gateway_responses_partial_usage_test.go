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
	"github.com/tidwall/gjson"
)

// erroringReader always fails after any prior bytes in the same io.MultiReader
// chain are exhausted; used to simulate an upstream SSE stream that dies
// mid-transfer (io.MultiReader propagates a non-EOF error from a later reader
// immediately once the earlier readers are drained).
type erroringReaderGW795 struct{ err error }

func (r erroringReaderGW795) Read(p []byte) (int, error) { return 0, r.err }

// TestOpenAIResponses_RawChatFallbackStreamReadErrorRecordsPartialUsage covers
// the previously-uncovered error path in Responses() where forwardOpenAI
// (via forwardResponsesViaRawChatCompletions's streaming branch) returns a
// non-nil *OpenAIForwardResult alongside a non-UpstreamFailoverError error
// (bufio.Scanner hit a real read error mid-stream). Before PR #795's fix this
// partial usage was silently dropped; now submitResponsesUsage(result) must
// still fire on this path.
func TestOpenAIResponses_RawChatFallbackStreamReadErrorRecordsPartialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseChunk := "data: {\"id\":\"cc1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	httpUpstream := openAIHandlerHTTPUpstreamStub{
		do: func(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
			body := io.NopCloser(io.MultiReader(
				strings.NewReader(sseChunk),
				erroringReaderGW795{err: errors.New("boom: upstream connection reset")},
			))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       body,
			}, nil
		},
	}

	groupID := int64(4210)
	account := service.Account{
		ID:          9910,
		Name:        "openai-raw-chat-stream-error",
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
		ID:      1810,
		GroupID: &groupID,
		User:    &service.User{ID: 1710, Status: service.StatusActive},
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
	router.POST("/openai/v1/responses", h.Responses)

	req := httptest.NewRequest(
		http.MethodPost,
		"/openai/v1/responses",
		strings.NewReader(`{"model":"gpt-4o-mini","input":"hello","stream":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	select {
	case usageLog := <-usageRepo.created:
		require.NotNil(t, usageLog)
	case <-time.After(3 * time.Second):
		t.Fatal("等待 partial usage 写入超时——submitResponsesUsage 未在流式读取错误的错误路径上被调用")
	}
}

// TestOpenAIResponses_ChannelModelMappingRewritesForwardModel covers the
// previously-uncovered branch in Responses() where a channel-level model
// mapping is applied (channelMapping.Mapped == true), which must rewrite
// forwardModel (used to seed the forward-model context for downstream
// channel-restriction / upstream-model resolution) to the mapped model
// instead of leaving it as the client-requested model.
func TestOpenAIResponses_ChannelModelMappingRewritesForwardModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamModelCh := make(chan string, 1)
	httpUpstream := openAIHandlerHTTPUpstreamStub{
		do: func(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
			reqBody, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			upstreamModelCh <- gjson.GetBytes(reqBody, "model").String()
			body := `{"id":"chatcmpl_channel_mapped","object":"chat.completion","model":"gpt-4o-mini-mapped","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		},
	}

	groupID := int64(4213)
	account := service.Account{
		ID:          9913,
		Name:        "openai-raw-channel-mapping",
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
	channelSvc := service.NewChannelService(&openAIWSUsageHandlerChannelRepoStub{
		channels: []service.Channel{{
			ID:       7702,
			Name:     "openai-responses-channel-mapping",
			Status:   service.StatusActive,
			GroupIDs: []int64{groupID},
			ModelMapping: map[string]map[string]string{
				service.PlatformOpenAI: {"gpt-4o-mini": "gpt-4o-mini-mapped"},
			},
		}},
		groupPlatforms: map[int64]string{groupID: service.PlatformOpenAI},
	}, nil, nil, nil)
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
		channelSvc,
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
		ID:      1813,
		GroupID: &groupID,
		User:    &service.User{ID: 1713, Status: service.StatusActive},
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
	router.POST("/openai/v1/responses", h.Responses)

	req := httptest.NewRequest(
		http.MethodPost,
		"/openai/v1/responses",
		strings.NewReader(`{"model":"gpt-4o-mini","input":"hello","stream":false}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	select {
	case forwardedModel := <-upstreamModelCh:
		require.Equal(t, "gpt-4o-mini-mapped", forwardedModel, "channelMapping.Mapped 分支必须把 forwardModel 改写成渠道映射后的模型再转发给上游")
	case <-time.After(3 * time.Second):
		t.Fatal("等待上游收到转发请求超时")
	}

	select {
	case usageLog := <-usageRepo.created:
		require.NotNil(t, usageLog)
	case <-time.After(3 * time.Second):
		t.Fatal("等待 usage log 写入超时")
	}
}

// openAIRecordUsageBillingRepoErrStub795 makes RecordUsage's applyUsageBilling
// step fail with a real (non-pricing-unavailable) error, so that the
// gatewayService.RecordUsage(...) call in Responses()/Messages() returns a
// non-nil error and the "record_usage_failed" zap-logging branch executes.
// Only reachable when cfg.RunMode is NOT RunModeSimple (Simple mode returns
// before ever calling applyUsageBilling) and the model has real pricing (so
// the cost-calculation step itself succeeds and isn't silently swallowed by
// isUsagePricingUnavailableError).
type openAIRecordUsageBillingRepoErrStub795 struct {
	service.UsageBillingRepository
	applied chan struct{}
}

func (s *openAIRecordUsageBillingRepoErrStub795) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	if s.applied != nil {
		s.applied <- struct{}{}
	}
	return nil, errors.New("simulated usage billing repo failure")
}

// openAIRecordUsageUserRepoStub795 backs BillingCacheService.CheckBillingEligibility's
// DB balance fallback (s.cache == nil) so it doesn't nil-deref on s.userRepo;
// a positive balance lets the pure-wallet admission check pass cleanly.
type openAIRecordUsageUserRepoStub795 struct {
	service.UserRepository
	user service.User
}

func (s *openAIRecordUsageUserRepoStub795) GetByID(ctx context.Context, id int64) (*service.User, error) {
	u := s.user
	return &u, nil
}

// TestOpenAIResponses_RecordUsageBillingErrorLogsFailure covers the
// previously-uncovered branch in Responses() where
// h.gatewayService.RecordUsage(ctx, ...) itself returns a non-nil error
// (applyUsageBilling's repo.Apply failed) and the handler must log
// "openai.record_usage_failed" instead of silently dropping the failure.
func TestOpenAIResponses_RecordUsageBillingErrorLogsFailure(t *testing.T) {
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

	groupID := int64(4214)
	account := service.Account{
		ID:          9914,
		Name:        "openai-raw-record-usage-billing-error",
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
	billingRepo := &openAIRecordUsageBillingRepoErrStub795{applied: make(chan struct{}, 1)}
	userRepo := &openAIRecordUsageUserRepoStub795{user: service.User{ID: 1714, Balance: 1000, Status: service.StatusActive}}
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
		ID:      1814,
		GroupID: &groupID,
		User:    &service.User{ID: 1714, Status: service.StatusActive},
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
	router.POST("/openai/v1/responses", h.Responses)

	req := httptest.NewRequest(
		http.MethodPost,
		"/openai/v1/responses",
		// gpt-5.1 有真实的 fallback 定价（见 openai_gateway_record_usage_test.go 里
		// 同名场景），保证成本计算本身不出错、不会被 isUsagePricingUnavailableError 吞掉。
		strings.NewReader(`{"model":"gpt-5.1","input":"hello","stream":false}`),
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
