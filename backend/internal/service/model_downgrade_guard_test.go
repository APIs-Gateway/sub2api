//go:build unit

package service

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelDowngradeCounterStub struct {
	count      int64
	increments int
	resets     int
	models     []string
}

func (s *modelDowngradeCounterStub) IncrementModelDowngradeCount(_ context.Context, _ int64, model string, _ int) (int64, error) {
	s.increments++
	s.models = append(s.models, model)
	s.count++
	return s.count, nil
}

func (s *modelDowngradeCounterStub) ResetModelDowngradeCount(context.Context, int64, string) error {
	s.resets++
	s.count = 0
	return nil
}

type modelDowngradeRepoStub struct {
	AccountRepository
	blocks []string
}

func (s *modelDowngradeRepoStub) TryBlockDowngradedModel(_ context.Context, _ int64, _, model string, _ time.Time, _ float64, _ bool, _ ModelDowngradeCandidateFilter) (bool, error) {
	s.blocks = append(s.blocks, model)
	return true, nil
}

func TestModelDowngradeGuardCountsRejectedAttemptsOnly(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ModelDowngradeGuard = config.GatewayModelDowngradeGuardConfig{
		Enabled: true, ThresholdCount: 2, WindowMinutes: 30, BlockHours: 24, MaxBlockedRatio: 0.3,
		Pairs: []config.GatewayModelDowngradePair{{SentModel: "gpt-6-astra", ResponseModel: "gpt-5.6-luna"}},
	}
	counter := &modelDowngradeCounterStub{}
	repo := &modelDowngradeRepoStub{}
	limiter := &RateLimitService{cfg: cfg, accountRepo: repo, modelDowngradeCounter: counter}
	svc := &OpenAIGatewayService{cfg: cfg, rateLimitService: limiter}
	account := &Account{ID: 17, Platform: PlatformOpenAI}
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		return c
	}

	// A late model cannot be retracted. It remains an observation, not a
	// quarantine signal.
	require.Nil(t, svc.checkUpstreamModelMismatch(newContext(), account, "", nil,
		"gpt-6-astra", "gpt-5.6-luna", true, false, OpenAIUsage{}))
	require.Zero(t, counter.increments)

	cfg.Gateway.DisableUpstreamModelMismatchBlock = true
	require.Nil(t, svc.checkUpstreamModelMismatch(newContext(), account, "", nil,
		"gpt-6-astra", "gpt-5.6-luna", true, true, OpenAIUsage{}))
	require.Zero(t, counter.increments)
	cfg.Gateway.DisableUpstreamModelMismatchBlock = false

	first := newContext()
	require.NotNil(t, svc.checkUpstreamModelMismatch(first, account, "", nil,
		"gpt-6-astra", "gpt-5.6-luna", true, true, OpenAIUsage{}))
	// A duplicate report in one attempt must not consume another threshold hit.
	require.NotNil(t, svc.checkUpstreamModelMismatch(first, account, "", nil,
		"gpt-6-astra", "gpt-5.6-luna", true, true, OpenAIUsage{}))
	require.Equal(t, 1, counter.increments)
	require.Empty(t, repo.blocks)
	ClearOpsUpstreamModelMismatch(first)
	// A pool-mode retry is a real second upstream attempt.
	require.NotNil(t, svc.checkUpstreamModelMismatch(first, account, "", nil,
		"gpt-6-astra", "gpt-5.6-luna", true, true, OpenAIUsage{}))
	require.Equal(t, 2, counter.increments)
	require.Equal(t, []string{"gpt-6-astra"}, repo.blocks)
	require.Equal(t, 1, counter.resets)
}

func TestModelDowngradeGuardMatchesExplicitPairOnly(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ModelDowngradeGuard = config.GatewayModelDowngradeGuardConfig{
		Enabled: true,
		Pairs: []config.GatewayModelDowngradePair{{SentModel: "gpt-6-astra", ResponseModel: "gpt-5.6-luna"}},
	}
	counter := &modelDowngradeCounterStub{}
	svc := &RateLimitService{cfg: cfg, modelDowngradeCounter: counter}
	account := &Account{ID: 17, Platform: PlatformOpenAI}
	filter := func(context.Context, *Account, *int64) bool { return true }
	svc.HandleConfirmedModelDowngrade(context.Background(), account, "gpt-6-astra", "gpt-6-astra", "gpt-5.6-terra", filter)
	svc.HandleConfirmedModelDowngrade(context.Background(), &Account{ID: 18, Platform: PlatformGrok}, "gpt-6-astra", "gpt-6-astra", "gpt-5.6-luna", filter)
	require.Zero(t, counter.increments)
	svc.HandleConfirmedModelDowngrade(context.Background(), account, "gpt-6-astra", "gpt-6-astra", "gpt-5.6-luna", filter)
	require.Equal(t, 1, counter.increments)
	svc.HandleConfirmedModelDowngrade(WithOpenAIForcedAccountRouting(context.Background(), account.ID), account,
		"gpt-6-astra", "gpt-6-astra", "gpt-5.6-luna", filter)
	require.Equal(t, 1, counter.increments)
}

