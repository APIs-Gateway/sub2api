//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type wsTurnFundingUserRepo struct {
	UserRepository
	user  *User
	err   error
	reads int
}

func (r *wsTurnFundingUserRepo) GetByID(context.Context, int64) (*User, error) {
	r.reads++
	return r.user, r.err
}

type wsTurnFundingCardRepo struct {
	UserSubscriptionRepository
	card  *UserSubscription
	err   error
	reads int
}

func (r *wsTurnFundingCardRepo) GetActiveByUserID(context.Context, int64) (*UserSubscription, error) {
	r.reads++
	return r.card, r.err
}

type wsTurnFundingBalanceCache struct {
	BillingCache
	reads int
}

func (r *wsTurnFundingBalanceCache) GetUserBalance(context.Context, int64) (float64, error) {
	r.reads++
	return 100, nil
}

type wsTurnFundingRPMCache struct {
	UserRPMCache
	increments int
}

func (r *wsTurnFundingRPMCache) IncrementUserRPM(context.Context, int64) (int, error) {
	r.increments++
	return r.increments, nil
}

func (r *wsTurnFundingRPMCache) IncrementUserGroupRPM(context.Context, int64, int64) (int, error) {
	r.increments++
	return r.increments, nil
}

func TestWSTurnFunding_FreshCreditAdmissionWithoutRPM(t *testing.T) {
	limit := 2.0
	for _, name := range []string{"positive_wallet", "zero_wallet", "negative_wallet", "negative_wallet_card_remaining", "zero_wallet_card_exhausted", "positive_wallet_card_exhausted", "negative_wallet_card_expired", "negative_wallet_new_card_ignores_no_card_cache", "positive_wallet_without_card_repo", "negative_wallet_without_card_repo", "inactive_user", "user_read_error", "missing_user", "card_read_error_negative_wallet", "card_read_error_positive_wallet"} {
		t.Run(name, func(t *testing.T) {
			user := &User{ID: 7002, Status: StatusActive, Balance: .25, RPMLimit: 100}
			users := &wsTurnFundingUserRepo{user: user}
			cards := &wsTurnFundingCardRepo{err: ErrSubscriptionNotFound}
			cache := &wsTurnFundingBalanceCache{}
			rpm := &wsTurnFundingRPMCache{}
			svc := &BillingCacheService{cfg: &config.Config{RunMode: config.RunModeStandard}, userRepo: users, subRepo: cards, cache: cache, userRPMCache: rpm}
			var expected error
			switch name {
			case "zero_wallet":
				user.Balance, expected = 0, ErrInsufficientBalance
			case "negative_wallet":
				user.Balance, expected = -.25, ErrInsufficientBalance
			case "negative_wallet_card_remaining", "negative_wallet_new_card_ignores_no_card_cache":
				user.Balance = -.25
				cards.err = nil
				cards.card = &UserSubscription{Status: SubscriptionStatusActive, ExpiresAt: time.Now().Add(time.Hour), DailyLimitUSD: &limit, WeeklyLimitUSD: &limit, MonthlyLimitUSD: &limit}
				svc.setNoSubscriptionLockCache(user.ID)
			case "zero_wallet_card_exhausted", "positive_wallet_card_exhausted":
				if name == "zero_wallet_card_exhausted" {
					user.Balance, expected = 0, ErrDailyLimitExceeded
				}
				cards.err = nil
				day, week, month := time.Now(), time.Now(), time.Now()
				cards.card = &UserSubscription{Status: SubscriptionStatusActive, ExpiresAt: time.Now().Add(time.Hour), DailyLimitUSD: &limit, WeeklyLimitUSD: &limit, MonthlyLimitUSD: &limit, DailyUsageUSD: 2, WeeklyUsageUSD: 2, MonthlyUsageUSD: 2, DailyWindowStart: &day, WeeklyWindowStart: &week, MonthlyWindowStart: &month}
			case "negative_wallet_card_expired":
				user.Balance, expected = -.25, ErrInsufficientBalance
				cards.err = nil
				cards.card = &UserSubscription{Status: SubscriptionStatusActive, ExpiresAt: time.Now().Add(-time.Hour), DailyLimitUSD: &limit, WeeklyLimitUSD: &limit, MonthlyLimitUSD: &limit}
			case "inactive_user":
				user.Status, expected = StatusDisabled, ErrUserNotActive
			case "user_read_error":
				users.err, expected = errors.New("authoritative store unavailable"), ErrBillingServiceUnavailable
			case "missing_user":
				users.user, expected = nil, ErrUserNotActive
			case "card_read_error_negative_wallet":
				user.Balance, expected = -.25, ErrInsufficientBalance
				cards.err = errors.New("card store unavailable")
			case "card_read_error_positive_wallet":
				cards.err = errors.New("card store unavailable")
			case "positive_wallet_without_card_repo":
				svc.subRepo = nil
			case "negative_wallet_without_card_repo":
				svc.subRepo = nil
				user.Balance, expected = -.25, ErrInsufficientBalance
			}
			err := svc.CheckWebSocketTurnFunding(context.Background(), user.ID)
			if expected != nil {
				require.ErrorIs(t, err, expected)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, users.reads)
			require.Zero(t, cache.reads, "a positive cached balance cannot authorize an established WS")
			require.Zero(t, rpm.increments, "rechecking funding must not count an existing turn twice")
			if name == "negative_wallet_new_card_ignores_no_card_cache" {
				require.Equal(t, 1, cards.reads)
			}
		})
	}
}

