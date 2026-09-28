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

func (s *modelDowngradeRepoStub) TryBlockDowngradedModel(_ context.Context, _ int64, model string, _ time.Time, _ float64) (bool, error) {
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
	svc.HandleConfirmedModelDowngrade(context.Background(), account, "gpt-6-astra", "gpt-5.6-terra")
	svc.HandleConfirmedModelDowngrade(context.Background(), &Account{ID: 18, Platform: PlatformGrok}, "gpt-6-astra", "gpt-5.6-luna")
	require.Zero(t, counter.increments)
	svc.HandleConfirmedModelDowngrade(context.Background(), account, "gpt-6-astra", "gpt-5.6-luna")
	require.Equal(t, 1, counter.increments)
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
