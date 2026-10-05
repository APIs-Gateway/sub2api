//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// hvoy 报价单改走 PriceQuoter 之后的对照：没有渠道覆盖时，7 个模型的展示价与旧口径（LiteLLM 原价 × 分组倍率 ÷ 支付倍率）
// 逐位相同；有渠道覆盖时只有被覆盖的那个模型变化。

var hvoyTestNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func hvoyTestGroup(rate float64) []Group {
	return []Group{{ID: 10, Name: "codex  plus", Platform: PlatformOpenAI, RateMultiplier: rate}}
}

func TestBuildHvoyProviderPricingQuoted_MatchesLegacyWithoutOverrides(t *testing.T) {
	pricing := NewPricingService(nil, nil)
	groups := hvoyTestGroup(1.4)
	fx := newPriceCatalogFixture(pricing, nil, groups, nil, nil)

	got, err := pricing.BuildHvoyProviderPricingQuoted(context.Background(), fx.quoter, hvoyGroupListerStub{groups: groups}, 0.5, "Site", "https://api.example.com", hvoyTestNow)
	require.NoError(t, err)
	want := pricing.BuildHvoyProviderPricing(0.5, map[string]float64{"codex plus": 1.4}, "Site", "https://api.example.com", hvoyTestNow)

	require.Len(t, got.Data.Models, 7)
	for i := range want.Data.Models {
		require.Equal(t, want.Data.Models[i], got.Data.Models[i], want.Data.Models[i].ModelName)
		require.True(t, got.Data.Models[i].Enabled)
	}
	require.Equal(t, want, got)
}

func TestBuildHvoyProviderPricingQuoted_NilQuoterKeepsLegacy(t *testing.T) {
	pricing := NewPricingService(nil, nil)
	groups := hvoyTestGroup(1.4)

	got, err := pricing.BuildHvoyProviderPricingQuoted(context.Background(), nil, hvoyGroupListerStub{groups: groups}, 0.5, "", "", hvoyTestNow)
	require.NoError(t, err)
	require.Equal(t, pricing.BuildHvoyProviderPricing(0.5, map[string]float64{"codex plus": 1.4}, "", "", hvoyTestNow), got)

	// 没有分组仓库时与旧口径的「分组倍率缺失」一致：按 1 倍出价并带说明。
	noGroups, err := pricing.BuildHvoyProviderPricingQuoted(context.Background(), nil, nil, 0.5, "", "", hvoyTestNow)
	require.NoError(t, err)
	require.Equal(t, "group rate multiplier unavailable", noGroups.Data.Models[1].Note)
}

// 渠道给 codex plus 分组的某个模型配了价，只有这个模型的展示价跟着变，其余 6 个与旧口径相同。
func TestBuildHvoyProviderPricingQuoted_ChannelOverrideChangesOnlyThatModel(t *testing.T) {
	pricing := NewPricingService(nil, nil)
	groups := hvoyTestGroup(1.4)
	override := 1e-6
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{10},
		ModelPricing: []ChannelModelPricing{{
			Platform: PlatformOpenAI, Models: []string{"gpt-5.6-sol"}, BillingMode: BillingModeToken, InputPrice: &override,
		}},
	}}
	fx := newPriceCatalogFixture(pricing, channels, groups, nil, nil)

	got, err := pricing.BuildHvoyProviderPricingQuoted(context.Background(), fx.quoter, hvoyGroupListerStub{groups: groups}, 0.5, "", "", hvoyTestNow)
	require.NoError(t, err)
	legacy := pricing.BuildHvoyProviderPricing(0.5, map[string]float64{"codex plus": 1.4}, "", "", hvoyTestNow)

	rate := 1.4
	for i := range legacy.Data.Models {
		if legacy.Data.Models[i].ModelName != "gpt-5.6-sol" {
			require.Equal(t, legacy.Data.Models[i], got.Data.Models[i], legacy.Data.Models[i].ModelName)
			continue
		}
		changed := got.Data.Models[i]
		require.True(t, changed.Enabled)
		require.Equal(t, usdPerTokenToCNYPerMTok(override*rate, 0.5), changed.InputPrice)
		require.NotEqual(t, legacy.Data.Models[i].InputPrice, changed.InputPrice)
		// 渠道没覆盖的字段保持官方价。
		require.Equal(t, legacy.Data.Models[i].OutputPrice, changed.OutputPrice)
		require.Equal(t, legacy.Data.Models[i].CacheInputPrice, changed.CacheInputPrice)
	}
}

