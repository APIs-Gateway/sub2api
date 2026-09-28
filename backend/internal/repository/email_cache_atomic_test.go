package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newMiniredisEmailCache(t *testing.T) (service.EmailCache, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewEmailCache(rdb), mr, rdb
}

func TestEmailCache_ConcurrentWrongCodesCannotExceedAttemptCap(t *testing.T) {
	cache, _, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "user@example.com"

	svc := service.NewEmailService(nil, cache)
	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{
		Code:      "123456",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}, 15*time.Minute))

	const workers = 50
	var invalid, maxed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.VerifyCode(ctx, email, "000000")
			switch {
			case errors.Is(err, service.ErrInvalidVerifyCode):
				invalid.Add(1)
			case errors.Is(err, service.ErrVerifyCodeMaxAttempts):
				maxed.Add(1)
			default:
				t.Errorf("unexpected result: %v", err)
			}
		}()
	}
	wg.Wait()

	// Only attempts 1..4 may return "invalid"; every other guess is rejected by the cap.
	require.LessOrEqual(t, int(invalid.Load()), 4)
	require.Equal(t, workers, int(invalid.Load()+maxed.Load()))

	// Even the correct code is now rejected.
	require.ErrorIs(t, svc.VerifyCode(ctx, email, "123456"), service.ErrVerifyCodeMaxAttempts)

	data, err := cache.GetVerificationCode(ctx, email)
	require.NoError(t, err)
	require.GreaterOrEqual(t, data.Attempts, 5)
}

func TestEmailCache_ResentCodeCannotBeSpentOrDeletedByOldCode(t *testing.T) {
	cache, _, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "race@example.com"
	svc := service.NewEmailService(nil, cache)
	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "111111", CreatedAt: time.Now()}, time.Minute))
	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "222222", CreatedAt: time.Now()}, time.Minute))
	require.ErrorIs(t, svc.VerifyCode(ctx, email, "111111"), service.ErrInvalidVerifyCode)
	data, err := cache.GetVerificationCode(ctx, email)
	require.NoError(t, err)
	require.Equal(t, "222222", data.Code)
	require.Equal(t, 1, data.Attempts)
	require.NoError(t, svc.VerifyCode(ctx, email, "222222"))
	require.ErrorIs(t, svc.VerifyCode(ctx, email, "222222"), service.ErrInvalidVerifyCode)

	require.NoError(t, cache.SetNotifyVerifyCode(ctx, email, &service.VerificationCodeData{Code: "333333", CreatedAt: time.Now()}, time.Minute))
	require.NoError(t, cache.SetNotifyVerifyCode(ctx, email, &service.VerificationCodeData{Code: "444444", CreatedAt: time.Now()}, time.Minute))
	result, err := cache.VerifyNotifyVerifyCode(ctx, email, "333333", 5)
	require.NoError(t, err)
	require.Equal(t, service.VerificationCodeInvalid, result)
	result, err = cache.VerifyNotifyVerifyCode(ctx, email, "444444", 5)
	require.NoError(t, err)
	require.Equal(t, service.VerificationCodeValid, result)
}

