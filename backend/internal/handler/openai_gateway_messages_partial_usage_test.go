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
