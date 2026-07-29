//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type grokOAuthClientStub struct{}

func (grokOAuthClientStub) ExchangeCode(context.Context, string, string, string, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

func (grokOAuthClientStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "refreshed-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

type grokOAuthProxyRepoStub struct {
	ProxyRepository
	proxy *Proxy
	err   error
}

func (s grokOAuthProxyRepoStub) GetByID(context.Context, int64) (*Proxy, error) {
	return s.proxy, s.err
}

func TestGrokOAuthServiceExchangeCodeRequiresState(t *testing.T) {
	service := NewGrokOAuthService(nil, grokOAuthClientStub{})
	defer service.Stop()

	authURL, err := service.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)

	_, err = service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: authURL.SessionID,
		Code:      "authorization-code",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_INVALID_STATE")

	authURL, err = service.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	tokenInfo, err := service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: authURL.SessionID,
		Code:      "authorization-code",
		State:     authURL.State,
	})
	require.NoError(t, err)
	require.Equal(t, "access-token", tokenInfo.AccessToken)
}

func TestGrokOAuthServiceRefreshValidationAndCredentials(t *testing.T) {
	proxyRepo := grokOAuthProxyRepoStub{proxy: &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 8080}}
	service := NewGrokOAuthService(proxyRepo, grokOAuthClientStub{})
	defer service.Stop()

	_, err := service.RefreshToken(context.Background(), " ", "", "")
	require.Error(t, err)

	refreshed, err := service.RefreshToken(context.Background(), "refresh-token", "http://proxy", "client-id")
	require.NoError(t, err)
	require.Equal(t, "refreshed-token", refreshed.AccessToken)
	require.Equal(t, "client-id", refreshed.ClientID)

	validated, err := service.ValidateRefreshToken(context.Background(), "refresh-token", ptrInt64(7))
	require.NoError(t, err)
	require.Equal(t, "refreshed-token", validated.AccessToken)

	_, err = service.RefreshAccountToken(context.Background(), &Account{Platform: PlatformOpenAI})
	require.Error(t, err)
	_, err = service.RefreshAccountToken(context.Background(), &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey})
	require.Error(t, err)
	_, err = service.RefreshAccountToken(context.Background(), &Account{Platform: PlatformGrok, Type: AccountTypeOAuth})
	require.Error(t, err)

	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		ProxyID:  ptrInt64(7),
		Credentials: map[string]any{
			"refresh_token":      "refresh-token",
			"client_id":          "client-id",
			"subscription_tier":  "supergrok",
			"entitlement_status": "active",
		},
	}
	accountToken, err := service.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "supergrok", accountToken.SubscriptionTier)
	require.Equal(t, "active", accountToken.EntitlementStatus)

	credentials := service.BuildAccountCredentials(&GrokTokenInfo{
		AccessToken:       "access",
		RefreshToken:      "refresh",
		IDToken:           "id",
		TokenType:         "Bearer",
		ExpiresAt:         1700000000,
		ClientID:          "client",
		Scope:             "scope",
		Email:             "email@example.com",
		SubscriptionTier:  "tier",
		EntitlementStatus: "active",
	})
	require.Equal(t, "access", credentials["access_token"])
	require.Equal(t, "refresh", credentials["refresh_token"])
	require.Equal(t, "https://api.x.ai/v1", credentials["base_url"])
	require.Nil(t, service.BuildAccountCredentials(nil))
}

func TestGrokOAuthServiceProxyAndTokenParsingErrors(t *testing.T) {
	service := NewGrokOAuthService(nil, grokOAuthClientStub{})
	defer service.Stop()

	_, err := service.GenerateAuthURL(context.Background(), ptrInt64(1), "")
	require.Error(t, err)

	proxyError := errors.New("proxy lookup failed")
	service = NewGrokOAuthService(grokOAuthProxyRepoStub{err: proxyError}, grokOAuthClientStub{})
	defer service.Stop()
	_, err = service.GenerateAuthURL(context.Background(), ptrInt64(1), "")
	require.Error(t, err)

	info := service.tokenInfoFromResponse(&xai.TokenResponse{AccessToken: "access", IDToken: "not-a-jwt"}, "", map[string]any{"email": "existing@example.com"})
	require.Equal(t, xai.DefaultClientID, info.ClientID)
	require.Equal(t, "Bearer", info.TokenType)
	require.Equal(t, "existing@example.com", info.Email)
	require.Greater(t, info.ExpiresAt, time.Now().Unix())
	info = service.tokenInfoFromResponse(&xai.TokenResponse{AccessToken: "access", IDToken: "a.invalid"}, "client", nil)
	require.Equal(t, "client", info.ClientID)
}
