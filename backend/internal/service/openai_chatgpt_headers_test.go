package service

import (
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
