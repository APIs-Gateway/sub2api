//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildHvoyProviderPricingUsesRechargeMultiplier(t *testing.T) {
	updatedAt := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewPricingService(nil, nil)
	// gpt-5.4 is intentionally not published; a catalog entry for it must not leak into the response.
	svc.pricingData = map[string]*LiteLLMModelPricing{
		"gpt-5.4": {
			InputCostPerToken:                   2.5e-6,
			OutputCostPerToken:                  1.5e-5,
			CacheReadInputTokenCost:             2.5e-7,
			CacheCreationInputTokenCost:         5e-7,
			CacheCreationInputTokenCostAbove1hr: 1e-6,
		},
	}
	svc.lastUpdated = updatedAt

	resp := svc.BuildHvoyProviderPricing(0.5, map[string]float64{"codex plus": 1}, "Test Site", "https://api.example.com/path", time.Time{})

	require.True(t, resp.Success)
	require.Equal(t, HvoyProviderPricingSchemaVersion, resp.SchemaVersion)
	require.Equal(t, HvoyProviderPricingCurrency, resp.Data.Currency)
	require.Equal(t, HvoyProviderPricingUnitTokens, resp.Data.PriceUnit)
	require.Equal(t, "Test Site", resp.Data.SiteName)
	require.Equal(t, "api.example.com", resp.Data.SiteDomain)
	require.Equal(t, updatedAt.Format(time.RFC3339), resp.Data.UpdatedAt)
	require.Len(t, resp.Data.Models, 5)

	gpt55 := resp.Data.Models[0]
	require.Equal(t, "gpt-5.5", gpt55.ModelName)
	require.Equal(t, HvoyProviderPricingGroupName, gpt55.GroupName)
	require.Equal(t, "codex plus", gpt55.GroupName)
	require.True(t, gpt55.Enabled)
	require.Equal(t, 5.0, gpt55.InputPrice)
	require.NotNil(t, gpt55.OutputPrice)
	require.Equal(t, 30.0, *gpt55.OutputPrice)
	require.NotNil(t, gpt55.CacheInputPrice)
	require.Equal(t, 0.5, *gpt55.CacheInputPrice)
	require.Nil(t, gpt55.CacheCreatePrice)
	require.Nil(t, gpt55.CacheCreatePrice1H)

	gpt56Sol := resp.Data.Models[1]
	require.Equal(t, "gpt-5.6-sol", gpt56Sol.ModelName)
	require.Equal(t, "codex plus", gpt56Sol.GroupName)
	require.Equal(t, 10.0, gpt56Sol.InputPrice)
	require.NotNil(t, gpt56Sol.OutputPrice)
	require.Equal(t, 60.0, *gpt56Sol.OutputPrice)
	require.NotNil(t, gpt56Sol.CacheInputPrice)
	require.Equal(t, 1.0, *gpt56Sol.CacheInputPrice)
	require.NotNil(t, gpt56Sol.CacheCreatePrice)
	require.Equal(t, 12.5, *gpt56Sol.CacheCreatePrice)

	gpt56Terra := resp.Data.Models[2]
	require.Equal(t, "gpt-5.6-terra", gpt56Terra.ModelName)
	require.Equal(t, "codex plus", gpt56Terra.GroupName)
	require.Equal(t, 4.0, gpt56Terra.InputPrice)
	require.NotNil(t, gpt56Terra.OutputPrice)
	require.Equal(t, 24.0, *gpt56Terra.OutputPrice)
	require.NotNil(t, gpt56Terra.CacheInputPrice)
	require.Equal(t, 0.4, *gpt56Terra.CacheInputPrice)
	require.NotNil(t, gpt56Terra.CacheCreatePrice)
	require.Equal(t, 5.0, *gpt56Terra.CacheCreatePrice)

	gpt56Luna := resp.Data.Models[3]
	require.Equal(t, "gpt-5.6-luna", gpt56Luna.ModelName)
	require.Equal(t, "codex plus", gpt56Luna.GroupName)
	require.True(t, gpt56Luna.Enabled)
	require.Equal(t, 0.4, gpt56Luna.InputPrice)
	require.NotNil(t, gpt56Luna.OutputPrice)
	require.Equal(t, 2.4, *gpt56Luna.OutputPrice)
	require.NotNil(t, gpt56Luna.CacheInputPrice)
	require.Equal(t, 0.04, *gpt56Luna.CacheInputPrice)
	require.NotNil(t, gpt56Luna.CacheCreatePrice)
	require.Equal(t, 0.5, *gpt56Luna.CacheCreatePrice)

	gpt6Astra := resp.Data.Models[4]
	require.Equal(t, "gpt-6-astra", gpt6Astra.ModelName)
	require.Equal(t, "codex plus", gpt6Astra.GroupName)
	require.True(t, gpt6Astra.Enabled)
	require.Equal(t, 20.0, gpt6Astra.InputPrice)
	require.NotNil(t, gpt6Astra.OutputPrice)
	require.Equal(t, 100.0, *gpt6Astra.OutputPrice)
	require.NotNil(t, gpt6Astra.CacheInputPrice)
	require.Equal(t, 2.0, *gpt6Astra.CacheInputPrice)
	require.NotNil(t, gpt6Astra.CacheCreatePrice)
	require.Equal(t, 25.0, *gpt6Astra.CacheCreatePrice)
	require.Nil(t, gpt6Astra.CacheCreatePrice1H)
}

