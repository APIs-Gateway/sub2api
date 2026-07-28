//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type grokUnauthorizedCacheStub struct {
	deleteKeys []string
	cacheMiss  bool
	setTokens  []string
}

func (c *grokUnauthorizedCacheStub) GetAccessToken(context.Context, string) (string, error) {
	if c.cacheMiss {
		return "", nil
	}
	return "cached-token", nil
}

func (c *grokUnauthorizedCacheStub) SetAccessToken(_ context.Context, _ string, token string, _ time.Duration) error {
	c.setTokens = append(c.setTokens, token)
	return nil
}

func (c *grokUnauthorizedCacheStub) DeleteAccessToken(_ context.Context, key string) error {
	c.deleteKeys = append(c.deleteKeys, key)
	return nil
}

func (c *grokUnauthorizedCacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (c *grokUnauthorizedCacheStub) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

type grokUnauthorizedRefreshExecutor struct {
	refreshCalls int
	refreshErr   error
	credentials  map[string]any
}

func (e *grokUnauthorizedRefreshExecutor) CanRefresh(*Account) bool { return true }

func (e *grokUnauthorizedRefreshExecutor) NeedsRefresh(*Account, time.Duration) bool {
	return false
}

func (e *grokUnauthorizedRefreshExecutor) Refresh(context.Context, *Account) (map[string]any, error) {
	e.refreshCalls++
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	return e.credentials, nil
}

func (e *grokUnauthorizedRefreshExecutor) CacheKey(*Account) string { return "grok:test" }

func newGrokUnauthorizedProvider(
	account *Account,
	executor *grokUnauthorizedRefreshExecutor,
	cache *grokUnauthorizedCacheStub,
) (*GrokTokenProvider, *refreshAPIAccountRepo) {
	repo := &refreshAPIAccountRepo{account: account}
	provider := NewGrokTokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)
	return provider, repo
}

func TestGrokTokenProviderRefreshAfterUnauthorizedForcesRefresh(t *testing.T) {
	account := &Account{
		ID:       701,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	cache := &grokUnauthorizedCacheStub{}
	executor := &grokUnauthorizedRefreshExecutor{credentials: map[string]any{
		"access_token":  "new-token",
		"refresh_token": "refresh-token-2",
		"expires_at":    time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
	}}
	provider, repo := newGrokUnauthorizedProvider(account, executor, cache)

	err := provider.RefreshAfterUnauthorized(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, []string{"grok:account:701"}, cache.deleteKeys)
	require.Equal(t, 1, executor.refreshCalls)
	require.Equal(t, "new-token", account.GetGrokAccessToken())
	require.Equal(t, "new-token", repo.account.GetGrokAccessToken())
	require.Equal(t, 1, repo.updateCredentialsCalls)
}

func TestGrokTokenProviderRefreshAfterUnauthorizedReturnsRefreshError(t *testing.T) {
	account := &Account{
		ID:       702,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-token",
		},
	}
	cache := &grokUnauthorizedCacheStub{}
	executor := &grokUnauthorizedRefreshExecutor{refreshErr: errors.New("invalid_grant")}
	provider, repo := newGrokUnauthorizedProvider(account, executor, cache)

	err := provider.RefreshAfterUnauthorized(context.Background(), account)

	require.EqualError(t, err, "invalid_grant")
	require.Equal(t, []string{"grok:account:702"}, cache.deleteKeys)
	require.Equal(t, 1, executor.refreshCalls)
	require.Zero(t, repo.updateCredentialsCalls)
}

func TestGrokTokenProviderGetAccessTokenCacheRefreshAndValidation(t *testing.T) {
	provider := NewGrokTokenProvider(nil, nil, nil)
	_, err := provider.GetAccessToken(context.Background(), nil)
	require.EqualError(t, err, "account is nil")
	_, err = provider.GetAccessToken(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	require.EqualError(t, err, "not a grok oauth account")

	account := &Account{
		ID:       706,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "account-token",
			"expires_at":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	cache := &grokUnauthorizedCacheStub{}
	provider = NewGrokTokenProvider(nil, cache, nil)
	token, err := provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "cached-token", token)

	cache.cacheMiss = true
	token, err = provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "account-token", token)
	require.Equal(t, []string{"account-token"}, cache.setTokens)

	account.Credentials["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	account.Credentials["refresh_token"] = ""
	_, err = provider.GetAccessToken(context.Background(), account)
	require.EqualError(t, err, "grok access_token expired and refresh_token is missing")

	account.Credentials["refresh_token"] = "refresh-token"
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "refreshed-access-token", "refresh_token": "refresh-token-2"},
	}
	provider = NewGrokTokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)
	token, err = provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "refreshed-access-token", token)
	require.Equal(t, 1, executor.refreshCalls)
}

func TestGrokTokenCacheKeyUsesEmailWhenAvailable(t *testing.T) {
	require.Equal(t, "grok:account:0", GrokTokenCacheKey(nil))
	require.Equal(t, "grok:user@example.com", GrokTokenCacheKey(&Account{
		ID:          707,
		Credentials: map[string]any{"email": " user@example.com "},
	}))
}
