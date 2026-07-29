//go:build unit

package xai

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseAuthorizationInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		wantCode  string
		wantState string
	}{
		{
			name:      "full callback url",
			raw:       "http://127.0.0.1:56121/callback?code=abc123&state=state456",
			wantCode:  "abc123",
			wantState: "state456",
		},
		{
			name:      "query string",
			raw:       "?code=abc123&state=state456",
			wantCode:  "abc123",
			wantState: "state456",
		},
		{
			name:     "bare code",
			raw:      "abc123",
			wantCode: "abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseAuthorizationInput(tt.raw)
			require.Equal(t, tt.wantCode, got.Code)
			require.Equal(t, tt.wantState, got.State)
		})
	}
}

func TestBuildAuthorizationURLIncludesHermesCompatibleParameters(t *testing.T) {
	t.Setenv(EnvAuthorizeURL, "https://auth.example.test/oauth2/authorize")
	t.Setenv(EnvClientID, "client-id")
	t.Setenv(EnvScope, "openid profile offline_access api:access")

	authURL := BuildAuthorizationURL("state", "challenge", "http://127.0.0.1:56121/callback", "nonce")
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)

	values := parsed.Query()
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "auth.example.test", parsed.Host)
	require.Equal(t, "/oauth2/authorize", parsed.Path)
	require.Equal(t, "code", values.Get("response_type"))
	require.Equal(t, "client-id", values.Get("client_id"))
	require.Equal(t, "http://127.0.0.1:56121/callback", values.Get("redirect_uri"))
	require.Equal(t, "openid profile offline_access api:access", values.Get("scope"))
	require.Equal(t, "state", values.Get("state"))
	require.Equal(t, "nonce", values.Get("nonce"))
	require.Equal(t, "challenge", values.Get("code_challenge"))
	require.Equal(t, "S256", values.Get("code_challenge_method"))
	require.Equal(t, "generic", values.Get("plan"))
	require.Equal(t, "sub2api", values.Get("referrer"))
}

func TestDefaultModelMappingIncludesGrokAliases(t *testing.T) {
	t.Parallel()

	mapping := DefaultModelMapping()
	require.Equal(t, "grok-4.3", mapping["grok"])
	require.Equal(t, "grok-4.3", mapping["grok-latest"])
	require.Equal(t, "grok-build-0.1", mapping["grok-build"])
	require.Equal(t, "grok-4.20-0309-reasoning", mapping["grok-4.20-reasoning"])
	require.Equal(t, "grok-4.20-0309-non-reasoning", mapping["grok-4.20-non-reasoning"])
	require.Equal(t, "grok-4.20-multi-agent-0309", mapping["grok-4.20-multi-agent-0309"])
}

func TestDefaultModelsAndIDsReturnIndependentModelLists(t *testing.T) {
	models := DefaultModels()
	require.Len(t, models, 5)
	require.Equal(t, "grok-4.3", models[0].ID)
	require.Equal(t, "Grok 4.20 Multi Agent", models[4].DisplayName)

	models[0].ID = "mutated"
	ids := DefaultModelIDs()
	require.Equal(t, "grok-4.3", ids[0])
	require.Len(t, ids, len(DefaultModels()))
}

func TestSessionStoreExpiresAndDeletesSessions(t *testing.T) {
	store := NewSessionStore()
	defer store.Stop()

	store.Set("live", &OAuthSession{CreatedAt: time.Now()})
	got, ok := store.Get("live")
	require.True(t, ok)
	require.NotNil(t, got)

	store.Set("expired", &OAuthSession{CreatedAt: time.Now().Add(-SessionTTL - time.Second)})
	got, ok = store.Get("expired")
	require.False(t, ok)
	require.Nil(t, got)

	store.Delete("live")
	_, ok = store.Get("live")
	require.False(t, ok)
	store.Stop()
}

func TestXAIEnvironmentOverridesAndAuthorizationInputEdges(t *testing.T) {
	t.Setenv(EnvBaseURL, " https://env.example.test/v1/ ")
	require.Equal(t, "https://env.example.test/v1", EffectiveBaseURL(""))
	require.Equal(t, "https://override.example.test", EffectiveBaseURL(" https://override.example.test/ "))

	require.Equal(t, AuthorizationInput{}, ParseAuthorizationInput(""))
	require.Equal(t, AuthorizationInput{Code: "abc"}, ParseAuthorizationInput("code=abc&state="))
	require.Equal(t, AuthorizationInput{Code: "abc"}, ParseAuthorizationInput("?code=abc&state="))

	require.Equal(t, AuthorizationInput{Code: "abc", State: "state"}, ParseAuthorizationInput("https://callback.test/?code=abc&state=state"))
	require.NotEmpty(t, BuildAuthorizationURL("state", "challenge", "", "nonce"))
	require.Equal(t, "https://api.x.ai/v1/responses", BuildResponsesURL("https://api.x.ai/v1/"))
}
