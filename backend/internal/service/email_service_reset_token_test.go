//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type resetTokenCacheStub struct {
	emailCacheStub
	stored       *PasswordResetTokenData
	consumedHash string
	cooldownOwner string
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

func (s *resetTokenCacheStub) RestorePasswordResetToken(_ context.Context, _ string, failedHash string, previous *PasswordResetTokenData, _ time.Duration) error {
	if s.stored != nil && s.stored.Token == failedHash {
		s.stored = previous
	}
	return nil
}

func (s *resetTokenCacheStub) ReservePasswordResetEmailCooldown(_ context.Context, _, owner string, _ time.Duration) (bool, error) {
	if s.cooldownOwner != "" {
		return false, nil
	}
	s.cooldownOwner = owner
	return true, nil
}

func (s *resetTokenCacheStub) ReleasePasswordResetEmailCooldown(_ context.Context, _, owner string) error {
	if s.cooldownOwner == owner {
		s.cooldownOwner = ""
	}
	return nil
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

func TestSendPasswordResetEmail_FailedDeliveryRestoresPreviousHash(t *testing.T) {
	previous := &PasswordResetTokenData{Token: hashPasswordResetToken("previous"), CreatedAt: time.Now()}
	cache := &resetTokenCacheStub{stored: previous}
	svc := NewEmailService(&settingRepoStub{}, cache)
	ctx := context.Background()
	// SMTP is deliberately unconfigured. The old link remains valid after failure.
	require.ErrorIs(t, svc.SendPasswordResetEmail(ctx, "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
	require.Equal(t, previous, cache.stored)
	require.NoError(t, svc.VerifyPasswordResetToken(ctx, "a@example.com", "previous"))
	require.ErrorIs(t, svc.SendPasswordResetEmail(ctx, "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
	require.Equal(t, previous, cache.stored)
}

func TestSendPasswordResetEmailWithCooldown_FailedDeliveryCanRetry(t *testing.T) {
	cache := &resetTokenCacheStub{}
	svc := NewEmailService(&settingRepoStub{}, cache)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		require.ErrorIs(t, svc.SendPasswordResetEmailWithCooldown(ctx, "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
		require.Empty(t, cache.cooldownOwner)
		require.Nil(t, cache.stored)
	}
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

func (s *notifyAttemptsCacheStub) VerifyNotifyVerifyCode(_ context.Context, _, code string, limit int) (VerificationCodeResult, error) {
	if s.data == nil {
		return VerificationCodeInvalid, nil
	}
	if s.attempts >= limit {
		return VerificationCodeMaxed, nil
	}
	s.attempts++
	if s.data.Code == code {
		s.data = nil
		return VerificationCodeValid, nil
	}
	if s.attempts >= limit {
		return VerificationCodeMaxed, nil
	}
	return VerificationCodeInvalid, nil
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
