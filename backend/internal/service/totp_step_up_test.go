//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

type stepUpUserRepoStub struct {
	UserRepository
	user *User
}

func (s *stepUpUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	if s.user == nil {
		return nil, errors.New("user not found")
	}
	return s.user, nil
}

func (s *stepUpUserRepoStub) DisableTotp(context.Context, int64) error {
	s.user.TotpEnabled = false
	return nil
}

type stepUpSettingRepoStub struct {
	SettingRepository
	values map[string]string
}

func (s *stepUpSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", errors.New("setting not found")
	}
	return value, nil
}

type stepUpCacheStub struct {
	TotpCache
	setErr       error
	grantSet     bool
	grantUserID  int64
	grantSession string
	grantTTL     time.Duration
	attempts     int
}

func (s *stepUpCacheStub) GetVerifyAttempts(context.Context, int64) (int, error) {
	return s.attempts, nil
}

func (s *stepUpCacheStub) IncrementVerifyAttempts(context.Context, int64) (int, error) {
	s.attempts++
	return s.attempts, nil
}

func (s *stepUpCacheStub) ClearVerifyAttempts(context.Context, int64) error {
	s.attempts = 0
	return nil
}

func (s *stepUpCacheStub) SetStepUpGrant(_ context.Context, userID int64, sessionKey string, ttl time.Duration) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.grantSet = true
	s.grantUserID = userID
	s.grantSession = sessionKey
	s.grantTTL = ttl
	return nil
}

func (s *stepUpCacheStub) HasStepUpGrant(context.Context, int64, string) (bool, error) {
	return s.grantSet, nil
}

type stepUpEncryptorStub struct{}

func (stepUpEncryptorStub) Encrypt(value string) (string, error) { return value, nil }
func (stepUpEncryptorStub) Decrypt(value string) (string, error) { return value, nil }

func newStepUpService(t *testing.T, user *User, emailEnabled bool, cache *stepUpCacheStub) *TotpService {
	t.Helper()
	values := map[string]string{}
	if emailEnabled {
		values[SettingKeyEmailVerifyEnabled] = "true"
	}
	settings := NewSettingService(&stepUpSettingRepoStub{values: values}, nil)
	return NewTotpService(&stepUpUserRepoStub{user: user}, stepUpEncryptorStub{}, cache, settings, nil, nil)
}

func TestVerifyStepUpBindsGrantToSession(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Sub2API", AccountName: "admin@example.com"})
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)

	cache := &stepUpCacheStub{}
	service := newStepUpService(t, &User{ID: 7, Role: RoleAdmin, TotpEnabled: true, TotpSecretEncrypted: stepUpStringPtr(key.Secret())}, false, cache)

	ttl, err := service.VerifyStepUp(context.Background(), 7, "sid-123", code)
	require.NoError(t, err)
	require.Equal(t, StepUpGrantTTL, ttl)
	require.True(t, cache.grantSet)
	require.Equal(t, int64(7), cache.grantUserID)
	require.Equal(t, "sid-123", cache.grantSession)
	require.Equal(t, StepUpGrantTTL, cache.grantTTL)
}

func TestVerifyStepUpFailsClosedWhenGrantCannotBeStored(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Sub2API", AccountName: "admin@example.com"})
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)

	service := newStepUpService(t, &User{ID: 7, Role: RoleAdmin, TotpEnabled: true, TotpSecretEncrypted: stepUpStringPtr(key.Secret())}, false, &stepUpCacheStub{setErr: errors.New("redis down")})
	_, err = service.VerifyStepUp(context.Background(), 7, "sid-123", code)
	require.Error(t, err)
}

func TestVerificationMethodAndDisableUsePasswordForAdmin(t *testing.T) {
	admin := &User{ID: 1, Role: RoleAdmin, TotpEnabled: true}
	require.NoError(t, admin.SetPassword("correct-password"))
	service := newStepUpService(t, admin, true, &stepUpCacheStub{})

	method, err := service.GetVerificationMethod(context.Background(), admin.ID)
	require.NoError(t, err)
	require.Equal(t, "password", method.Method)
	require.ErrorIs(t, service.Disable(context.Background(), admin.ID, "", ""), ErrPasswordRequired)
	require.NoError(t, service.Disable(context.Background(), admin.ID, "", "correct-password"))
}

func TestVerificationMethodKeepsEmailForRegularUser(t *testing.T) {
	user := &User{ID: 2, Role: RoleUser, TotpEnabled: true}
	service := newStepUpService(t, user, true, &stepUpCacheStub{})

	method, err := service.GetVerificationMethod(context.Background(), user.ID)
	require.NoError(t, err)
	require.Equal(t, "email", method.Method)
	require.ErrorIs(t, service.Disable(context.Background(), user.ID, "", "password"), ErrVerifyCodeRequired)
}

func stepUpStringPtr(value string) *string { return &value }
