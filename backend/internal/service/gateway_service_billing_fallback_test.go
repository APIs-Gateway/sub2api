package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// newFallbackOnlyBillingGatewayService builds a GatewayService whose only
// pricing source is BillingService's hardcoded fallback table (no channel
// resolver, no dynamic pricing service, no DB) — enough to exercise
// hasResolvableTokenPricing/billableModelWithFallback deterministically.
func newFallbackOnlyBillingGatewayService() *GatewayService {
	return &GatewayService{billingService: NewBillingService(&config.Config{}, nil)}
}

func TestHasResolvableTokenPricing_EmptyModelIsUnresolvable(t *testing.T) {
	svc := newFallbackOnlyBillingGatewayService()
	require.False(t, svc.hasResolvableTokenPricing(context.Background(), "   ", nil))
}

func TestHasResolvableTokenPricing_KnownModelResolvesViaFallbackPricing(t *testing.T) {
	svc := newFallbackOnlyBillingGatewayService()
	require.True(t, svc.hasResolvableTokenPricing(context.Background(), "claude-sonnet-4-20250514", nil))
}

func TestHasResolvableTokenPricing_UnknownModelIsUnresolvable(t *testing.T) {
	svc := newFallbackOnlyBillingGatewayService()
	require.False(t, svc.hasResolvableTokenPricing(context.Background(), "totally-unknown-alias-xyz", nil))
}

func TestHasResolvableTokenPricing_NoBillingServiceIsUnresolvable(t *testing.T) {
	svc := &GatewayService{}
	require.False(t, svc.hasResolvableTokenPricing(context.Background(), "claude-sonnet-4-20250514", nil))
}

func TestBillableModelWithFallback_KeepsResolvableModelUnchanged(t *testing.T) {
	svc := newFallbackOnlyBillingGatewayService()
	got := svc.billableModelWithFallback(context.Background(), nil, "claude-sonnet-4-20250514", "some-other-model")
	require.Equal(t, "claude-sonnet-4-20250514", got)
}

func TestBillableModelWithFallback_FallsBackToFirstResolvableCandidate(t *testing.T) {
	svc := newFallbackOnlyBillingGatewayService()
	got := svc.billableModelWithFallback(context.Background(), nil, "totally-unknown-alias-xyz", "also-unknown", "claude-sonnet-4-20250514")
	require.Equal(t, "claude-sonnet-4-20250514", got)
}

func TestBillableModelWithFallback_SkipsEmptyAndIdenticalCandidates(t *testing.T) {
	svc := newFallbackOnlyBillingGatewayService()
	got := svc.billableModelWithFallback(context.Background(), nil, "totally-unknown-alias-xyz", "", "totally-unknown-alias-xyz", "claude-sonnet-4-20250514")
	require.Equal(t, "claude-sonnet-4-20250514", got)
}

func TestBillableModelWithFallback_KeepsOriginalWhenNothingResolves(t *testing.T) {
	svc := newFallbackOnlyBillingGatewayService()
	got := svc.billableModelWithFallback(context.Background(), nil, "totally-unknown-alias-xyz", "also-unknown")
	require.Equal(t, "totally-unknown-alias-xyz", got)
}