func TestModelDowngradeGuardSkipsLaterWebSocketModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ModelDowngradeGuard = config.GatewayModelDowngradeGuardConfig{
		Enabled: true, ThresholdCount: 2,
		Pairs: []config.GatewayModelDowngradePair{{SentModel: "gpt-6-astra", ResponseModel: "gpt-5.6-luna"}},
	}
	counter := &modelDowngradeCounterStub{}
	limiter := &RateLimitService{cfg: cfg, accountRepo: &modelDowngradeRepoStub{}, modelDowngradeCounter: counter}
	svc := &OpenAIGatewayService{cfg: cfg, rateLimitService: limiter}
	account := &Account{ID: 17, Platform: PlatformOpenAI}
	newWebSocketContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		request := httptest.NewRequest("GET", "/v1/responses", nil)
		request.Header.Set("Upgrade", "websocket")
		ctx := WithModelDowngradeSelectionModel(request.Context(), "gpt-6-astra-channel")
		request = request.WithContext(WithModelDowngradeWebSocketInitialModel(ctx, "gpt-6-astra"))
		c.Request = request
		return c
	}
	require.NotNil(t, svc.checkUpstreamModelMismatch(newWebSocketContext(), account, "", nil,
		"gpt-6-astra", "gpt-5.6-luna", true, true, OpenAIUsage{}, "gpt-6-astra"))
	require.Equal(t, 1, counter.increments)
	// The forwarded account may still use the first-turn selector after a
	// later turn changes its client model. Keep immediate mismatch failover,
	// but do not consume a cross-request quarantine hit for that new model.
	require.NotNil(t, svc.checkUpstreamModelMismatch(newWebSocketContext(), account, "", nil,
		"gpt-6-astra", "gpt-5.6-luna", true, true, OpenAIUsage{}, "gpt-5.4"))
	require.Equal(t, 1, counter.increments)
}

func TestModelDowngradeCandidateRequiresResponsesCapability(t *testing.T) {
	svc := &OpenAIGatewayService{}
	ctx := context.Background()
	filter := svc.modelDowngradeCandidateFilter(ctx, "gpt-6-astra", "/v1/responses", OpenAIUpstreamTransportAny)
	candidate := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Credentials: map[string]any{}, Extra: map[string]any{}}
	require.False(t, filter(ctx, candidate, nil), "raw Chat fallback cannot witness native Responses availability")
	candidate.Extra["openai_responses_supported"] = true
	require.True(t, filter(ctx, candidate, nil))
	groupID := int64(123)
	candidate.Groups = []*Group{{ID: groupID, RequirePrivacySet: true}}
	require.False(t, filter(ctx, candidate, &groupID), "group privacy requirement excludes this candidate")
	candidate.Extra["privacy_mode"] = PrivacyModeTrainingOff
	require.True(t, filter(ctx, candidate, &groupID))

	wsCfg := &config.Config{}
	wsCfg.Gateway.OpenAIWS.Enabled = true
	wsCfg.Gateway.OpenAIWS.APIKeyEnabled = true
	wsCfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	wsSvc := &OpenAIGatewayService{cfg: wsCfg}
	wsFilter := wsSvc.modelDowngradeCandidateFilter(ctx, "gpt-6-astra", "/v1/responses", OpenAIUpstreamTransportResponsesWebsocketV2)
	require.False(t, wsFilter(ctx, candidate, &groupID), "HTTP-only account cannot witness a WebSocket pool")
	candidate.Extra["openai_apikey_responses_websockets_v2_enabled"] = true
	require.True(t, wsFilter(ctx, candidate, &groupID))
}

func TestModelDowngradeCandidateUsesSelectorModelAfterChannelAlias(t *testing.T) {
	ctx := WithModelDowngradeSelectionModel(context.Background(), "gpt-6-astra-channel")
	_, hasForwardModel := openAIForwardModelFromContext(ctx)
	require.False(t, hasForwardModel, "guard metadata must not change forwarding or cooldown model keys")
	reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	limited := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{"openai_passthrough": true, "openai_responses_supported": true,
			"model_rate_limits": map[string]any{"gpt-6-astra-channel": map[string]any{"rate_limit_reset_at": reset}}}}
	require.False(t, limited.isModelRateLimitedWithContext(ctx, "gpt-6-astra"), "selector metadata cannot change existing cooldown lookup")
	require.True(t, limited.isModelRateLimitedWithContext(WithOpenAIForwardModel(ctx, "gpt-6-astra-channel", false), "gpt-6-astra"))
	svc := &OpenAIGatewayService{}
	filter := svc.modelDowngradeCandidateFilter(ctx, "gpt-6-astra", "/v1/chat/completions", OpenAIUpstreamTransportAny)
	candidate := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"}},
		Extra: map[string]any{},
	}
	require.False(t, filter(ctx, candidate, nil), "the original client model is not the selector's channel alias")
	candidate.Credentials["model_mapping"] = map[string]any{"gpt-6-astra-channel": "gpt-6-astra"}
	require.True(t, filter(ctx, candidate, nil))
}

func TestOpenAIModelRateLimitUsesForwardedModelKey(t *testing.T) {
	reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	limit := map[string]any{"rate_limit_reset_at": reset, "reason": ModelDowngradeGuardReason}
	alias := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{"model_rate_limits": map[string]any{"gpt-5.4": limit}}}
	require.True(t, alias.isModelRateLimitedWithContext(context.Background(), "gpt-5.4-high"))

	passthrough := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-astra": "gpt-5.6-luna"}},
		Extra: map[string]any{"openai_passthrough": true, "openai_responses_supported": true, "model_rate_limits": map[string]any{"gpt-6-astra": limit}}}
	require.True(t, passthrough.isModelRateLimitedWithContext(context.Background(), "gpt-6-astra"))
	passthrough.Extra["model_rate_limits"] = map[string]any{"gpt-5.6-luna": limit}
	require.False(t, passthrough.isModelRateLimitedWithContext(context.Background(), "gpt-6-astra"))

	compact := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"}},
		Extra: map[string]any{"model_rate_limits": map[string]any{"gpt-5.4-openai-compact": limit}}}
	ctx := WithOpenAIForwardModel(context.Background(), "gpt-5.4", true)
	require.True(t, compact.isModelRateLimitedWithContext(ctx, "gpt-5.4"))
}
