package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSelectCompositeBillingModel(t *testing.T) {
	composite := &Group{Platform: PlatformComposite}
	regular := &Group{Platform: PlatformOpenAI}

	require.Equal(t, "claude-opus-4-7", selectCompositeBillingModel(
		composite, "all/claude", "claude-opus-4-7", nil,
	))
	require.Equal(t, "all/claude", selectCompositeBillingModel(
		composite, "all/claude", "claude-opus-4-7", func() bool { return true },
	))
	require.Equal(t, "all/claude", selectCompositeBillingModel(
		regular, "all/claude", "claude-opus-4-7", nil,
	))
	require.Equal(t, "all/claude", selectCompositeBillingModel(
		composite, "all/claude", "", nil,
	))
}

func TestConcreteForwardResultBillingModelPrefersUpstream(t *testing.T) {
	require.Equal(t, "claude-opus-4-7", concreteForwardResultBillingModel("all/claude", "claude-opus-4-7"))
	require.Equal(t, "all/claude", concreteForwardResultBillingModel("all/claude", ""))
}

func TestGatewayServiceBillableModelWithFallback(t *testing.T) {
	cfg := &config.Config{}
	svc := &GatewayService{billingService: NewBillingService(cfg, nil)}
	apiKey := &APIKey{Group: &Group{Platform: PlatformComposite}}

	require.Equal(t, "claude-sonnet-4", svc.billableModelWithFallback(
		context.Background(), apiKey, "team/best", "claude-sonnet-4",
	))
	require.Equal(t, "claude-sonnet-4", svc.billableModelWithFallback(
		context.Background(), apiKey, "claude-sonnet-4", "claude-opus-4-7",
	))
	require.Equal(t, "team/best", svc.billableModelWithFallback(
		context.Background(), apiKey, "team/best", "another/alias",
	))
}

func TestGatewayServiceCompositeBillingBoundaries(t *testing.T) {
	ctx := context.Background()
	apiKey := &APIKey{Group: &Group{Platform: PlatformComposite}}

	// Exercise the nil pricing dependencies used by requests without a group
	// resolver, while keeping this test independent of database-backed pricing.
	noBilling := &GatewayService{}
	require.Nil(t, noBilling.resolveChannelPricing(ctx, "claude-sonnet-4", nil))
	require.False(t, noBilling.hasResolvableTokenPricing(ctx, "claude-sonnet-4", apiKey))
	require.False(t, noBilling.hasResolvableTokenPricing(ctx, "claude-sonnet-4", nil))

	svc := &GatewayService{billingService: NewBillingService(&config.Config{}, nil)}
	require.False(t, svc.hasResolvableTokenPricing(ctx, "", apiKey))
	require.Equal(t, "team/best", svc.billableModelWithFallback(ctx, apiKey, "team/best", "", "team/best"))
}

func TestGatewayServiceRecordUsageCompositeAliasUsesConcreteModel(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	usageRepo := &compositeBillingUsageLogCapture{}
	svc := &GatewayService{
		cfg:             cfg,
		billingService:  NewBillingService(cfg, nil),
		deferredService: &DeferredService{},
		usageLogRepo:    usageRepo,
	}
	apiKey := &APIKey{
		ID: 1,
		Group: &Group{
			Platform: PlatformComposite,
		},
	}

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "composite-gateway-concrete-billing",
			Model:         "all/claude",
			UpstreamModel: "claude-opus-4-7",
			Usage:         ClaudeUsage{InputTokens: 1, OutputTokens: 1},
		},
		APIKey:  apiKey,
		User:    &User{ID: 2},
		Account: &Account{ID: 3},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.log)
	expected, err := svc.billingService.CalculateCost("claude-opus-4-7", UsageTokens{
		InputTokens:  1,
		OutputTokens: 1,
	}, 1)
	require.NoError(t, err)
	require.InDelta(t, expected.ActualCost, usageRepo.log.ActualCost, 1e-12)
}

type compositeBillingUsageLogCapture struct {
	UsageLogRepository
	log *UsageLog
}

func (r *compositeBillingUsageLogCapture) CreateBestEffort(_ context.Context, log *UsageLog) error {
	r.log = log
	return nil
}
