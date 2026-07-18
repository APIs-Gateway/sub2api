//go:build unit

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAuthServiceSessionBindingTokenLifecycle(t *testing.T) {
	cache := newEmailBindRefreshTokenCacheStub()
	svc, _, client := newAuthServiceForEmailBindWithRefreshCache(t, map[string]string{
		service.SettingKeySessionBindingEnabled: "true",
	}, nil, nil, cache)
	user := createEmailBindTestUser(t, client, "session-binding@example.com", "session-binding", "password-hash")
	model := &service.User{
		ID:           user.ID,
		Email:        user.Email,
		PasswordHash: "password-hash",
		Role:         user.Role,
		Status:       service.StatusActive,
	}

	boundContext := service.WithSessionBinding(context.Background(), &service.SessionBinding{
		IP:        "192.0.2.10",
		UserAgent: "test-agent/1",
	})
	pair, err := svc.GenerateTokenPair(boundContext, model, "")
	require.NoError(t, err)

	claims, err := svc.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	require.NotEmpty(t, claims.SessionID)
	require.Equal(t, (&service.SessionBinding{IP: "192.0.2.10", UserAgent: "test-agent/1"}).Hash(), claims.BindingHash)

	otherContext := service.WithSessionBinding(context.Background(), &service.SessionBinding{
		IP:        "192.0.2.11",
		UserAgent: "test-agent/1",
	})
	contextToken, err := svc.GenerateTokenWithContext(boundContext, model)
	require.NoError(t, err)
	_, err = svc.RefreshToken(otherContext, contextToken)
	require.ErrorIs(t, err, service.ErrSessionBindingMismatch)

	legacyRefreshPair, err := svc.GenerateTokenPair(boundContext, model, "")
	require.NoError(t, err)
	_, err = svc.RefreshTokenPair(otherContext, legacyRefreshPair.RefreshToken)
	require.ErrorIs(t, err, service.ErrSessionBindingMismatch)

	_, err = svc.RefreshTokenPair(otherContext, pair.RefreshToken)
	require.ErrorIs(t, err, service.ErrSessionBindingMismatch)

	_, err = svc.RefreshTokenPair(boundContext, pair.RefreshToken)
	require.True(t, errors.Is(err, service.ErrRefreshTokenInvalid) || errors.Is(err, service.ErrRefreshTokenNotFound))
	require.NoError(t, svc.RevokeSessionFamily(context.Background(), ""))
}

func TestAuthServiceSessionBindingAllowsLegacyRefreshOnce(t *testing.T) {
	cache := newEmailBindRefreshTokenCacheStub()
	svc, _, client := newAuthServiceForEmailBindWithRefreshCache(t, map[string]string{
		service.SettingKeySessionBindingEnabled: "true",
	}, nil, nil, cache)
	user := createEmailBindTestUser(t, client, "legacy-session@example.com", "legacy-session", "password-hash")
	model := &service.User{
		ID:           user.ID,
		Email:        user.Email,
		PasswordHash: "password-hash",
		Role:         user.Role,
		Status:       service.StatusActive,
	}

	legacyContext := context.Background()
	legacyPair, err := svc.GenerateTokenPair(legacyContext, model, "")
	require.NoError(t, err)

	boundContext := service.WithSessionBinding(context.Background(), &service.SessionBinding{
		IP:        "198.51.100.10",
		UserAgent: "test-agent/2",
	})
	rotated, err := svc.RefreshTokenPair(boundContext, legacyPair.RefreshToken)
	require.NoError(t, err)
	claims, err := svc.ValidateToken(rotated.AccessToken)
	require.NoError(t, err)
	require.NotEmpty(t, claims.BindingHash)
}