func TestEmailCache_ResetCooldownReservationIsExclusiveAndOwnerSafe(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "cooldown@example.com"
	const workers = 30
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := cache.ReservePasswordResetEmailCooldown(ctx, email, fmt.Sprintf("owner-%d", i), time.Minute)
			if err != nil {
				t.Errorf("reserve: %v", err)
				return
			}
			if ok {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, int32(1), won.Load())
	owner, err := mr.Get(passwordResetSentAtKey(email))
	require.NoError(t, err)
	require.NoError(t, cache.ReleasePasswordResetEmailCooldown(ctx, email, "not-owner"))
	require.True(t, mr.Exists(passwordResetSentAtKey(email)))
	require.NoError(t, cache.ReleasePasswordResetEmailCooldown(ctx, email, owner))
	ok, err := cache.ReservePasswordResetEmailCooldown(ctx, email, "retry", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestEmailCache_StagedResetKeepsActiveLinkUntilPromotion(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "staged@example.com"
	previous := &service.PasswordResetTokenData{Token: "old-hash", CreatedAt: time.Now().Add(-time.Minute)}
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, previous, 29*time.Minute))
	reserved, err := cache.ReservePasswordResetEmailCooldown(ctx, email, "owner", time.Minute)
	require.NoError(t, err)
	require.True(t, reserved)
	staged, err := cache.StagePasswordResetToken(ctx, email, "owner", &service.PasswordResetTokenData{Token: "failed-hash"}, 30*time.Minute)
	require.NoError(t, err)
	require.True(t, staged)
	data, err := cache.GetPasswordResetToken(ctx, email)
	require.NoError(t, err)
	require.Equal(t, previous.Token, data.Token)
	consumed, err := cache.ConsumePasswordResetToken(ctx, email, "failed-hash")
	require.NoError(t, err)
	require.False(t, consumed, "pending links cannot be consumed before promotion")
	require.NoError(t, cache.DiscardPendingPasswordResetToken(ctx, email, "wrong-hash"))
	require.True(t, mr.Exists(passwordResetPendingKey(email)))
	require.NoError(t, cache.DiscardPendingPasswordResetToken(ctx, email, "failed-hash"))
	require.False(t, mr.Exists(passwordResetPendingKey(email)))
	require.Equal(t, 29*time.Minute, mr.TTL(passwordResetKey(email)))
	consumed, err = cache.ConsumePasswordResetToken(ctx, email, "old-hash")
	require.NoError(t, err)
	require.True(t, consumed, "the old link can be consumed while a new email is pending")
	staged, err = cache.StagePasswordResetToken(ctx, email, "owner", &service.PasswordResetTokenData{Token: "new-hash"}, 30*time.Minute)
	require.NoError(t, err)
	require.True(t, staged)
	promoted, err := cache.PromotePasswordResetToken(ctx, email, "wrong-owner", "new-hash", 30*time.Second)
	require.NoError(t, err)
	require.False(t, promoted)
	promoted, err = cache.PromotePasswordResetToken(ctx, email, "owner", "new-hash", 30*time.Second)
	require.NoError(t, err)
	require.True(t, promoted)
	consumed, err = cache.ConsumePasswordResetToken(ctx, email, "old-hash")
	require.NoError(t, err)
	require.False(t, consumed, "promoting a new hash must not revive a consumed old link")
	data, err = cache.GetPasswordResetToken(ctx, email)
	require.NoError(t, err)
	require.Equal(t, "new-hash", data.Token)
	require.Equal(t, 30*time.Second, mr.TTL(passwordResetSentAtKey(email)))
}

func TestEmailCache_ExpiredOwnerCannotPromoteLateResetEmail(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "expired-owner@example.com"
	reserved, err := cache.ReservePasswordResetEmailCooldown(ctx, email, "expired", time.Second)
	require.NoError(t, err)
	require.True(t, reserved)
	staged, err := cache.StagePasswordResetToken(ctx, email, "expired", &service.PasswordResetTokenData{Token: "old-pending"}, time.Minute)
	require.NoError(t, err)
	require.True(t, staged)
	mr.FastForward(2 * time.Second)
	reserved, err = cache.ReservePasswordResetEmailCooldown(ctx, email, "new-owner", time.Minute)
	require.NoError(t, err)
	require.True(t, reserved)
	staged, err = cache.StagePasswordResetToken(ctx, email, "new-owner", &service.PasswordResetTokenData{Token: "new-pending"}, time.Minute)
	require.NoError(t, err)
	require.True(t, staged)
	staged, err = cache.StagePasswordResetToken(ctx, email, "expired", &service.PasswordResetTokenData{Token: "late-old"}, time.Minute)
	require.NoError(t, err)
	require.False(t, staged)
	promoted, err := cache.PromotePasswordResetToken(ctx, email, "expired", "old-pending", 30*time.Second)
	require.NoError(t, err)
	require.False(t, promoted)
	require.NoError(t, cache.DiscardPendingPasswordResetToken(ctx, email, "old-pending"))
	require.True(t, mr.Exists(passwordResetPendingKey(email)))
	promoted, err = cache.PromotePasswordResetToken(ctx, email, "new-owner", "new-pending", 30*time.Second)
	require.NoError(t, err)
	require.True(t, promoted)
}

