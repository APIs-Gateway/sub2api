//go:build unit

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
