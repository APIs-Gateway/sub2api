package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetOpenAIChatGPTAccountHeadersHandlesFedRAMPAndAccountMode(t *testing.T) {
	headers := make(http.Header)
	headers.Set("x-openai-fedramp", "true")
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id":         "acct-standard",
			"chatgpt_account_is_fedramp": false,
		},
	}

	setOpenAIChatGPTAccountHeaders(headers, account)
	require.Equal(t, "acct-standard", headers.Get("chatgpt-account-id"))
	require.Empty(t, headers.Get("x-openai-fedramp"))

	account.Credentials["chatgpt_account_is_fedramp"] = true
	setOpenAIChatGPTAccountHeaders(headers, account)
	require.Equal(t, "true", headers.Get("x-openai-fedramp"))

	account.Type = AccountTypeAPIKey
	headers.Set("chatgpt-account-id", "stale")
	headers.Set("x-openai-fedramp", "stale")
	setOpenAIChatGPTAccountHeaders(headers, account)
	require.Equal(t, "stale", headers.Get("chatgpt-account-id"))
	require.Equal(t, "stale", headers.Get("x-openai-fedramp"))
}

func TestOpenAIAccountPATAndFedRAMPCredentialParsing(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.False(t, account.IsOpenAIPersonalAccessToken())
	require.False(t, account.IsChatGPTAccountFedRAMP())

	account.Credentials = map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}
	require.True(t, account.IsOpenAIPersonalAccessToken())
	account.Credentials = map[string]any{"openai_auth_mode": "personal_access_token"}
	require.True(t, account.IsOpenAIPersonalAccessToken())
	account.Credentials = map[string]any{"auth_mode": "oauth"}
	require.False(t, account.IsOpenAIPersonalAccessToken())

	apiKey := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken},
	}
	require.False(t, apiKey.IsOpenAIPersonalAccessToken())
	require.False(t, apiKey.IsChatGPTAccountFedRAMP())

	values := []struct {
		name  string
		value any
		want  bool
	}{
		{name: "bool true", value: true, want: true},
		{name: "bool false", value: false, want: false},
		{name: "string true", value: " true ", want: true},
		{name: "string invalid", value: "invalid", want: false},
		{name: "json number true", value: json.Number("1"), want: true},
		{name: "json number false", value: json.Number("0"), want: false},
		{name: "float true", value: float64(1), want: true},
		{name: "float false", value: float64(0), want: false},
		{name: "int true", value: 1, want: true},
		{name: "int false", value: 0, want: false},
		{name: "int64 true", value: int64(1), want: true},
		{name: "int64 false", value: int64(0), want: false},
		{name: "unknown", value: []any{"true"}, want: false},
	}
	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			account.Credentials = map[string]any{"chatgpt_account_is_fedramp": tt.value}
			require.Equal(t, tt.want, account.IsChatGPTAccountFedRAMP())
		})
	}

	account.Credentials = map[string]any{"chatgpt_account_is_fedramp": nil}
	require.False(t, account.IsChatGPTAccountFedRAMP())
}
