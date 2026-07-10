package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeKnownOpenAICodexModel_BareGPT56RoutesToSol(t *testing.T) {
	for input, expected := range map[string]string{
		"gpt-5.6":            "gpt-5.6-sol",
		"openai/gpt-5.6":     "gpt-5.6-sol",
		"gpt5.6":             "gpt-5.6-sol",
		"gpt-5.6-high":       "gpt-5.6-sol",
		"gpt-5.6-max":        "gpt-5.6-sol",
		"gpt-5.6-2026-07-09": "gpt-5.6-sol",
		"gpt-5.6-preview":    "",
	} {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, expected, normalizeKnownOpenAICodexModel(input))
		})
	}
}

func TestIsOpenAIGPT56Model(t *testing.T) {
	for input, want := range map[string]bool{
		"gpt-5.6":             true,
		"gpt-5.6-max":         true,
		"gpt-5.6-2026-07-09":  true,
		"gpt-5.6-sol-preview": true,
		"openai/gpt5.6-terra": true,
		"gpt-5.6-preview":     false,
		"gpt-5.5":             false,
		"claude-opus-4-6":     false,
	} {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, want, isOpenAIGPT56Model(input))
		})
	}
}

func TestUsageBillingModelCandidates_BareGPT56IncludesSol(t *testing.T) {
	require.Equal(t, []string{"gpt-5.6", "gpt-5.6-sol"}, usageBillingModelCandidates("gpt-5.6"))
	require.Equal(t, []string{"openai/gpt-5.6", "gpt-5.6", "gpt-5.6-sol"}, usageBillingModelCandidates("openai/gpt-5.6"))
}
