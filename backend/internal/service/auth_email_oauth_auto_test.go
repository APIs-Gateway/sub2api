//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newEmailOAuthAutoAuthService(
	userRepo UserRepository,
	settings map[string]string,
	quotaRepo UserPlatformQuotaRepository,
) *AuthService {
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:                   "test-secret",
			ExpireHour:               1,
			AccessTokenExpireMinutes: 60,
			RefreshTokenExpireDays:   7,
		},
		Default: config.DefaultConfig{
			UserBalance:     3.5,
			UserConcurrency: 2,
		},
	}

	settingService := NewSettingService(&settingRepoStub{values: settings}, cfg)

	return NewAuthService(
		nil, // entClient — nil, updateUserSignupSource early return
		userRepo,
		nil, // redeemRepo — invitationCode="" 时不触发
		&refreshTokenCacheStub{},
		cfg,
		settingService,
		nil, // emailService
		nil, // turnstileService
		nil, // emailQueueService
		nil, // promoService
		nil, // defaultSubAssigner — nil, assignSubscriptions early return
		nil, // affiliateService — nil, bindOAuthAffiliate early return
		quotaRepo,
	)
}

func TestEmailOAuthAuto_SnapshotsPlatformQuotaDefaults(t *testing.T) {
	userRepo := &userRepoStub{nextID: 88}
	quotaRepo := &userPlatformQuotaRepoStub{}

	svc := newEmailOAuthAutoAuthService(
		userRepo,
		map[string]string{
			SettingKeyRegistrationEnabled:   "true",
			SettingKeyDefaultPlatformQuotas: `{"gemini": {"monthly": 100.0}}`,
		},
		quotaRepo,
	)

	user, err := svc.createEmailOAuthUser(
		context.Background(),
		"newoauth@example.com",
		"newoauth",
		"github",
		"", // invitationCode
		"", // affiliateCode
	)
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, int64(88), user.ID)

	require.Len(t, quotaRepo.bulkInsertCalls, 1, "createEmailOAuthUser must snapshot platform quotas via BulkInsertInitial")

	records := quotaRepo.bulkInsertCalls[0]
	require.Len(t, records, 1, "only platforms with a configured limit get a row")
	var geminiRecord *UserPlatformQuotaRecord
	for i := range records {
		if records[i].Platform == "gemini" {
			geminiRecord = &records[i]
			break
		}
	}
	require.NotNil(t, geminiRecord, "expected gemini platform record")
	require.NotNil(t, geminiRecord.MonthlyLimitUSD)
	require.InDelta(t, 100.0, *geminiRecord.MonthlyLimitUSD, 0.0001)
}

func TestEmailOAuthCanAutoLinkExistingUser(t *testing.T) {
	require.False(t, emailOAuthCanAutoLinkExistingUser(nil))
	for _, source := range []string{"", "email", "oidc", "linuxdo"} {
		require.False(t, emailOAuthCanAutoLinkExistingUser(&User{SignupSource: source}), source)
	}
	for _, source := range []string{"google", "GitHub"} {
		require.True(t, emailOAuthCanAutoLinkExistingUser(&User{SignupSource: source}), source)
	}
}

func TestCreateEmailOAuthUserReportsConcurrentExistingAccount(t *testing.T) {
	repo := &userRepoStub{createErr: ErrEmailExists}
	svc := newEmailOAuthAutoAuthService(repo, map[string]string{SettingKeyRegistrationEnabled: "true"}, nil)
	user, err := svc.createEmailOAuthUser(context.Background(), "raced@example.com", "raced", "google", "", "")
	require.ErrorIs(t, err, errEmailOAuthRegistrationRace)
	require.Nil(t, user)
	require.Empty(t, repo.created)
}

type emailOAuthRaceRepo struct {
	UserRepository
	existing  *User
	reloadErr error
	createErr error
	lookups   int
}

func (r *emailOAuthRaceRepo) GetByEmail(context.Context, string) (*User, error) {
	r.lookups++
	if r.lookups == 1 {
		return nil, ErrUserNotFound
	}
	if r.reloadErr != nil {
		return nil, r.reloadErr
	}
	return r.existing, nil
}

func (r *emailOAuthRaceRepo) Create(context.Context, *User) error {
	if r.createErr != nil {
		return r.createErr
	}
	return ErrEmailExists
}

func TestEmailOAuthConcurrentLocalRegistrationCannotBind(t *testing.T) {
	_, client := newAuthPendingIdentityServiceTestClient(t)
	repo := &emailOAuthRaceRepo{existing: &User{ID: 42, Email: "raced@example.com", Status: StatusActive, SignupSource: "email"}}
	svc := newEmailOAuthAutoAuthService(repo, map[string]string{SettingKeyRegistrationEnabled: "true"}, nil)
	svc.entClient = client

	tokens, user, err := svc.LoginOrRegisterVerifiedEmailOAuth(context.Background(), EmailOAuthIdentityInput{
		ProviderType: "google", ProviderSubject: "google-raced", Email: "raced@example.com", EmailVerified: true,
	})

	require.ErrorIs(t, err, ErrOAuthExistingAccountBindRequired)
	require.Nil(t, tokens)
	require.Nil(t, user)
	require.Equal(t, 2, repo.lookups)
	identityCount, err := client.AuthIdentity.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, identityCount)
}

func TestEmailOAuthConcurrentRegistrationWinnerDisappears(t *testing.T) {
	_, client := newAuthPendingIdentityServiceTestClient(t)
	repo := &emailOAuthRaceRepo{reloadErr: ErrUserNotFound}
	svc := newEmailOAuthAutoAuthService(repo, map[string]string{SettingKeyRegistrationEnabled: "true"}, nil)
	svc.entClient = client

	tokens, user, err := svc.LoginOrRegisterVerifiedEmailOAuth(context.Background(), EmailOAuthIdentityInput{
		ProviderType: "google", ProviderSubject: "google-raced-missing", Email: "missing@example.com", EmailVerified: true,
	})

	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Nil(t, tokens)
	require.Nil(t, user)
	require.Equal(t, 2, repo.lookups)
	identityCount, err := client.AuthIdentity.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, identityCount)
}

func TestEmailOAuthRegistrationCreateFailureDoesNotBind(t *testing.T) {
	_, client := newAuthPendingIdentityServiceTestClient(t)
	repo := &emailOAuthRaceRepo{createErr: ErrServiceUnavailable}
	svc := newEmailOAuthAutoAuthService(repo, map[string]string{SettingKeyRegistrationEnabled: "true"}, nil)
	svc.entClient = client

	tokens, user, err := svc.LoginOrRegisterVerifiedEmailOAuth(context.Background(), EmailOAuthIdentityInput{
		ProviderType: "google", ProviderSubject: "google-create-failed", Email: "failed@example.com", EmailVerified: true,
	})

	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Nil(t, tokens)
	require.Nil(t, user)
	require.Equal(t, 1, repo.lookups)
	identityCount, err := client.AuthIdentity.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, identityCount)
}
