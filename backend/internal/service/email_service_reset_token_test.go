//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type resetTokenCacheStub struct {
	emailCacheStub
	stored       *PasswordResetTokenData
	consumedHash string
}

func (s *resetTokenCacheStub) GetPasswordResetToken(context.Context, string) (*PasswordResetTokenData, error) {
	return s.stored, nil
}

func (s *resetTokenCacheStub) SetPasswordResetToken(_ context.Context, _ string, data *PasswordResetTokenData, _ time.Duration) error {
	s.stored = data
	return nil
}

func (s *resetTokenCacheStub) ConsumePasswordResetToken(_ context.Context, _ string, tokenHash string) (bool, error) {
	s.consumedHash = tokenHash
	if s.stored == nil || s.stored.Token != tokenHash {
		return false, nil
	}
	s.stored = nil
	return true, nil
}

func TestConsumePasswordResetToken_ComparesHashNotPlaintext(t *testing.T) {
	token := "deadbeef"
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	require.Equal(t, hash, hashPasswordResetToken(token))
	require.NotEqual(t, token, hashPasswordResetToken(token))

	cache := &resetTokenCacheStub{stored: &PasswordResetTokenData{Token: hash}}
	svc := NewEmailService(nil, cache)

	require.NoError(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", token))
	require.Equal(t, hash, cache.consumedHash)
	require.ErrorIs(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", token), ErrInvalidResetToken)

	// A legacy plaintext value (issued before upgrade) no longer validates.
	cache.stored = &PasswordResetTokenData{Token: token}
	require.ErrorIs(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", token), ErrInvalidResetToken)
}

func TestSendPasswordResetEmail_ReplacesStoredTokenWithHash(t *testing.T) {
	cache := &resetTokenCacheStub{stored: &PasswordResetTokenData{Token: "legacy-plaintext"}}
	svc := NewEmailService(&settingRepoStub{}, cache)
	ctx := context.Background()
	// SMTP is deliberately unconfigured; token persistence precedes delivery.
	require.ErrorIs(t, svc.SendPasswordResetEmail(ctx, "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
	first := cache.stored.Token
	require.Len(t, first, 64)
	_, err := hex.DecodeString(first)
	require.NoError(t, err)
	require.NotEqual(t, "legacy-plaintext", first)
	require.ErrorIs(t, svc.SendPasswordResetEmail(ctx, "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
	require.Len(t, cache.stored.Token, 64)
	require.False(t, strings.EqualFold(first, cache.stored.Token), "a new email replaces the previous link")
}

type notifyAttemptsCacheStub struct {
	EmailCache
	data     *VerificationCodeData
	attempts int
}

func (s *notifyAttemptsCacheStub) GetNotifyVerifyCode(context.Context, string) (*VerificationCodeData, error) {
	return s.data, nil
}

func (s *notifyAttemptsCacheStub) IncrNotifyVerifyCodeAttempts(context.Context, string) (int, error) {
	s.attempts++
	return s.attempts, nil
}

func TestVerifyNotifyCode_ReservesAttemptBeforeComparison(t *testing.T) {
	cache := &notifyAttemptsCacheStub{data: &VerificationCodeData{Code: "123456"}}
	ctx := context.Background()
	for i := 1; i <= 4; i++ {
		require.ErrorIs(t, verifyNotifyCode(ctx, cache, "notify@example.com", "wrong"), ErrInvalidVerifyCode)
	}
	require.ErrorIs(t, verifyNotifyCode(ctx, cache, "notify@example.com", "wrong"), ErrVerifyCodeMaxAttempts)
	require.Equal(t, 5, cache.attempts)
	require.ErrorIs(t, verifyNotifyCode(ctx, cache, "notify@example.com", "123456"), ErrVerifyCodeMaxAttempts)
}
