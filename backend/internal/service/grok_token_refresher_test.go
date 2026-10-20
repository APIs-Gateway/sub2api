//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrokTokenRefresherPolicyAndRefresh(t *testing.T) {
	service := NewGrokOAuthService(nil, grokOAuthClientStub{})
	defer service.Stop()
	refresher := NewGrokTokenRefresher(service)

	account := &Account{
		ID:       7,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "refresh-token",
			"base_url":      "https://custom.example/v1",
			"existing":      "keep",
		},
	}
	require.Equal(t, "grok:account:7", refresher.CacheKey(account))
	require.True(t, refresher.CanRefresh(account))
	require.False(t, refresher.CanRefresh(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	require.False(t, refresher.NeedsRefresh(&Account{}, time.Hour))
	require.True(t, refresher.NeedsRefresh(account, time.Minute))

	account.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	require.False(t, refresher.NeedsRefresh(account, time.Hour))
	account.Credentials["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	require.True(t, refresher.NeedsRefresh(account, time.Minute))

	newCredentials, err := refresher.Refresh(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "keep", newCredentials["existing"])
	require.Equal(t, "https://custom.example/v1", newCredentials["base_url"])
	require.Equal(t, "refreshed-token", newCredentials["access_token"])

	broken := NewGrokTokenRefresher(nil)
	_, err = broken.Refresh(context.Background(), account)
	require.Error(t, err)
}
