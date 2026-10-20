package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type dailyResetTrackingUserSubRepo struct {
	userSubRepoNoop

	resetDailyCalled bool
}

func (r *dailyResetTrackingUserSubRepo) ResetDailyUsage(context.Context, int64, *time.Time, time.Time) error {
	r.resetDailyCalled = true
	return nil
}

// per-day 模型：历史 expired 卡不复用；新购买会创建一张新的 active 卡。
func TestAssignOrExtendSubscription_CreatesNewCardAfterExpiredHistoricalCard(t *testing.T) {
	groupRepo := &subscriptionGroupRepoStub{
		group: &Group{ID: 1, SubscriptionType: SubscriptionTypeSubscription, DailyLimitUSD: ptrFloat64(10)},
	}
	subRepo := newSubscriptionUserSubRepoStub()
	oldStart := time.Now().AddDate(0, 0, -3)
	subRepo.seed(&UserSubscription{
		ID:        100,
		UserID:    200,
		GroupID:   1,
		StartsAt:  oldStart,
		ExpiresAt: oldStart.AddDate(0, 0, 1),
		Status:    SubscriptionStatusExpired,
		Notes:     "old",
	})
	svc := NewSubscriptionService(groupRepo, subRepo, nil, nil, nil, nil, nil, nil)

	created, reused, err := svc.AssignOrExtendSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:       200,
		GroupID:      1,
		ValidityDays: 1,
		Notes:        "new",
	})

	require.NoError(t, err)
	require.False(t, reused, "过期历史卡不复用")
	require.NotNil(t, created)
	require.NotEqual(t, int64(100), created.ID, "应新建一张卡而非复用过期订阅")
	require.Equal(t, SubscriptionStatusActive, created.Status)
	require.True(t, created.StartsAt.After(oldStart), "新卡 StartsAt 应为当前时间")
	require.True(t, created.HasOneTimeDailyQuota(), "1 日卡仍应被识别为一次性日额度")
	require.Equal(t, "new", created.Notes, "新卡只带本次 notes，不再追加旧 notes")
}

func TestUserSubscriptionNeedsDailyReset_DailyCardKeepsOneTimeQuota(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        start.Add(24 * time.Hour),
		DailyWindowStart: &dailyWindowStart,
		DailyUsageUSD:    10,
	}

	require.True(t, sub.HasOneTimeDailyQuota())
	require.False(t, sub.NeedsDailyResetAt(dailyWindowStart.Add(25*time.Hour)), "日卡应作为一次性配额，跨 0 点后不再刷新日额度")
}

func TestUserSubscriptionNeedsDailyReset_MultiDaySubscriptionStillRefreshes(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        start.AddDate(0, 0, 2),
		DailyWindowStart: &dailyWindowStart,
	}

	require.False(t, sub.HasOneTimeDailyQuota())
	require.True(t, sub.NeedsDailyResetAt(dailyWindowStart.Add(24*time.Hour)), "多日订阅仍应按 24 小时日窗口刷新")
}

func TestUserSubscriptionDailyResetTime_DailyCardReturnsExpiry(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	expiresAt := start.Add(24 * time.Hour)
	sub := &UserSubscription{
		StartsAt:         start,
		ExpiresAt:        expiresAt,
		DailyWindowStart: &dailyWindowStart,
	}

	resetAt := sub.DailyResetTime()
	require.NotNil(t, resetAt)
	require.Equal(t, expiresAt, *resetAt, "日卡展示的日额度结束时间应为订阅过期时间")
}

func TestCheckAndResetWindows_DailyCardDoesNotResetDailyUsage(t *testing.T) {
	now := time.Now()
	startsAt := now.Add(-23 * time.Hour)
	dailyWindowStart := now.Add(-25 * time.Hour)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	sub := &UserSubscription{
		ID:               1,
		UserID:           10,
		GroupID:          20,
		StartsAt:         startsAt,
		ExpiresAt:        startsAt.Add(24 * time.Hour),
		DailyUsageUSD:    10,
		DailyWindowStart: &dailyWindowStart,
	}

	err := svc.CheckAndResetWindows(context.Background(), sub)

	require.NoError(t, err)
	require.False(t, repo.resetDailyCalled, "日卡作为一次性配额，过了 24 小时日窗口也不应重置 daily usage")
	require.Equal(t, 10.0, sub.DailyUsageUSD)
}

func TestCheckAndResetWindows_MultiDaySubscriptionStillResetsDailyUsage(t *testing.T) {
	now := time.Now()
	startsAt := now.Add(-48 * time.Hour)
	dailyWindowStart := now.Add(-25 * time.Hour)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	sub := &UserSubscription{
		ID:               1,
		UserID:           10,
		GroupID:          20,
		StartsAt:         startsAt,
		ExpiresAt:        startsAt.AddDate(0, 0, 2),
		DailyUsageUSD:    10,
		DailyWindowStart: &dailyWindowStart,
	}

	err := svc.CheckAndResetWindows(context.Background(), sub)

	require.NoError(t, err)
	require.True(t, repo.resetDailyCalled, "多日订阅仍应重置过期 daily window")
	require.Equal(t, 0.0, sub.DailyUsageUSD)
}

