package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const wsGroupBillingKey = "sk-ws-group-billing"

type wsGroupBillingKeyRepo struct {
	service.APIKeyRepository
	mu     sync.Mutex
	key    service.APIKey
	group  service.Group
	err    error
	reads  int
}

func newWSGroupBillingKeyRepo() *wsGroupBillingKeyRepo {
	groupID := int64(4201)
	return &wsGroupBillingKeyRepo{
		key: service.APIKey{
			ID: 1801, UserID: 1701, Key: wsGroupBillingKey, Status: service.StatusActive,
			GroupID: &groupID, User: &service.User{ID: 1701, Status: service.StatusActive, Balance: 100},
		},
		group: service.Group{
			ID: groupID, Name: "ws-group-billing", Platform: service.PlatformOpenAI,
			Status: service.StatusActive, Hydrated: true, RateMultiplier: 3,
		},
	}
}

func (r *wsGroupBillingKeyRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	if key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	apiKey := r.key
	user := *r.key.User
	group := r.group
	apiKey.User = &user
	apiKey.Group = &group
	return &apiKey, nil
}

func (r *wsGroupBillingKeyRepo) ListKeysByGroupID(_ context.Context, _ int64) ([]string, error) {
	return []string{wsGroupBillingKey}, nil
}

func (r *wsGroupBillingKeyRepo) change(keyService *service.APIKeyService, fn func(*service.APIKey, *service.Group)) {
	r.mu.Lock()
	fn(&r.key, &r.group)
	r.mu.Unlock()
	keyService.InvalidateAuthCacheByGroupID(context.Background(), 4201)
}

func newWSGroupBillingKeyService(repo *wsGroupBillingKeyRepo) *service.APIKeyService {
	return service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
}

func TestOpenAIResponsesWebSocket_LaterTurnUsesCurrentGroupBilling(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			repo := newWSGroupBillingKeyRepo()
			keyService := newWSGroupBillingKeyService(repo)
			billingRepo := &openAIWSPrewarmUsageBillingRepoStub{commands: make(chan *service.UsageBillingCommand, 2)}
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":false}`,
				secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":false}`,
				apiKeyService: keyService,
				apiKeyCredential: wsGroupBillingKey,
				ingressMode: mode,
				billingRepo: billingRepo,
				afterFirstResponse: func() error {
					repo.change(keyService, func(_ *service.APIKey, group *service.Group) {
						group.RateMultiplier = 0.3
					})
					return nil
				},
			})
			require.Len(t, got.logs, 2)
			require.InDelta(t, 3.0, got.logs[0].RateMultiplier, 1e-12)
			require.InDelta(t, 0.3, got.logs[1].RateMultiplier, 1e-12)
			require.Greater(t, got.logs[0].ActualCost, 0.0)
			require.InDelta(t, got.logs[0].ActualCost/10, got.logs[1].ActualCost, 1e-12)
			first := <-billingRepo.commands
			second := <-billingRepo.commands
			require.NotEqual(t, first.RequestID, second.RequestID)
			require.InDelta(t, 3.0, first.RateMultiplier, 1e-12)
			require.InDelta(t, 0.3, second.RateMultiplier, 1e-12)
			require.Greater(t, first.OfficialCost, 0.0)
			require.InDelta(t, first.OfficialCost, second.OfficialCost, 1e-12)
		})
	}
}

func TestOpenAIResponsesWebSocket_LaterTurnKeepsConnectionGroupWhenKeyMovesOrRefreshFails(t *testing.T) {
	for name, change := range map[string]func(*wsGroupBillingKeyRepo, *service.APIKeyService){
		"moved": func(repo *wsGroupBillingKeyRepo, keyService *service.APIKeyService) {
			repo.change(keyService, func(key *service.APIKey, group *service.Group) {
				other := int64(4202)
				key.GroupID = &other
				group.ID = other
				group.RateMultiplier = 0.3
			})
		},
		"lookup error": func(repo *wsGroupBillingKeyRepo, keyService *service.APIKeyService) {
			repo.change(keyService, func(_ *service.APIKey, group *service.Group) {
				group.RateMultiplier = 0.3
				repo.err = errors.New("auth store unavailable")
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newWSGroupBillingKeyRepo()
			keyService := newWSGroupBillingKeyService(repo)
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":false}`,
				secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":false}`,
				apiKeyService: keyService,
				apiKeyCredential: wsGroupBillingKey,
				afterFirstResponse: func() error {
					change(repo, keyService)
					return nil
				},
			})
			require.Len(t, got.logs, 2)
			require.InDelta(t, 3.0, got.logs[1].RateMultiplier, 1e-12)
		})
	}
}

