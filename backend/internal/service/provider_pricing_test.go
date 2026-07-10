//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildHvoyProviderPricingUsesRechargeMultiplier(t *testing.T) {
	updatedAt := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewPricingService(nil, nil)
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

	resp := svc.BuildHvoyProviderPricing(0.5, "Test Site", "https://api.example.com/path", time.Time{})

	require.True(t, resp.Success)
	require.Equal(t, HvoyProviderPricingSchemaVersion, resp.SchemaVersion)
	require.Equal(t, HvoyProviderPricingCurrency, resp.Data.Currency)
	require.Equal(t, HvoyProviderPricingUnitTokens, resp.Data.PriceUnit)
	require.Equal(t, "Test Site", resp.Data.SiteName)
	require.Equal(t, "api.example.com", resp.Data.SiteDomain)
	require.Equal(t, updatedAt.Format(time.RFC3339), resp.Data.UpdatedAt)
	require.Len(t, resp.Data.Models, 4)

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

	gpt54 := resp.Data.Models[1]
	require.Equal(t, "gpt-5.4", gpt54.ModelName)
	require.Equal(t, "codex plus", gpt54.GroupName)
	require.Equal(t, gpt55.InputPrice, gpt54.InputPrice)
	require.NotNil(t, gpt54.CacheCreatePrice)
	require.Equal(t, 1.0, *gpt54.CacheCreatePrice)
	require.NotNil(t, gpt54.CacheCreatePrice1H)
	require.Equal(t, 2.0, *gpt54.CacheCreatePrice1H)

	gpt56Sol := resp.Data.Models[2]
	require.Equal(t, "gpt-5.6-sol", gpt56Sol.ModelName)
	require.Equal(t, "codex plus", gpt56Sol.GroupName)
	require.Equal(t, 10.0, gpt56Sol.InputPrice)
	require.NotNil(t, gpt56Sol.OutputPrice)
	require.Equal(t, 60.0, *gpt56Sol.OutputPrice)
	require.NotNil(t, gpt56Sol.CacheInputPrice)
	require.Equal(t, 1.0, *gpt56Sol.CacheInputPrice)
	require.NotNil(t, gpt56Sol.CacheCreatePrice)
	require.Equal(t, 12.5, *gpt56Sol.CacheCreatePrice)

	gpt56Terra := resp.Data.Models[3]
	require.Equal(t, "gpt-5.6-terra", gpt56Terra.ModelName)
	require.Equal(t, "codex plus", gpt56Terra.GroupName)
	require.Equal(t, 5.0, gpt56Terra.InputPrice)
	require.NotNil(t, gpt56Terra.OutputPrice)
	require.Equal(t, 30.0, *gpt56Terra.OutputPrice)
	require.NotNil(t, gpt56Terra.CacheInputPrice)
	require.Equal(t, 0.5, *gpt56Terra.CacheInputPrice)
	require.NotNil(t, gpt56Terra.CacheCreatePrice)
	require.Equal(t, 6.25, *gpt56Terra.CacheCreatePrice)
}

func TestBuildHvoyProviderPricingUsesStaticFallbackWithoutCatalog(t *testing.T) {
	svc := NewPricingService(nil, nil)
	resp := svc.BuildHvoyProviderPricing(1, "", "", time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))

	require.Len(t, resp.Data.Models, 4)
	require.True(t, resp.Data.Models[0].Enabled)
	require.Equal(t, 2.5, resp.Data.Models[0].InputPrice)
	require.True(t, resp.Data.Models[1].Enabled)
	require.Equal(t, 2.5, resp.Data.Models[1].InputPrice)
	require.True(t, resp.Data.Models[2].Enabled)
	require.Equal(t, "gpt-5.6-sol", resp.Data.Models[2].ModelName)
	require.Equal(t, "codex plus", resp.Data.Models[2].GroupName)
	require.Equal(t, 5.0, resp.Data.Models[2].InputPrice)
	require.True(t, resp.Data.Models[3].Enabled)
	require.Equal(t, "gpt-5.6-terra", resp.Data.Models[3].ModelName)
	require.Equal(t, "codex plus", resp.Data.Models[3].GroupName)
	require.Equal(t, 2.5, resp.Data.Models[3].InputPrice)
}

func TestBuildHvoyProviderPricingMarksUnavailableModel(t *testing.T) {
	originalModels := hvoyProviderPricingModels
	hvoyProviderPricingModels = []hvoyProviderPricingModelRef{{modelName: "missing-model", groupName: "missing-group"}}
	t.Cleanup(func() { hvoyProviderPricingModels = originalModels })

	svc := NewPricingService(nil, nil)
	resp := svc.BuildHvoyProviderPricing(1, "  Site  ", "http://%zz", time.Time{})

	require.Equal(t, "Site", resp.Data.SiteName)
	require.Empty(t, resp.Data.SiteDomain)
	require.NotEmpty(t, resp.Data.UpdatedAt)
	require.Len(t, resp.Data.Models, 1)
	require.Equal(t, "missing-model", resp.Data.Models[0].ModelName)
	require.Equal(t, "missing-group", resp.Data.Models[0].GroupName)
	require.False(t, resp.Data.Models[0].Enabled)
	require.Equal(t, "pricing unavailable", resp.Data.Models[0].Note)
}

func TestProviderPricingHelpersHandleFallbacks(t *testing.T) {
	require.Equal(t, 2.5, usdPerTokenToCNYPerMTok(2.5e-6, 0))
	require.Zero(t, usdPerTokenToCNYPerMTok(0, 1))
	require.Nil(t, optionalUSDPerTokenToCNYPerMTok(0, 1))
	require.Equal(t, time.Time{}, (*PricingService)(nil).LastUpdated())
	require.Empty(t, frontendURLDomain(""))
}
