package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	opus5InputPricePerToken         = 5e-6
	opus5OutputPricePerToken        = 25e-6
	opus5CacheCreationPricePerToken = 6.25e-6
	opus5CacheReadPricePerToken     = 0.5e-6
)

// TestClaudeOpus5_FamilyFallbackDoesNotUseOpus4Rates verifies remote pricing
// data can fall back to the same-priced Opus 4.8 entry rather than Opus 4 rates.
func TestClaudeOpus5_FamilyFallbackDoesNotUseOpus4Rates(t *testing.T) {
	svc := NewBillingService(&config.Config{}, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-4-8": {
				InputCostPerToken:           opus5InputPricePerToken,
				OutputCostPerToken:          opus5OutputPricePerToken,
				CacheCreationInputTokenCost: opus5CacheCreationPricePerToken,
				CacheReadInputTokenCost:     opus5CacheReadPricePerToken,
			},
			"claude-opus-4-1":        {InputCostPerToken: 15e-6, OutputCostPerToken: 75e-6},
			"claude-opus-4-20250514": {InputCostPerToken: 15e-6, OutputCostPerToken: 75e-6},
			"claude-3-opus-20240229": {InputCostPerToken: 15e-6, OutputCostPerToken: 75e-6},
		},
	})

	for _, model := range []string{"claude-opus-5", "us.anthropic.claude-opus-5-v1"} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			assert.InDelta(t, opus5InputPricePerToken, pricing.InputPricePerToken, 1e-12)
			assert.InDelta(t, opus5OutputPricePerToken, pricing.OutputPricePerToken, 1e-12)
		})
	}
}

// TestClaudeOpus5_HardcodedFallbackPricing covers the static fallback table
// when dynamic pricing data is unavailable.
func TestClaudeOpus5_HardcodedFallbackPricing(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)

	tests := []struct {
		model  string
		input  float64
		output float64
	}{
		{"claude-opus-5", opus5InputPricePerToken, opus5OutputPricePerToken},
		{"us.anthropic.claude-opus-5-v1", opus5InputPricePerToken, opus5OutputPricePerToken},
		{"claude-opus-4-8", opus5InputPricePerToken, opus5OutputPricePerToken},
		{"claude-opus-4-5-20251101", 5e-6, 25e-6},
		{"claude-opus-4-1-20250805", 15e-6, 75e-6},
		{"claude-3-opus-20240229", 15e-6, 75e-6},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			assert.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-12)
			assert.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-12)
		})
	}

	opus5, err := svc.GetModelPricing("claude-opus-5")
	require.NoError(t, err)
	assert.InDelta(t, opus5CacheCreationPricePerToken, opus5.CacheCreationPricePerToken, 1e-12)
	assert.InDelta(t, opus5CacheReadPricePerToken, opus5.CacheReadPricePerToken, 1e-12)
}

// TestClaudeOpus5_BedrockCapabilityGates verifies bare major-version model IDs
// retain their modern cache, tool-search, and adaptive-thinking capabilities.
func TestClaudeOpus5_BedrockCapabilityGates(t *testing.T) {
	tests := []struct {
		modelID       string
		claude45Newer bool
		toolSearch    bool
		opus47Newer   bool
	}{
		{"claude-opus-5", true, true, true},
		{"us.anthropic.claude-opus-5-v1", true, true, true},
		{"eu.anthropic.claude-opus-5-v1", true, true, true},
		{"claude-sonnet-5", true, true, false},
		{"us.anthropic.claude-sonnet-5-v1", true, true, false},
		{"anthropic.claude-opus-4-1-v1", false, false, false},
		{"anthropic.claude-sonnet-4-0-v1", false, false, false},
		{"anthropic.claude-3-opus-20240229-v1:0", false, false, false},
		{"us.anthropic.claude-opus-4-8-v1", true, true, true},
		{"us.anthropic.claude-haiku-4-5-20251001-v1:0", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			assert.Equal(t, tt.claude45Newer, isBedrockClaude45OrNewer(tt.modelID), "isBedrockClaude45OrNewer")
			assert.Equal(t, tt.toolSearch, bedrockModelSupportsToolSearch(tt.modelID), "bedrockModelSupportsToolSearch")
			assert.Equal(t, tt.opus47Newer, isBedrockOpus47OrNewer(tt.modelID), "isBedrockOpus47OrNewer")
		})
	}
}

func TestClaudeOpus5_BedrockThinkingConvertedToAdaptive(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":10000}}`)
	got := sanitizeBedrockThinking(body, "us.anthropic.claude-opus-5-v1")

	assert.JSONEq(t, `{"thinking":{"type":"adaptive"}}`, string(got))
}

func TestClaudeOpus5_CatalogAndBedrockMapping(t *testing.T) {
	assert.Contains(t, claude.DefaultModelIDs(), "claude-opus-5")

	mapped, ok := domain.DefaultBedrockModelMapping["claude-opus-5"]
	require.True(t, ok, "claude-opus-5 missing from DefaultBedrockModelMapping")
	assert.Equal(t, "us.anthropic.claude-opus-5-v1", mapped)
}
