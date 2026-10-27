//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type resetTokenCacheStub struct {
	emailCacheStub
	stored          *PasswordResetTokenData
	pending         *PasswordResetTokenData
	consumedHash    string
	consumeErr      error
	cooldownOwner   string
	onStage         func()
	reserveErr      error
	stageErr        error
	stageRejected   bool
	promoteErr      error
	promoteRejected bool
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
	if s.consumeErr != nil {
		return false, s.consumeErr
	}
	if s.stored == nil || s.stored.Token != tokenHash {
		return false, nil
	}
	s.stored = nil
	return true, nil
}

func (s *resetTokenCacheStub) StagePasswordResetToken(_ context.Context, _, owner string, data *PasswordResetTokenData, _ time.Duration) (bool, error) {
	if s.stageErr != nil {
		return false, s.stageErr
	}
	if s.stageRejected {
		return false, nil
	}
	if s.cooldownOwner != owner {
		return false, nil
	}
	s.pending = data
	if s.onStage != nil {
		s.onStage()
	}
	return true, nil
}

func (s *resetTokenCacheStub) PromotePasswordResetToken(_ context.Context, _, owner, tokenHash string, _ time.Duration) (bool, error) {
	if s.promoteErr != nil {
		return false, s.promoteErr
	}
	if s.promoteRejected {
		return false, nil
	}
	if s.cooldownOwner != owner || s.pending == nil || s.pending.Token != tokenHash {
		return false, nil
	}
	s.stored = s.pending
	s.pending = nil
	return true, nil
}

func (s *resetTokenCacheStub) DiscardPendingPasswordResetToken(_ context.Context, _ string, tokenHash string) error {
	if s.pending != nil && s.pending.Token == tokenHash {
		s.pending = nil
	}
	return nil
}

