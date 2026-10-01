package handler

import (
	"context"
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

type chatPartialBillingRepo struct {
	service.UsageBillingRepository
	applied chan *service.UsageBillingCommand
}

type delayedChatSSEReader struct {
	reader *strings.Reader
	delayed bool
}

func (r *delayedChatSSEReader) Read(p []byte) (int, error) {
	if !r.delayed {
		r.delayed = true
		time.Sleep(9 * time.Second) // Let the converted path flush role metadata at its 8s preamble limit.
	}
	return r.reader.Read(p)
}

func (s *chatPartialBillingRepo) Apply(_ context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	s.applied <- cmd
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

// This exercises the real ChatCompletions handler, forwarding service,
// RecordUsage and billing command. The raw path receives a genuine error
// frame carrying usage; the converted path ends in response.failed with usage.
func TestOpenAIChatCompletions_PartialStreamUsageReachesBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		mode openai_compat.ResponsesSupportMode
		payload string
		readError bool
		fixedPrice bool
		wantBilling bool
		wantInput int
		wantOutput int
		status int
		pauseBeforeError bool
	}{
		{
			name: "raw chat error frame with usage",
			mode: openai_compat.ResponsesSupportModeForceChatCompletions,
			payload: "data: {\"id\":\"chatcmpl_partial\",\"model\":\"gpt-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n" +
				"event: error\n" +
				"data: {\"error\":{\"type\":\"api_error\",\"message\":\"upstream stream interrupted\"},\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\n",
			wantBilling: true, wantInput: 11, wantOutput: 5,
		},
		{
			name: "converted responses failed",
			mode: openai_compat.ResponsesSupportModeForceResponses,
			payload: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_partial\",\"model\":\"gpt-5.1\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_partial\",\"model\":\"gpt-5.1\",\"status\":\"failed\",\"usage\":{\"input_tokens\":11,\"output_tokens\":5},\"error\":{\"code\":\"upstream_error\",\"message\":\"stream failed\"}}}\n\n",
			wantBilling: true, wantInput: 11, wantOutput: 5,
		},
		{
			name: "raw delivered output at a fixed per-request price",
			mode: openai_compat.ResponsesSupportModeForceChatCompletions,
			payload: "data: {\"id\":\"chatcmpl_fixed_partial\",\"model\":\"gpt-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n",
			readError: true, fixedPrice: true, wantBilling: true,
		},
		{
			name: "converted pre-output failure at a fixed per-request price",
			mode: openai_compat.ResponsesSupportModeForceResponses,
			payload: "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fixed_failed\",\"model\":\"gpt-5.1\",\"status\":\"failed\",\"error\":{\"code\":\"upstream_error\",\"message\":\"stream failed\"}}}\n\n",
			fixedPrice: true,
		},
		{
			name: "raw usage-only then error at a fixed per-request price",
			mode: openai_compat.ResponsesSupportModeForceChatCompletions,
			payload: "data: {\"id\":\"chatcmpl_usage_only\",\"model\":\"gpt-5.1\",\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n" +
				"event: error\n" +
				"data: {\"error\":{\"type\":\"api_error\",\"message\":\"upstream stream interrupted\"}}\n\n",
			fixedPrice: true,
		},
		{
			name: "converted metadata flushed before error at a fixed per-request price",
			mode: openai_compat.ResponsesSupportModeForceResponses,
			payload: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_metadata\",\"model\":\"gpt-5.1\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
				"__PAUSE__" +
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_metadata\",\"model\":\"gpt-5.1\",\"status\":\"failed\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0},\"error\":{\"code\":\"upstream_error\",\"message\":\"stream failed\"}}}\n\n",
			fixedPrice: true, pauseBeforeError: true,
		},
		{
			name: "pre-output 429 failover has no billing",
			mode: openai_compat.ResponsesSupportModeForceChatCompletions,
			payload: `{"error":{"message":"rate limited","type":"rate_limit_error"}}`,
			status: http.StatusTooManyRequests,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := openAIHandlerHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.payload))
				if tc.pauseBeforeError {
					parts := strings.SplitN(tc.payload, "__PAUSE__", 2)
					body = io.NopCloser(io.MultiReader(strings.NewReader(parts[0]), &delayedChatSSEReader{reader: strings.NewReader(parts[1])}))
				}
				if tc.readError {
					body = io.NopCloser(io.MultiReader(strings.NewReader(tc.payload), erroringReaderGW795{err: io.ErrUnexpectedEOF}))
				}
				return &http.Response{
					StatusCode: status,
					Header: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_chat_partial"}},
					Body: body,
				}, nil
			}}

			groupID := int64(1466)
			account := service.Account{
				ID: 991466, Name: "chat-partial", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeAPIKey, Status: service.StatusActive,
				Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID},
				Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.example"},
				Extra: map[string]any{openai_compat.ExtraKeyResponsesMode: string(tc.mode)},
			}
			cfg := &config.Config{}
			cfg.Default.RateMultiplier = 1
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
			billingRepo := &chatPartialBillingRepo{applied: make(chan *service.UsageBillingCommand, 2)}
			userRepo := &openAIRecordUsageUserRepoStub795{user: service.User{ID: 171466, Balance: 1000, Status: service.StatusActive}}
			billingCache := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil, nil)
			defer billingCache.Stop()
			billingService := service.NewBillingService(cfg, nil)
			var channelService *service.ChannelService
			var resolver *service.ModelPricingResolver
			if tc.fixedPrice {
				price := 0.02
				channelService = service.NewChannelService(&openAIWSUsageHandlerChannelRepoStub{
					channels: []service.Channel{{
						ID: 1466, Status: service.StatusActive, GroupIDs: []int64{groupID},
						ModelPricing: []service.ChannelModelPricing{{
							Platform: service.PlatformOpenAI, Models: []string{"gpt-5.1"},
							BillingMode: service.BillingModePerRequest, PerRequestPrice: &price,
						}},
					}},
					groupPlatforms: map[int64]string{groupID: service.PlatformOpenAI},
				}, nil, nil, nil, nil)
				resolver = service.NewModelPricingResolver(channelService, billingService)
			}
			gateway := service.NewOpenAIGatewayService(
				&openAIWSUsageHandlerAccountRepoStub{account: account}, usageRepo,
				billingRepo, userRepo, nil, nil, nil, cfg, nil, nil,
				billingService, nil, billingCache, upstream,
				&service.DeferredService{}, nil, nil, resolver, channelService, nil, nil, nil, nil, nil,
			)
			cache := &concurrencyCacheMock{
				acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
				acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
			}
			h := &OpenAIGatewayHandler{
				gatewayService: gateway, billingCacheService: billingCache,
				apiKeyService: &service.APIKeyService{},
				concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
			}
			apiKey := &service.APIKey{
				ID: 181466, GroupID: &groupID,
				User: &service.User{ID: 171466, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1},
			}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), apiKey)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
				c.Next()
			})
			router.POST("/openai/v1/chat/completions", h.ChatCompletions)
			req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if tc.name == "raw chat error frame with usage" {
				require.Contains(t, rec.Body.String(), "upstream stream interrupted")
			}

			if !tc.wantBilling {
				require.Empty(t, billingRepo.applied, "pre-output failure must not charge a per-request price")
				require.Empty(t, usageRepo.created, "pre-output failure must not create a usage log")
				return
			}
			require.Len(t, billingRepo.applied, 1, "partial usage must reach billing exactly once")
			cmd := <-billingRepo.applied
			require.Equal(t, tc.wantInput, cmd.InputTokens)
			require.Equal(t, tc.wantOutput, cmd.OutputTokens)
			require.Positive(t, cmd.OfficialCost)
			require.Positive(t, cmd.BalanceCost)
			if tc.fixedPrice {
				require.InDelta(t, 0.02, cmd.BalanceCost, 1e-8)
			}
			require.Len(t, usageRepo.created, 1, "partial usage must create one usage log")
			log := <-usageRepo.created
			require.Equal(t, tc.wantInput, log.InputTokens)
			require.Equal(t, tc.wantOutput, log.OutputTokens)
			require.Positive(t, log.ActualCost)
		})
	}
}
