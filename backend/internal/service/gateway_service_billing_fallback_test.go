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

// billableModelCandidates 是 billableModelWithFallback 抽出来的候选顺序，纯函数；
// 这里钉住它的规则：选定的计费模型原样排第一（不去空白、不去重），兜底候选先去首尾空白，再跳过空串与等于计费模型的。
func TestBillableModelCandidates_OrderTrimAndSkipRules(t *testing.T) {
	require.Equal(t, []string{"alias"}, billableModelCandidates("alias"))
	require.Equal(t, []string{"alias", "a", "b"}, billableModelCandidates("alias", "a", "b"))
	require.Equal(t, []string{"alias", "a"}, billableModelCandidates("alias", "", "  ", "alias", " a "),
		"empty after trimming and equal to the billing model are skipped; fallbacks are trimmed")
	require.Equal(t, []string{" alias ", "alias"}, billableModelCandidates(" alias ", "alias"),
		"the billing model is kept as given, so a fallback that only equals it after trimming is not a duplicate")
	require.Equal(t, []string{"a", "b", "b"}, billableModelCandidates("a", "b", "b"),
		"fallbacks are not deduplicated among themselves")
	require.Equal(t, []string{""}, billableModelCandidates(""), "always at least the billing model itself")
}

// 抽出候选顺序之后网关的结果不变：返回的仍是去过空白的兜底候选；选定模型本身有价时原样返回（含空白）。
func TestBillableModelWithFallback_ReturnsTrimmedFallbackAndUntrimmedBillingModel(t *testing.T) {
	svc := newFallbackOnlyBillingGatewayService()
	ctx := context.Background()
	require.Equal(t, "claude-sonnet-4-20250514",
		svc.billableModelWithFallback(ctx, nil, "totally-unknown-alias-xyz", "  claude-sonnet-4-20250514 "))
	require.Equal(t, " claude-sonnet-4-20250514",
		svc.billableModelWithFallback(ctx, nil, " claude-sonnet-4-20250514", "claude-opus-4-20250514"))
	require.Equal(t, "claude-sonnet-4-20250514",
		svc.billableModelWithFallback(ctx, nil, "", "claude-sonnet-4-20250514"), "an empty billing model never resolves; the first priced fallback wins")
}