func (s *resetTokenCacheStub) ReservePasswordResetEmailCooldown(_ context.Context, _, owner string, _ time.Duration) (bool, error) {
	if s.reserveErr != nil {
		return false, s.reserveErr
	}
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

func TestConsumePasswordResetToken_RedisFailureDoesNotAuthorizeReset(t *testing.T) {
	token := "one-time-token"
	cache := &resetTokenCacheStub{
		stored: &PasswordResetTokenData{Token: hashPasswordResetToken(token)},
		consumeErr: errors.New("redis unavailable"),
	}
	svc := NewEmailService(nil, cache)
	require.ErrorIs(t, svc.ConsumePasswordResetToken(context.Background(), "a@example.com", token), ErrInvalidResetToken)
	require.Equal(t, hashPasswordResetToken(token), cache.consumedHash)
	require.NotNil(t, cache.stored, "a cache failure must not claim that the token was consumed")
}

func TestSendPasswordResetEmail_FailedDeliveryKeepsPreviousHash(t *testing.T) {
	previous := &PasswordResetTokenData{Token: hashPasswordResetToken("previous"), CreatedAt: time.Now()}
	cache := &resetTokenCacheStub{stored: previous}
	svc := NewEmailService(&settingRepoStub{}, cache)
	ctx := context.Background()
	// SMTP is deliberately unconfigured. The old link remains valid after failure.
	require.ErrorIs(t, svc.SendPasswordResetEmail(ctx, "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
	require.Equal(t, previous, cache.stored)
	require.Nil(t, cache.pending)
	require.NoError(t, svc.VerifyPasswordResetToken(ctx, "a@example.com", "previous"))
	require.ErrorIs(t, svc.SendPasswordResetEmail(ctx, "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
	require.Equal(t, previous, cache.stored)
}

func TestSendPasswordResetEmail_ConsumedPreviousLinkIsNotRevivedOnFailure(t *testing.T) {
	cache := &resetTokenCacheStub{stored: &PasswordResetTokenData{Token: hashPasswordResetToken("previous")}}
	cache.onStage = func() { cache.stored = nil }
	svc := NewEmailService(&settingRepoStub{}, cache)
	require.ErrorIs(t, svc.SendPasswordResetEmail(context.Background(), "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
	require.Nil(t, cache.stored)
	require.Nil(t, cache.pending)
}

func TestSendPasswordResetEmail_DirectAndQueuedPathsShareReservation(t *testing.T) {
	cache := &resetTokenCacheStub{cooldownOwner: "other-worker"}
	svc := NewEmailService(&settingRepoStub{}, cache)
	ctx := context.Background()
	require.ErrorIs(t, svc.SendPasswordResetEmail(ctx, "a@example.com", "Site", "https://example.com/reset"), errPasswordResetEmailCooldown)
	require.NoError(t, svc.SendPasswordResetEmailWithCooldown(ctx, "a@example.com", "Site", "https://example.com/reset"))
	require.Nil(t, cache.pending)
}

func TestSendPasswordResetEmail_ConcurrentDirectRequestCannotReplacePendingLink(t *testing.T) {
	cache := &resetTokenCacheStub{stored: &PasswordResetTokenData{Token: hashPasswordResetToken("previous")}}
	svc := NewEmailService(&settingRepoStub{}, cache)
	cache.onStage = func() {
		require.ErrorIs(t, svc.SendPasswordResetEmail(context.Background(), "a@example.com", "Site", "https://example.com/reset"), errPasswordResetEmailCooldown)
		require.NotNil(t, cache.pending)
	}
	require.ErrorIs(t, svc.SendPasswordResetEmail(context.Background(), "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
	require.Equal(t, hashPasswordResetToken("previous"), cache.stored.Token)
}

func TestSendPasswordResetEmail_SuccessPromotesOnlyAfterDelivery(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	cache := &resetTokenCacheStub{stored: &PasswordResetTokenData{Token: hashPasswordResetToken("previous")}}
	settings := &settingRepoStub{values: map[string]string{
		SettingKeySMTPHost:     "127.0.0.1",
		SettingKeySMTPPort:     strconv.Itoa(port),
		SettingKeySMTPUsername: "user",
		SettingKeySMTPPassword: "pass",
		SettingKeySMTPFrom:     "noreply@example.com",
	}}
	svc := NewEmailService(settings, cache)
	cache.onStage = func() {
		require.Equal(t, hashPasswordResetToken("previous"), cache.stored.Token)
	}
	require.NoError(t, svc.SendPasswordResetEmail(context.Background(), "a@example.com", "Site", "https://example.com/reset"))
	require.True(t, srv.sawCommand("DATA"))
	require.NotNil(t, cache.stored)
	require.Len(t, cache.stored.Token, 64)
	require.NotEqual(t, hashPasswordResetToken("previous"), cache.stored.Token)
	require.Nil(t, cache.pending)
	require.ErrorIs(t, svc.VerifyPasswordResetToken(context.Background(), "a@example.com", "previous"), ErrInvalidResetToken)
}

func TestSendPasswordResetEmailWithCooldown_FailedDeliveryCanRetry(t *testing.T) {
	cache := &resetTokenCacheStub{}
	svc := NewEmailService(&settingRepoStub{}, cache)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		require.ErrorIs(t, svc.SendPasswordResetEmailWithCooldown(ctx, "a@example.com", "Site", "https://example.com/reset"), ErrEmailNotConfigured)
		require.Empty(t, cache.cooldownOwner)
		require.Nil(t, cache.stored)
		require.Nil(t, cache.pending)
	}
}

func TestSendPasswordResetEmail_ReservationAndStagingFailuresKeepActiveLink(t *testing.T) {
	previous := &PasswordResetTokenData{Token: hashPasswordResetToken("previous")}
	for _, tc := range []struct {
		name  string
		cache *resetTokenCacheStub
	}{
		{"reserve error", &resetTokenCacheStub{stored: previous, reserveErr: errors.New("redis unavailable")}},
		{"stage error", &resetTokenCacheStub{stored: previous, stageErr: errors.New("redis unavailable")}},
		{"lost owner before stage", &resetTokenCacheStub{stored: previous, stageRejected: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewEmailService(&settingRepoStub{}, tc.cache)
			require.Error(t, svc.SendPasswordResetEmail(context.Background(), "a@example.com", "Site", "https://example.com/reset"))
			require.Equal(t, previous, tc.cache.stored)
			require.Nil(t, tc.cache.pending)
			require.Empty(t, tc.cache.cooldownOwner)
		})
	}
}

func TestSendPasswordResetEmail_PromotionFailureFailsClosed(t *testing.T) {
	_, port := startFakeSMTPServer(t, false, false)
	settings := &settingRepoStub{values: map[string]string{
		SettingKeySMTPHost:     "127.0.0.1",
		SettingKeySMTPPort:     strconv.Itoa(port),
		SettingKeySMTPUsername: "user",
		SettingKeySMTPPassword: "pass",
		SettingKeySMTPFrom:     "noreply@example.com",
	}}
	for _, tc := range []struct {
		name  string
		cache *resetTokenCacheStub
	}{
		{"owner expired", &resetTokenCacheStub{promoteRejected: true}},
		{"redis error", &resetTokenCacheStub{promoteErr: errors.New("redis unavailable")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := &PasswordResetTokenData{Token: hashPasswordResetToken("previous")}
			tc.cache.stored = previous
			svc := NewEmailService(settings, tc.cache)
			require.Error(t, svc.SendPasswordResetEmail(context.Background(), "a@example.com", "Site", "https://example.com/reset"))
			require.Equal(t, previous, tc.cache.stored)
			require.Nil(t, tc.cache.pending)
			require.Empty(t, tc.cache.cooldownOwner)
		})
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