func TestBuildHvoyProviderPricingUsesStaticFallbackWithoutCatalog(t *testing.T) {
	svc := NewPricingService(nil, nil)
	resp := svc.BuildHvoyProviderPricing(1, map[string]float64{"codex plus": 1}, "", "", time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))

	require.Len(t, resp.Data.Models, 5)
	require.True(t, resp.Data.Models[0].Enabled)
	require.Equal(t, "gpt-5.5", resp.Data.Models[0].ModelName)
	require.Equal(t, 2.5, resp.Data.Models[0].InputPrice)
	require.True(t, resp.Data.Models[1].Enabled)
	require.Equal(t, "gpt-5.6-sol", resp.Data.Models[1].ModelName)
	require.Equal(t, "codex plus", resp.Data.Models[1].GroupName)
	require.Equal(t, 5.0, resp.Data.Models[1].InputPrice)
	require.True(t, resp.Data.Models[2].Enabled)
	require.Equal(t, "gpt-5.6-terra", resp.Data.Models[2].ModelName)
	require.Equal(t, "codex plus", resp.Data.Models[2].GroupName)
	require.Equal(t, 2.0, resp.Data.Models[2].InputPrice)
	require.True(t, resp.Data.Models[3].Enabled)
	require.Equal(t, "gpt-5.6-luna", resp.Data.Models[3].ModelName)
	require.Equal(t, "codex plus", resp.Data.Models[3].GroupName)
	require.Equal(t, 0.2, resp.Data.Models[3].InputPrice)
	require.True(t, resp.Data.Models[4].Enabled)
	require.Equal(t, "gpt-6-astra", resp.Data.Models[4].ModelName)
	require.Equal(t, "codex plus", resp.Data.Models[4].GroupName)
	require.Equal(t, 10.0, resp.Data.Models[4].InputPrice)
}

func TestBuildHvoyProviderPricingMarksUnavailableModel(t *testing.T) {
	originalModels := hvoyProviderPricingModels
	hvoyProviderPricingModels = []hvoyProviderPricingModelRef{{modelName: "missing-model", groupName: "missing-group"}}
	t.Cleanup(func() { hvoyProviderPricingModels = originalModels })

	svc := NewPricingService(nil, nil)
	resp := svc.BuildHvoyProviderPricing(1, nil, "  Site  ", "http://%zz", time.Time{})

	require.Equal(t, "Site", resp.Data.SiteName)
	require.Empty(t, resp.Data.SiteDomain)
	require.NotEmpty(t, resp.Data.UpdatedAt)
	require.Len(t, resp.Data.Models, 1)
	require.Equal(t, "missing-model", resp.Data.Models[0].ModelName)
	require.Equal(t, "missing-group", resp.Data.Models[0].GroupName)
	require.False(t, resp.Data.Models[0].Enabled)
	require.Equal(t, "pricing unavailable", resp.Data.Models[0].Note)
}