func TestRefreshOpenAIWSTurnBillingKeyKeepsConnectionIdentity(t *testing.T) {
	repo := newWSGroupBillingKeyRepo()
	keyService := newWSGroupBillingKeyService(repo)
	connectionKey, err := keyService.GetByKey(context.Background(), wsGroupBillingKey)
	require.NoError(t, err)
	repo.change(keyService, func(key *service.APIKey, group *service.Group) {
		key.Quota = 99
		group.RateMultiplier = 0.3
	})
	got := refreshOpenAIWSTurnBillingKey(context.Background(), keyService, connectionKey)
	require.NotSame(t, connectionKey, got)
	require.Same(t, connectionKey.User, got.User)
	require.Equal(t, connectionKey.Quota, got.Quota)
	require.InDelta(t, 0.3, got.Group.RateMultiplier, 1e-12)
	require.InDelta(t, 3.0, connectionKey.Group.RateMultiplier, 1e-12)

	var keys openAIWSTurnBillingKeys
	require.Same(t, connectionKey, keys.forTurn(1, connectionKey))
	keys.set(2, got)
	keys.set(3, connectionKey)
	require.Same(t, got, keys.forTurn(2, connectionKey), "turn 3 can start before turn 2 is recorded")
}

func TestOpenAIResponsesWebSocket_LaterCyberTurnBillsCurrentGroupOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newWSGroupBillingKeyRepo()
	keyService := newWSGroupBillingKeyService(repo)
	connectionKey, err := keyService.GetByKey(context.Background(), wsGroupBillingKey)
	require.NoError(t, err)

	groupID := int64(4201)
	account := service.Account{
		ID: 9901, Name: "ws-cyber-billing", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-upstream", "base_url": "https://api.openai.example"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough,
		},
	}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	billingCfg := *cfg
	billingCfg.RunMode = config.RunModeStandard
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
	billingRepo := &openAIWSPrewarmUsageBillingRepoStub{commands: make(chan *service.UsageBillingCommand, 2)}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		&openAIWSUsageHandlerAccountRepoStub{account: account}, usageRepo,
		billingRepo, nil, nil, nil, nil, &billingCfg, nil, nil, service.NewBillingService(&billingCfg, nil),
		nil, billingCache, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := &OpenAIGatewayHandler{
		gatewayService: gateway, billingCacheService: billingCache, apiKeyService: keyService,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		cfg: cfg,
	}
	h.responsesWebSocketProxy = func(_ context.Context, c *gin.Context, _ *coderws.Conn, _ *service.Account, _ string, _ []byte, hooks *service.OpenAIWSIngressHooks) error {
		hooks.AfterTurn(1, &service.OpenAIForwardResult{
			RequestID: "resp_ws_first", Model: "gpt-5.4", UpstreamModel: "gpt-5.4",
			Usage: service.OpenAIUsage{InputTokens: 2, OutputTokens: 1}, OpenAIWSMode: true,
		}, nil)
		repo.change(keyService, func(_ *service.APIKey, group *service.Group) { group.RateMultiplier = 0.3 })
		if beforeErr := hooks.BeforeTurn(2); beforeErr != nil {
			return beforeErr
		}
		service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{
			Message: "blocked", UpstreamStatus: 200, UpstreamInTok: 2, UpstreamOutTok: 1,
		})
		hooks.AfterTurn(2, nil, errors.New("upstream cyber block"))
		return service.NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "done", nil)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), connectionKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: connectionKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.GET("/openai/v1/responses", h.ResponsesWebSocket)
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","stream":false}`)))

	logs := make([]*service.UsageLog, 0, 2)
	for len(logs) < 2 {
		select {
		case log := <-usageRepo.created:
			logs = append(logs, log)
		case <-ctx.Done():
			t.Fatal("timed out waiting for both normal and cyber billing rows")
		}
	}
	var normal, cyber *service.UsageLog
	for _, log := range logs {
		if log.RequestType == service.RequestTypeCyberBlocked {
			cyber = log
		} else {
			normal = log
		}
	}
	require.NotNil(t, normal)
	require.NotNil(t, cyber)
	require.Equal(t, &groupID, cyber.GroupID)
	require.InDelta(t, 3.0, normal.RateMultiplier, 1e-12)
	require.InDelta(t, 0.3, cyber.RateMultiplier, 1e-12)
	require.Greater(t, cyber.ActualCost, 0.0)
	commands := []*service.UsageBillingCommand{<-billingRepo.commands, <-billingRepo.commands}
	require.NotEqual(t, commands[0].RequestID, commands[1].RequestID)
	rates := []float64{commands[0].RateMultiplier, commands[1].RateMultiplier}
	require.ElementsMatch(t, []float64{3.0, 0.3}, rates)
	for _, command := range commands {
		require.Greater(t, command.OfficialCost, 0.0)
	}
	select {
	case extra := <-usageRepo.created:
		t.Fatalf("cyber turn was billed twice: %+v", extra)
	default:
	}
	select {
	case extra := <-billingRepo.commands:
		t.Fatalf("cyber turn was deducted twice: %+v", extra)
	default:
	}
}
