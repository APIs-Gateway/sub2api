package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIWSPrewarmUsageBillingRepoStub struct {
	commands chan *service.UsageBillingCommand
}

func (s *openAIWSPrewarmUsageBillingRepoStub) Apply(_ context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	s.commands <- cmd
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

func TestOpenAIResponsesWebSocket_PrewarmUsageHashesSurviveDelayedWorker(t *testing.T) {
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	firstBody := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"warm context"},{"role":"user","content":"first"}]}`)
	secondBody := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"warm context"},{"role":"user","content":"second"}]}`)
	firstHash := service.HashUsageRequestPayload(firstBody)
	secondHash := service.HashUsageRequestPayload(secondBody)
	require.NotEqual(t, firstHash, secondHash)
	proxyQueued := make(chan struct{})
	proxy := func(_ context.Context, _ *gin.Context, _ *coderws.Conn, _ *service.Account, _ string, _ []byte, hooks *service.OpenAIWSIngressHooks) error {
		hooks.AfterLocalPrewarm(1)
		for i, body := range [][]byte{firstBody, secondBody} {
			turn := i + 2
			hooks.BeforeBridgeUpstreamTurn(turn, body)
			hooks.AfterTurn(turn, &service.OpenAIForwardResult{
				RequestID:     []string{"resp_hash_first", "resp_hash_second"}[i],
				Model:         "gpt-5.4",
				UpstreamModel: "gpt-5.4",
				Usage:         service.OpenAIUsage{InputTokens: 1, OutputTokens: 1},
				Stream:        true,
				OpenAIWSMode:  true,
			}, nil)
		}
		close(proxyQueued)
		return service.NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "done", nil)
	}
	h := newOpenAIResponsesWebSocketAttributionHandlerWithProxy(t, cache, proxy, make(chan bool, 4), true)
	account := service.Account{
		ID: 9451, Name: "openai-ws-attribution", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-attribution", "base_url": "https://api.openai.example"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
		},
	}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
	billingRepo := &openAIWSPrewarmUsageBillingRepoStub{commands: make(chan *service.UsageBillingCommand, 2)}
	billingCfg := *h.cfg
	billingCfg.RunMode = config.RunModeStandard
	h.gatewayService = service.NewOpenAIGatewayService(
		&openAIWSUsageHandlerAccountRepoStub{account: account}, usageRepo,
		billingRepo, nil, nil, nil, nil, &billingCfg, nil, service.NewConcurrencyService(cache),
		service.NewBillingService(&billingCfg, nil), nil, h.billingCacheService, nil, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	pool := newUsageRecordTestPool(t)
	h.usageRecordWorkerPool = pool
	workerBlocked := make(chan struct{})
	releaseWorker := make(chan struct{})
	defer func() {
		select {
		case <-releaseWorker:
		default:
			close(releaseWorker)
		}
	}()
	pool.Submit(func(context.Context) {
		close(workerBlocked)
		<-releaseWorker
	})
	select {
	case <-workerBlocked:
	case <-time.After(3 * time.Second):
		t.Fatal("usage worker did not block before websocket turns")
	}
	handlerDone := make(chan struct{})
	server := newOpenAIResponsesWebSocketAttributionServerWithDone(t, h, handlerDone)
	defer server.Close()
	client := dialAndSendFirstResponseCreate(t, server.URL)
	defer func() { _ = client.CloseNow() }()
	select {
	case <-proxyQueued:
	case <-time.After(3 * time.Second):
		t.Fatal("two usage tasks were not queued")
	}
	close(releaseWorker)
	for _, want := range []string{firstHash, secondHash} {
		select {
		case cmd := <-billingRepo.commands:
			require.Equal(t, want, cmd.RequestPayloadHash)
		case <-time.After(3 * time.Second):
			t.Fatal("delayed usage task did not reach billing")
		}
	}
	_ = client.CloseNow()
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("websocket handler did not exit")
	}
}