type hvoyGroupListerStub struct {
	groups []Group
	err    error
}

func (s hvoyGroupListerStub) ListActive(context.Context) ([]Group, error) {
	return s.groups, s.err
}

func TestBuildHvoyProviderPricingAppliesGroupRateMultiplier(t *testing.T) {
	svc := NewPricingService(nil, nil)
	resp := svc.BuildHvoyProviderPricing(0.5, map[string]float64{"codex plus": 1.4}, "", "", time.Time{})

	require.Len(t, resp.Data.Models, 5)
	for _, model := range resp.Data.Models {
		require.True(t, model.Enabled, model.ModelName)
		require.Empty(t, model.Note, model.ModelName)
	}
	gpt56Sol := resp.Data.Models[1]
	require.Equal(t, "gpt-5.6-sol", gpt56Sol.ModelName)
	require.Equal(t, 14.0, gpt56Sol.InputPrice)
	require.Equal(t, 84.0, *gpt56Sol.OutputPrice)
	require.Equal(t, 1.4, *gpt56Sol.CacheInputPrice)
	require.Equal(t, 17.5, *gpt56Sol.CacheCreatePrice)
	gpt6Astra := resp.Data.Models[4]
	require.Equal(t, "gpt-6-astra", gpt6Astra.ModelName)
	require.Equal(t, 28.0, gpt6Astra.InputPrice)
	require.Equal(t, 140.0, *gpt6Astra.OutputPrice)
}

func TestBuildHvoyProviderPricingFallsBackToOneWhenGroupMultiplierMissing(t *testing.T) {
	svc := NewPricingService(nil, nil)

	missing := svc.BuildHvoyProviderPricing(0.5, nil, "", "", time.Time{})
	require.Equal(t, 10.0, missing.Data.Models[1].InputPrice)
	require.Equal(t, "group rate multiplier unavailable", missing.Data.Models[1].Note)
	require.True(t, missing.Data.Models[1].Enabled)

	invalid := svc.BuildHvoyProviderPricing(0.5, map[string]float64{"codex plus": 0}, "", "", time.Time{})
	require.Equal(t, 10.0, invalid.Data.Models[1].InputPrice)
	require.Equal(t, "group rate multiplier invalid", invalid.Data.Models[1].Note)
}

func TestLoadHvoyProviderGroupMultipliers(t *testing.T) {
	ctx := context.Background()

	got, err := LoadHvoyProviderGroupMultipliers(ctx, hvoyGroupListerStub{groups: []Group{
		{Name: "codex  plus", RateMultiplier: 1.4},
		{Name: "Codex Pro+Plus", RateMultiplier: 3},
	}})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"codex plus": 1.4}, got)

	got, err = LoadHvoyProviderGroupMultipliers(ctx, hvoyGroupListerStub{groups: []Group{{Name: "other", RateMultiplier: 2}}})
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = LoadHvoyProviderGroupMultipliers(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, got)

	_, err = LoadHvoyProviderGroupMultipliers(ctx, hvoyGroupListerStub{err: errors.New("db down")})
	require.Error(t, err)
}

func TestProviderPricingHelpersHandleFallbacks(t *testing.T) {
	require.Equal(t, 2.5, usdPerTokenToCNYPerMTok(2.5e-6, 0))
	require.Zero(t, usdPerTokenToCNYPerMTok(0, 1))
	require.Nil(t, optionalUSDPerTokenToCNYPerMTok(0, 1))
	require.Equal(t, time.Time{}, (*PricingService)(nil).LastUpdated())
	require.Empty(t, frontendURLDomain(""))
}
