//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var antigravityGemini36FlashModels = []string{
	"gemini-3.6-flash",
	"gemini-3.6-flash-high",
	"gemini-3.6-flash-low",
	"gemini-3.6-flash-medium",
	"gemini-3.6-flash-tiered",
}

// Fork regression for the upstream Gemini 3.6 Flash sync: an Antigravity
// account without an explicit model_mapping must schedule every 3.6 Flash
// thinking-tier ID and forward it unchanged, so billing can normalize the
// tier alias to the gemini-3.6-flash price card.
func TestAntigravityAccount_Gemini36FlashDefaultMapping(t *testing.T) {
	account := &Account{ID: 3601, Platform: PlatformAntigravity, Type: AccountTypeOAuth}

	for _, model := range antigravityGemini36FlashModels {
		require.True(t, account.IsModelSupported(model), model)
		require.Equal(t, model, account.GetMappedModel(model), model)
	}
}

// A custom model_mapping must still keep the 3.6 Flash IDs as default
// passthroughs instead of silently whitelisting them out.
func TestAntigravityAccount_Gemini36FlashPassthroughWithCustomMapping(t *testing.T) {
	account := &Account{
		ID:       3602,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-sonnet-4-5": "claude-sonnet-4-5",
			},
		},
	}

	for _, model := range antigravityGemini36FlashModels {
		require.True(t, account.IsModelSupported(model), model)
		require.Equal(t, model, account.GetMappedModel(model), model)
	}
	require.False(t, account.IsModelSupported("gemini-2.0-pro"))
}