func TestEmailCache_AttemptsResetOnNewCodeAndTTLFollowsCode(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "User@Example.com"

	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "1"}, time.Minute))
	result, err := cache.VerifyVerificationCode(ctx, email, "wrong", 5)
	require.NoError(t, err)
	require.Equal(t, service.VerificationCodeInvalid, result)
	require.Greater(t, mr.TTL(verifyCodeKey(email)+attemptsKeySuffix), time.Duration(0))

	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "2"}, time.Minute))
	data, err := cache.GetVerificationCode(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 0, data.Attempts)

	require.NoError(t, cache.DeleteVerificationCode(ctx, email))
	result, err = cache.VerifyVerificationCode(ctx, email, "2", 5)
	require.NoError(t, err)
	require.Equal(t, service.VerificationCodeInvalid, result)
	require.False(t, mr.Exists(verifyCodeKey(email)+attemptsKeySuffix))
}

func TestEmailCache_LegacyAttemptsRemainWithinFiveComparisonBudget(t *testing.T) {
	cache, _, rdb := newMiniredisEmailCache(t)
	ctx := context.Background()
	key := "invite:legacy@example.com"
	// This is the JSON shape left by the previous release. It has no side
	// counter, so the first new attempt must reserve attempt five, not one.
	require.NoError(t, rdb.Set(ctx, verifyCodeKey(key), `{"Code":"123456","Attempts":4}`, time.Minute).Err())
	svc := service.NewEmailService(nil, cache)
	require.ErrorIs(t, svc.VerifyScopedCode(ctx, "invite", "legacy@example.com", "wrong"), service.ErrVerifyCodeMaxAttempts)
	data, err := cache.GetVerificationCode(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 5, data.Attempts)
	require.ErrorIs(t, svc.VerifyScopedCode(ctx, "invite", "legacy@example.com", "123456"), service.ErrVerifyCodeMaxAttempts)
	// A newly issued code restores its own complete budget in the same scope.
	require.NoError(t, cache.SetVerificationCode(ctx, key, &service.VerificationCodeData{Code: "654321"}, time.Minute))
	require.NoError(t, svc.VerifyScopedCode(ctx, "invite", "legacy@example.com", "654321"))
}

func TestEmailCache_NotifyAttemptsReserveAgainstLegacyCounter(t *testing.T) {
	cache, _, rdb := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "notify@example.com"
	require.NoError(t, rdb.Set(ctx, notifyVerifyKey(email), `{"Code":"123456","Attempts":4}`, time.Minute).Err())
	result, err := cache.VerifyNotifyVerifyCode(ctx, email, "wrong", 5)
	require.NoError(t, err)
	require.Equal(t, service.VerificationCodeMaxed, result)
	data, err := cache.GetNotifyVerifyCode(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 5, data.Attempts)
	result, err = cache.VerifyNotifyVerifyCode(ctx, email, "123456", 5)
	require.NoError(t, err)
	require.Equal(t, service.VerificationCodeMaxed, result)
}

func TestEmailCache_PasswordResetTokenHashedAndSingleUse(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "reset@example.com"

	svc := service.NewEmailService(nil, cache)

	// Seed the token the same way SendPasswordResetEmail does (hash only).
	token, err := svc.GeneratePasswordResetToken()
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(token))
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{
		Token: hex.EncodeToString(sum[:]), CreatedAt: time.Now(),
	}, 30*time.Minute))

	raw, err := mr.Get(passwordResetKey(email))
	require.NoError(t, err)
	require.False(t, strings.Contains(raw, token), "plaintext token must not be stored")

	require.NoError(t, svc.VerifyPasswordResetToken(ctx, email, token))
	require.ErrorIs(t, svc.ConsumePasswordResetToken(ctx, email, "wrong"), service.ErrInvalidResetToken)

	const workers = 30
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if svc.ConsumePasswordResetToken(ctx, email, token) == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), ok.Load())
	require.False(t, mr.Exists(passwordResetKey(email)))
}

func TestEmailCache_ConsumePasswordResetTokenMismatchKeepsToken(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "keep@example.com"
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{Token: "abc"}, time.Minute))

	ok, err := cache.ConsumePasswordResetToken(ctx, email, "xyz")
	require.NoError(t, err)
	require.False(t, ok)
	require.True(t, mr.Exists(passwordResetKey(email)))

	ok, err = cache.ConsumePasswordResetToken(ctx, email, "abc")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.ConsumePasswordResetToken(ctx, email, "abc")
	require.NoError(t, err)
	require.False(t, ok)
}
