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

func (grokOAuthClientStub) ExchangeCode(context.Context, string, string, string, string, string) (*xai.TokenResponse, error) {
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
	require.Contains(t, err.Error(), "GROK_OAUTH_STATE_REQUIRED")

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

type grokOAuthRedirectCaptureClient struct {
	exchangeCalls       int
	exchangeRedirectURI string
}

func (c *grokOAuthRedirectCaptureClient) ExchangeCode(_ context.Context, _, _, redirectURI, _, _ string) (*xai.TokenResponse, error) {
	c.exchangeCalls++
	c.exchangeRedirectURI = redirectURI
	return &xai.TokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

func (c *grokOAuthRedirectCaptureClient) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "refreshed-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

// redirect_uri 与授权会话绑定：客户端改写 redirect_uri 必须被拒绝且不消耗 session，
// 一致或留空时交换一律使用 session 记录的值（upstream #5408）。
func TestGrokOAuthServiceExchangeCodeBindsRedirectURIToSession(t *testing.T) {
	client := &grokOAuthRedirectCaptureClient{}
	service := NewGrokOAuthService(nil, client)
	defer service.Stop()

	authURL, err := service.GenerateAuthURL(context.Background(), nil, "http://localhost/callback")
	require.NoError(t, err)

	_, err = service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID:   authURL.SessionID,
		Code:        "authorization-code",
		State:       authURL.State,
		RedirectURI: "http://127.0.0.1:9999/callback",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_REDIRECT_URI_MISMATCH")
	require.Zero(t, client.exchangeCalls)
	_, ok := service.sessionStore.Get(authURL.SessionID)
	require.True(t, ok, "mismatched redirect_uri must not consume the session")

	_, err = service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID:   authURL.SessionID,
		Code:        "authorization-code",
		State:       authURL.State,
		RedirectURI: " http://localhost/callback ",
	})
	require.NoError(t, err)
	require.Equal(t, 1, client.exchangeCalls)
	require.Equal(t, "http://localhost/callback", client.exchangeRedirectURI)

	authURL, err = service.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	_, err = service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: authURL.SessionID,
		Code:      "authorization-code",
		State:     authURL.State,
	})
	require.NoError(t, err)
	require.Equal(t, xai.EffectiveRedirectURI(""), client.exchangeRedirectURI)
}

type grokOAuthEmptyTokenClient struct{}

func (grokOAuthEmptyTokenClient) ExchangeCode(context.Context, string, string, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{}, nil
}

func (grokOAuthEmptyTokenClient) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return nil, nil
}

// 上游返回缺 access_token（或空）的 token 响应时必须报错，不能生成空凭证（upstream #5408）。
func TestGrokOAuthServiceRejectsEmptyUpstreamTokenResponse(t *testing.T) {
	service := NewGrokOAuthService(nil, grokOAuthEmptyTokenClient{})
	defer service.Stop()

	require.NotPanics(t, func() {
		info, err := service.RefreshToken(context.Background(), "refresh-token", "", "client-id")
		require.Nil(t, info)
		require.Error(t, err)
		require.Contains(t, err.Error(), "GROK_OAUTH_INVALID_TOKEN_RESPONSE")
	})

	authURL, err := service.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	info, err := service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: authURL.SessionID,
		Code:      "authorization-code",
		State:     authURL.State,
	})
	require.Nil(t, info)
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_INVALID_TOKEN_RESPONSE")
}

type grokOAuthRefreshResponseStub struct {
	grokOAuthClientStub
	refreshResponse *xai.TokenResponse
}

func (s *grokOAuthRefreshResponseStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return s.refreshResponse, nil
}

func TestGrokOAuthServiceRefreshTokenPreservesOriginalRefreshTokenWhenNotRotated(t *testing.T) {
	svc := NewGrokOAuthService(nil, &grokOAuthRefreshResponseStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "new-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	defer svc.Stop()

	info, err := svc.RefreshToken(context.Background(), "original-refresh-token", "", "client-id")
	require.NoError(t, err)
	require.Equal(t, "new-access-token", info.AccessToken)
	require.Equal(t, "original-refresh-token", info.RefreshToken)
	require.Equal(t, "client-id", info.ClientID)
}

type grokOAuthExchangeCountingClient struct {
	exchangeCalls int
	exchangeErr   error
}

func (c *grokOAuthExchangeCountingClient) ExchangeCode(context.Context, string, string, string, string, string) (*xai.TokenResponse, error) {
	c.exchangeCalls++
	if c.exchangeErr != nil {
		return nil, c.exchangeErr
	}
	return &xai.TokenResponse{AccessToken: "access-token"}, nil
}

func (c *grokOAuthExchangeCountingClient) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "refreshed-token"}, nil
}

// 回调 URL 里缺 state 必须拒绝且不向 xAI 发起交换；一旦真正发起过交换（无论成败），
// session 即作废，不能用同一 session 重放（upstream f29ccc7df）。
func TestGrokOAuthServiceExchangeCodeRequiresStateForCallbackURLAndConsumesSession(t *testing.T) {
	client := &grokOAuthExchangeCountingClient{exchangeErr: errors.New("upstream exchange failed")}
	svc := NewGrokOAuthService(nil, client)
	defer svc.Stop()

	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)

	_, err = svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "http://127.0.0.1:56121/callback?code=code-without-state",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_STATE_REQUIRED")
	require.Zero(t, client.exchangeCalls)

	_, err = svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "code-with-state",
		State:     auth.State,
	})
	require.Error(t, err)
	require.Equal(t, 1, client.exchangeCalls)

	_, err = svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "code-with-state",
		State:     auth.State,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_SESSION_NOT_FOUND")
	require.Equal(t, 1, client.exchangeCalls)
}
