//go:build unit

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

type messagesOverflowUsageBillingRepo struct {
	commands chan *service.UsageBillingCommand
}

func (r *messagesOverflowUsageBillingRepo) Apply(_ context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.commands <- cmd
	debit := cmd.BalanceCost
	return &service.UsageBillingApplyResult{Applied: true, WalletDebit: &debit}, nil
}

func TestOpenAIMessagesParallelOverflowCyberBillsOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_overflow_cyber","model":"gpt-5.4"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","call_id":"call_a","name":"first"}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"b","call_id":"call_b","name":"second"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"b","delta":"` + strings.Repeat("x", 1<<20) + `"}`,
		`data: {"type":"response.failed","response":{"id":"resp_overflow_cyber","model":"gpt-5.4","status":"failed","error":{"code":"cyber_policy","message":"blocked"},"usage":{"input_tokens":13,"output_tokens":5}}}`,
	}, "\n\n") + "\n\n"
	httpUpstream := openAIHandlerHTTPUpstreamStub{do: func(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	groupID := int64(4271)
	account := service.Account{
		ID: 9971, Name: "openai-responses-overflow-cyber", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		GroupIDs: []int64{groupID},
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{openai_compat.ExtraKeyResponsesSupported: true},
	}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeStandard
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
	billingRepo := &messagesOverflowUsageBillingRepo{commands: make(chan *service.UsageBillingCommand, 2)}
	userRepo := &openAIRecordUsageUserRepoStub795Msg{user: service.User{ID: 1771, Balance: 1000, Status: service.StatusActive}}
	billingCacheSvc := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil, nil)
	defer billingCacheSvc.Stop()
	gatewaySvc := service.NewOpenAIGatewayService(
		&openAIWSUsageHandlerAccountRepoStub{account: account}, usageRepo,
		billingRepo, userRepo, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCacheSvc, httpUpstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) { return true, nil },
	}
	h := &OpenAIGatewayHandler{
		gatewayService: gatewaySvc, billingCacheService: billingCacheSvc,
		apiKeyService: &service.APIKeyService{},
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}
	apiKey := &service.APIKey{
		ID: 1871, GroupID: &groupID, User: &service.User{ID: 1771, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, AllowMessagesDispatch: true},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.POST("/openai/v1/messages", h.Messages)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/messages",
		strings.NewReader(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error"))
	require.NotContains(t, rec.Body.String(), "event: message_stop")
	select {
	case cmd := <-billingRepo.commands:
		require.Equal(t, 13, cmd.InputTokens)
		require.Equal(t, 5, cmd.OutputTokens)
		const expectedCost = 13*2.5e-6 + 5*15e-6
		require.InDelta(t, expectedCost, cmd.BalanceCost, 1e-8, "wallet debit must match delivered terminal usage")
		require.InDelta(t, expectedCost, cmd.OfficialCost, 1e-8)
	case <-time.After(3 * time.Second):
		t.Fatal("terminal cyber usage was not billed")
	}
	select {
	case usageLog := <-usageRepo.created:
		require.Equal(t, 13, usageLog.InputTokens)
		require.Equal(t, 5, usageLog.OutputTokens)
		require.Equal(t, service.RequestTypeCyberBlocked, usageLog.RequestType)
		require.InDelta(t, 13*2.5e-6+5*15e-6, usageLog.ActualCost, 1e-8)
	case <-time.After(3 * time.Second):
		t.Fatal("terminal cyber usage was not recorded")
	}
	select {
	case <-billingRepo.commands:
		t.Fatal("overflow cyber usage was charged twice")
	case <-time.After(2 * time.Second):
	}
	select {
	case <-usageRepo.created:
		t.Fatal("overflow cyber usage was recorded twice")
	case <-time.After(2 * time.Second):
	}
}