// 分组里关闭的模型不再对 hvoy 报价（旧口径不看准入，会照常报价）。
func TestBuildHvoyProviderPricingQuoted_ClosedModelIsDisabled(t *testing.T) {
	pricing := NewPricingService(nil, nil)
	groups := hvoyTestGroup(1.4)
	fx := newPriceCatalogFixture(pricing, nil, groups, nil, nil)
	fx.resolver.policyOverride = stagedStubPolicy{
		GroupPolicy: legacyPolicy{cs: fx.cs},
		v2:          map[int64]bool{10: true},
		closed:      map[string]bool{"gpt-6-astra": true},
	}

	got, err := pricing.BuildHvoyProviderPricingQuoted(context.Background(), fx.quoter, hvoyGroupListerStub{groups: groups}, 0.5, "", "", hvoyTestNow)
	require.NoError(t, err)
	for _, model := range got.Data.Models {
		if model.ModelName == "gpt-6-astra" {
			require.False(t, model.Enabled)
			require.Equal(t, "model not available in group", model.Note)
			require.Zero(t, model.InputPrice)
			continue
		}
		require.True(t, model.Enabled, model.ModelName)
	}
}

// 找不到 codex plus 分组或分组倍率无效：保持旧口径（按 1 倍出价并带说明），不因为接管而改变已发布的价格。
func TestBuildHvoyProviderPricingQuoted_MissingOrInvalidGroupKeepsLegacy(t *testing.T) {
	pricing := NewPricingService(nil, nil)

	missing := []Group{{ID: 11, Name: "other", Platform: PlatformOpenAI, RateMultiplier: 2}}
	fx := newPriceCatalogFixture(pricing, nil, missing, nil, nil)
	got, err := pricing.BuildHvoyProviderPricingQuoted(context.Background(), fx.quoter, hvoyGroupListerStub{groups: missing}, 0.5, "", "", hvoyTestNow)
	require.NoError(t, err)
	require.Equal(t, pricing.BuildHvoyProviderPricing(0.5, nil, "", "", hvoyTestNow), got)
	require.Equal(t, "group rate multiplier unavailable", got.Data.Models[1].Note)

	invalid := hvoyTestGroup(0)
	fx = newPriceCatalogFixture(pricing, nil, invalid, nil, nil)
	got, err = pricing.BuildHvoyProviderPricingQuoted(context.Background(), fx.quoter, hvoyGroupListerStub{groups: invalid}, 0.5, "", "", hvoyTestNow)
	require.NoError(t, err)
	require.Equal(t, pricing.BuildHvoyProviderPricing(0.5, map[string]float64{"codex plus": 0}, "", "", hvoyTestNow), got)
	require.Equal(t, "group rate multiplier invalid", got.Data.Models[1].Note)
}

func TestBuildHvoyProviderPricingQuoted_GroupListErrorPropagates(t *testing.T) {
	pricing := NewPricingService(nil, nil)
	fx := newPriceCatalogFixture(pricing, nil, hvoyTestGroup(1), nil, nil)
	_, err := pricing.BuildHvoyProviderPricingQuoted(context.Background(), fx.quoter, hvoyGroupListerStub{err: errors.New("db down")}, 1, "", "", hvoyTestNow)
	require.Error(t, err)
}

// 没有任何价格的模型：Enabled=false，说明与旧口径相同。
func TestBuildHvoyProviderPricingQuoted_UnpricedModelIsDisabled(t *testing.T) {
	original := hvoyProviderPricingModels
	hvoyProviderPricingModels = []hvoyProviderPricingModelRef{{modelName: "missing-model", groupName: HvoyProviderPricingGroupName}}
	t.Cleanup(func() { hvoyProviderPricingModels = original })

	pricing := NewPricingService(nil, nil)
	groups := hvoyTestGroup(1.4)
	fx := newPriceCatalogFixture(pricing, nil, groups, nil, nil)

	got, err := pricing.BuildHvoyProviderPricingQuoted(context.Background(), fx.quoter, hvoyGroupListerStub{groups: groups}, 1, "", "", hvoyTestNow)
	require.NoError(t, err)
	require.Len(t, got.Data.Models, 1)
	require.False(t, got.Data.Models[0].Enabled)
	require.Equal(t, "pricing unavailable", got.Data.Models[0].Note)
}