func TestWSTurnFunding_NoServiceAndSimpleMode(t *testing.T) {
	var missing *BillingCacheService
	require.ErrorIs(t, missing.CheckWebSocketTurnFunding(context.Background(), 1), ErrBillingServiceUnavailable)
	require.ErrorIs(t, (&BillingCacheService{cfg: &config.Config{RunMode: config.RunModeStandard}}).CheckWebSocketTurnFunding(context.Background(), 1), ErrBillingServiceUnavailable)
	require.NoError(t, (&BillingCacheService{cfg: &config.Config{RunMode: config.RunModeSimple}}).CheckWebSocketTurnFunding(context.Background(), 1))
}

type wsTurnIdentityRepo struct {
	APIKeyRepository
	key   *APIKey
	err   error
	reads int
}

func (r *wsTurnIdentityRepo) GetByKeyForAuth(context.Context, string) (*APIKey, error) {
	r.reads++
	return r.key, r.err
}

func TestWSTurnFunding_CurrentIdentityKeepsConnectionGroup(t *testing.T) {
	for _, name := range []string{"active", "group_moved", "deleted", "disabled", "user_disabled", "user_identity_changed", "key_identity_changed", "expired_status", "expired_time", "quota_status", "quota_used", "read_error"} {
		t.Run(name, func(t *testing.T) {
			groupID, otherGroup := int64(1), int64(2)
			initial := &APIKey{ID: 7, UserID: 8, Key: "ws-frozen-key", GroupID: &groupID, Group: &Group{ID: groupID}}
			current := &APIKey{ID: 7, UserID: 8, Key: initial.Key, Status: StatusActive, User: &User{ID: 8, Status: StatusActive}, GroupID: &groupID}
			repo := &wsTurnIdentityRepo{key: current}
			var expected error
			switch name {
			case "group_moved":
				current.GroupID = &otherGroup
			case "deleted":
				repo.err, expected = ErrAPIKeyNotFound, ErrAPIKeyNotFound
			case "disabled":
				current.Status, expected = StatusDisabled, ErrInsufficientPerms
			case "user_disabled":
				current.User.Status, expected = StatusDisabled, ErrUserNotActive
			case "user_identity_changed":
				current.User.ID, expected = 9, ErrInsufficientPerms
			case "key_identity_changed":
				current.ID, expected = 9, ErrInsufficientPerms
			case "expired_status":
				current.Status, expected = StatusAPIKeyExpired, ErrAPIKeyExpired
			case "expired_time":
				expired := time.Now().Add(-time.Hour)
				current.ExpiresAt, expected = &expired, ErrAPIKeyExpired
			case "quota_status":
				current.Status, expected = StatusAPIKeyQuotaExhausted, ErrAPIKeyQuotaExhausted
			case "quota_used":
				current.Quota, current.QuotaUsed, expected = 1, 1, ErrAPIKeyQuotaExhausted
			case "read_error":
				repo.err = errors.New("key store unavailable")
				expected = repo.err
			}
			err := (&APIKeyService{apiKeyRepo: repo}).RecheckWebSocketTurnIdentity(context.Background(), initial)
			if expected != nil {
				require.ErrorIs(t, err, expected)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, repo.reads)
			require.Equal(t, groupID, *initial.GroupID)
			require.Equal(t, groupID, initial.Group.ID)
		})
	}
}
