//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type grokOAuthErrorClient struct {
	exchangeErr error
	refreshErr  error
}

func (c grokOAuthErrorClient) ExchangeCode(context.Context, string, string, string, string, string, string) (*xai.TokenResponse, error) {
	if c.exchangeErr != nil {
		return nil, c.exchangeErr
	}
	return &xai.TokenResponse{AccessToken: "access-token"}, nil
}

func (c grokOAuthErrorClient) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	if c.refreshErr != nil {
		return nil, c.refreshErr
	}
	return &xai.TokenResponse{AccessToken: "refreshed-token"}, nil
}

func TestGrokOAuthServiceCoversInputProxyAndClientErrors(t *testing.T) {
	proxy := &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 8080}
	proxyRepo := grokOAuthProxyRepoStub{proxy: proxy}
	service := NewGrokOAuthService(proxyRepo, grokOAuthClientStub{})
	defer service.Stop()

	_, err := service.ExchangeCode(context.Background(), nil)
	require.Error(t, err)
	_, err = service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{SessionID: "missing", Code: "code", State: "state"})
	require.Error(t, err)

	authURL, err := service.GenerateAuthURL(context.Background(), ptrInt64(1), " http://localhost/redirect ")
	require.NoError(t, err)
	_, err = service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: authURL.SessionID,
		Code:      " ",
		State:     authURL.State,
	})
	require.Error(t, err)

	authURL, err = service.GenerateAuthURL(context.Background(), ptrInt64(1), "http://localhost/redirect")
	require.NoError(t, err)
	callback := "https://callback.test/?code=callback-code&state=" + authURL.State
	tokenInfo, err := service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID:   authURL.SessionID,
		Code:        callback,
		State:       "",
		RedirectURI: "http://localhost/override",
		ProxyID:     ptrInt64(1),
	})
	require.NoError(t, err)
	require.Equal(t, "access-token", tokenInfo.AccessToken)

	noProxyService := NewGrokOAuthService(grokOAuthProxyRepoStub{}, grokOAuthClientStub{})
	defer noProxyService.Stop()
	_, err = noProxyService.GenerateAuthURL(context.Background(), ptrInt64(2), "")
	require.NoError(t, err)

	clientErr := errors.New("oauth exchange failed")
	errorService := NewGrokOAuthService(nil, grokOAuthErrorClient{exchangeErr: clientErr, refreshErr: clientErr})
	defer errorService.Stop()
	authURL, err = errorService.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	_, err = errorService.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: authURL.SessionID,
		Code:      "code",
		State:     authURL.State,
	})
	require.ErrorIs(t, err, clientErr)
	_, err = errorService.RefreshToken(context.Background(), "refresh", "", "")
	require.ErrorIs(t, err, clientErr)
}

func TestGrokOAuthServiceCoversRefreshAndTokenParsingBranches(t *testing.T) {
	proxyErr := errors.New("proxy lookup failed")
	service := NewGrokOAuthService(grokOAuthProxyRepoStub{err: proxyErr}, grokOAuthClientStub{})
	defer service.Stop()

	account := &Account{
		ID:       1,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		ProxyID:  ptrInt64(1),
		Credentials: map[string]any{
			"refresh_token": "refresh-token",
		},
	}
	_, err := service.RefreshAccountToken(context.Background(), account)
	require.Error(t, err)

	refreshErr := errors.New("refresh failed")
	service = NewGrokOAuthService(nil, grokOAuthErrorClient{refreshErr: refreshErr})
	defer service.Stop()
	account.ProxyID = nil
	_, err = service.RefreshAccountToken(context.Background(), account)
	require.ErrorIs(t, err, refreshErr)

	emailPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"parsed@example.com"}`))
	info := service.tokenInfoFromResponse(&xai.TokenResponse{
		AccessToken: "access",
		IDToken:     "header." + emailPayload + ".signature",
	}, "", nil)
	require.Equal(t, "parsed@example.com", info.Email)
	info = service.tokenInfoFromResponse(&xai.TokenResponse{
		AccessToken: "access",
		ExpiresIn:   0,
		IDToken:     "header." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".signature",
	}, "", nil)
	require.NotEmpty(t, info.ExpiresAt)
	info = service.tokenInfoFromResponse(&xai.TokenResponse{AccessToken: "access", IDToken: "a.b.c"}, "", nil)
	require.Empty(t, info.Email)
}