func TestValidateAndCheckLimits_DailyCardDoesNotAllowSecondQuotaAfterMidnight(t *testing.T) {
	start := time.Now().Add(-23 * time.Hour)
	dailyWindowStart := time.Now().Add(-25 * time.Hour)
	dailyLimit := 10.0
	sub := &UserSubscription{
		Status:           SubscriptionStatusActive,
		StartsAt:         start,
		ExpiresAt:        start.Add(24 * time.Hour),
		DailyWindowStart: &dailyWindowStart,
		DailyUsageUSD:    dailyLimit + 0.01,
		DailyLimitUSD:    &dailyLimit,
	}
	group := &Group{
		SubscriptionType: SubscriptionTypeSubscription,
		DailyLimitUSD:    &dailyLimit,
	}
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil, nil, nil, nil)

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub, group)

	require.False(t, needsMaintenance, "日卡跨过日窗口后不应触发 daily reset 维护")
	require.True(t, errors.Is(err, ErrDailyLimitExceeded))
	require.Equal(t, dailyLimit+0.01, sub.DailyUsageUSD, "热路径不应清零日卡已用额度")
}

func TestValidateAndCheckLimits_UsesFrozenCardLimitInsteadOfGroup(t *testing.T) {
	windowStart := time.Now()
	cardLimit := 10.0
	tighterGroupLimit := 1.0
	looserGroupLimit := 100.0
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil, nil, nil, nil)

	t.Run("group tightening does not shrink existing card", func(t *testing.T) {
		sub := &UserSubscription{
			Status:           SubscriptionStatusActive,
			StartsAt:         time.Now().Add(-time.Hour),
			ExpiresAt:        time.Now().Add(48 * time.Hour),
			DailyWindowStart: &windowStart,
			DailyUsageUSD:    5,
			DailyLimitUSD:    &cardLimit,
		}

		_, err := svc.ValidateAndCheckLimits(sub, &Group{DailyLimitUSD: &tighterGroupLimit})

		require.NoError(t, err)
	})

	t.Run("group loosening does not expand existing card", func(t *testing.T) {
		sub := &UserSubscription{
			Status:           SubscriptionStatusActive,
			StartsAt:         time.Now().Add(-time.Hour),
			ExpiresAt:        time.Now().Add(48 * time.Hour),
			DailyWindowStart: &windowStart,
			DailyUsageUSD:    cardLimit + 0.01,
			DailyLimitUSD:    &cardLimit,
		}

		_, err := svc.ValidateAndCheckLimits(sub, &Group{DailyLimitUSD: &looserGroupLimit})

		require.ErrorIs(t, err, ErrDailyLimitExceeded)
	})
}

func TestValidateAndCheckLimits_ExactFrozenLimitIsAllowed(t *testing.T) {
	windowStart := time.Now()
	limit := 10.0
	sub := &UserSubscription{
		Status:           SubscriptionStatusActive,
		StartsAt:         time.Now().Add(-time.Hour),
		ExpiresAt:        time.Now().Add(48 * time.Hour),
		DailyWindowStart: &windowStart,
		DailyUsageUSD:    limit,
		DailyLimitUSD:    &limit,
	}
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil, nil, nil, nil)

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub, &Group{})

	require.NoError(t, err)
	require.False(t, needsMaintenance)
}

func TestValidateAndCheckLimits_LegacyNilSnapshotFallsBackToGroupLimit(t *testing.T) {
	windowStart := time.Now()
	groupLimit := 10.0
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil, nil, nil, nil)

	for _, tc := range []struct {
		name    string
		usage   float64
		wantErr error
	}{
		{name: "exact group limit remains allowed", usage: groupLimit},
		{name: "usage above group limit is rejected", usage: groupLimit + 0.01, wantErr: ErrDailyLimitExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := &UserSubscription{
				Status:           SubscriptionStatusActive,
				StartsAt:         time.Now().Add(-time.Hour),
				ExpiresAt:        time.Now().Add(48 * time.Hour),
				DailyWindowStart: &windowStart,
				DailyUsageUSD:    tc.usage,
				DailyLimitUSD:    nil,
			}

			_, err := svc.ValidateAndCheckLimits(sub, &Group{DailyLimitUSD: &groupLimit})

			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

func TestValidateAndCheckLimits_CoversFrozenAndLegacyWeeklyMonthlyLimits(t *testing.T) {
	windowStart := time.Now()
	limit := 10.0
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil, nil, nil, nil)

	for _, tc := range []struct {
		name    string
		sub     UserSubscription
		group   Group
		wantErr error
	}{
		{
			name: "frozen weekly limit wins over group",
			sub: UserSubscription{
				WeeklyWindowStart: &windowStart,
				WeeklyUsageUSD:    limit + 0.01,
				WeeklyLimitUSD:    &limit,
			},
			group:   Group{WeeklyLimitUSD: ptrFloat64(100)},
			wantErr: ErrWeeklyLimitExceeded,
		},
		{
			name: "legacy weekly limit falls back to group",
			sub: UserSubscription{
				WeeklyWindowStart: &windowStart,
				WeeklyUsageUSD:    limit + 0.01,
			},
			group:   Group{WeeklyLimitUSD: &limit},
			wantErr: ErrWeeklyLimitExceeded,
		},
		{
			name: "frozen monthly limit wins over group",
			sub: UserSubscription{
				MonthlyWindowStart: &windowStart,
				MonthlyUsageUSD:    limit + 0.01,
				MonthlyLimitUSD:    &limit,
			},
			group:   Group{MonthlyLimitUSD: ptrFloat64(100)},
			wantErr: ErrMonthlyLimitExceeded,
		},
		{
			name: "legacy monthly limit falls back to group",
			sub: UserSubscription{
				MonthlyWindowStart: &windowStart,
				MonthlyUsageUSD:    limit + 0.01,
			},
			group:   Group{MonthlyLimitUSD: &limit},
			wantErr: ErrMonthlyLimitExceeded,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := tc.sub
			sub.Status = SubscriptionStatusActive
			sub.StartsAt = time.Now().Add(-time.Hour)
			sub.ExpiresAt = time.Now().Add(48 * time.Hour)

			_, err := svc.ValidateAndCheckLimits(&sub, &tc.group)

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
