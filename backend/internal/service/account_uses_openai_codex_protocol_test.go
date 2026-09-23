package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountUsesOpenAICodexProtocol(t *testing.T) {
	var nilAccount *Account
	require.False(t, nilAccount.UsesOpenAICodexProtocol())
	for _, tc := range []struct {
		platform, accountType string
		want                  bool
	}{
		{PlatformOpenAI, AccountTypeOAuth, true},
		{PlatformOpenAI, AccountTypeSetupToken, true},
		{PlatformOpenAI, AccountTypeAPIKey, false},
		{PlatformAnthropic, AccountTypeSetupToken, false},
	} {
		account := &Account{Platform: tc.platform, Type: tc.accountType}
		require.Equal(t, tc.want, account.UsesOpenAICodexProtocol(), "%s/%s", tc.platform, tc.accountType)
	}
}
