package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountApplyHeaderOverrides(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides: map[string]any{
				"Anthropic-Beta": "context-management-2025-06-27",
				"authorization":   "Bearer forbidden",
				"Content-Type":    "application/json; broken",
			},
		},
	}
	header := http.Header{"anthropic-beta": {"client"}, "Authorization": {"Bearer gateway"}}
	account.ApplyHeaderOverrides(header)
	require.Equal(t, "context-management-2025-06-27", header.Get("anthropic-beta"))
	require.Equal(t, "Bearer gateway", header.Get("authorization"))
	_, ok := account.HeaderOverrideValue("content-type")
	require.False(t, ok)
}

func TestNormalizeHeaderOverrideCredentials(t *testing.T) {
	credentials := map[string]any{
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides: map[string]any{
			" X-Upstream-Mode ": " enabled ",
			"":                    "",
		},
	}
	require.NoError(t, NormalizeHeaderOverrideCredentials(credentials))
	require.Equal(t, map[string]any{"x-upstream-mode": "enabled"}, credentials[credKeyHeaderOverrides])

	for _, blocked := range []string{"content-type", "cookie", "x-goog-api-key", "x-client-request-id"} {
		err := NormalizeHeaderOverrideCredentials(map[string]any{
			credKeyHeaderOverrides: map[string]any{blocked: "value"},
		})
		require.Error(t, err, blocked)
	}
}

func TestAccountHeaderOverrides_EligibilityAndEnablement(t *testing.T) {
	tests := []struct {
		name     string
		account  *Account
		eligible bool
		enabled  bool
	}{
		{"nil account", nil, false, false},
		{"Anthropic API key", &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, true, false},
		{"OpenAI API key", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, true, false},
		{"Anthropic OAuth", &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, false, false},
		{"Bedrock credential", &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}, false, false},
		{"enabled Anthropic API key", &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{credKeyHeaderOverrideEnabled: true}}, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.eligible, tt.account.IsHeaderOverrideEligible())
			require.Equal(t, tt.enabled, tt.account.IsHeaderOverrideEnabled())
		})
	}
}

func TestAccountHeaderOverrides_EnabledNoopAndCaseInsensitiveReplacement(t *testing.T) {
	disabled := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: false,
			credKeyHeaderOverrides:       map[string]any{"X-Upstream-Mode": "enabled"},
		},
	}
	header := http.Header{"X-Upstream-Mode": {"client"}}
	disabled.ApplyHeaderOverrides(header)
	require.Equal(t, "client", header.Get("X-Upstream-Mode"))

	enabled := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"x-upstream-mode": "gateway"},
		},
	}
	header = http.Header{"X-Upstream-Mode": {"client"}, "x-upstream-mode": {"duplicate"}}
	enabled.ApplyHeaderOverrides(header)
	require.Equal(t, "gateway", header.Get("X-Upstream-Mode"))
	matching := 0
	for name := range header {
		if name == "X-Upstream-Mode" || name == "x-upstream-mode" {
			matching++
		}
	}
	require.Equal(t, 1, matching)
}

func TestNormalizeHeaderOverrideCredentials_RejectsMalformedBlockedAndUnsafeValues(t *testing.T) {
	tooMany := make(map[string]any, maxHeaderOverrideEntries+1)
	for i := 0; i <= maxHeaderOverrideEntries; i++ {
		tooMany["X-Test-"+string(rune('a'+i))] = "value"
	}

	tests := []struct {
		name        string
		credentials map[string]any
	}{
		{"enabled must be boolean", map[string]any{credKeyHeaderOverrideEnabled: "true"}},
		{"overrides must be object", map[string]any{credKeyHeaderOverrides: "not-an-object"}},
		{"value must be string", map[string]any{credKeyHeaderOverrides: map[string]any{"X-Test": true}}},
		{"empty name with value", map[string]any{credKeyHeaderOverrides: map[string]any{"": "value"}}},
		{"invalid header name", map[string]any{credKeyHeaderOverrides: map[string]any{"X Bad": "value"}}},
		{"duplicate names are case insensitive", map[string]any{credKeyHeaderOverrides: map[string]any{"X-Test": "one", "x-test": "two"}}},
		{"entry limit", map[string]any{credKeyHeaderOverrides: tooMany}},
		{"name length limit", map[string]any{credKeyHeaderOverrides: map[string]any{string(make([]byte, maxHeaderOverrideNameLength+1)): "value"}}},
		{"value length limit", map[string]any{credKeyHeaderOverrides: map[string]any{"X-Test": string(make([]byte, maxHeaderOverrideValueLength+1))}}},
		{"CRLF value", map[string]any{credKeyHeaderOverrides: map[string]any{"X-Test": "ok\\r\\nInjected: true"}}},
		{"authorization is gateway owned", map[string]any{credKeyHeaderOverrides: map[string]any{"Authorization": "Bearer override"}}},
		{"anthropic API key is gateway owned", map[string]any{credKeyHeaderOverrides: map[string]any{"X-Api-Key": "override"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, NormalizeHeaderOverrideCredentials(tt.credentials))
		})
	}
}

func TestAccountHeaderOverrides_CacheInvalidatesAfterInPlaceCredentialsMutation(t *testing.T) {
	overrides := map[string]any{"X-Feature": "one"}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       overrides,
		},
	}

	require.Equal(t, "one", account.GetHeaderOverrides()["x-feature"])
	overrides["X-Feature"] = "two"
	require.Equal(t, "two", account.GetHeaderOverrides()["x-feature"])
}
