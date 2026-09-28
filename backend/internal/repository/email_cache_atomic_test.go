package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func TestEmailCache_AttemptsResetOnNewCodeAndTTLFollowsCode(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "User@Example.com"

	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "1"}, time.Minute))
	n, err := cache.IncrVerificationCodeAttempts(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Greater(t, mr.TTL(verifyCodeKey(email)+attemptsKeySuffix), time.Duration(0))

	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "2"}, time.Minute))
	data, err := cache.GetVerificationCode(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 0, data.Attempts)

	require.NoError(t, cache.DeleteVerificationCode(ctx, email))
	_, err = cache.IncrVerificationCodeAttempts(ctx, email)
	require.Error(t, err)
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
	n, err := cache.IncrNotifyVerifyCodeAttempts(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 5, n)
	n, err = cache.IncrNotifyVerifyCodeAttempts(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 6, n)
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
