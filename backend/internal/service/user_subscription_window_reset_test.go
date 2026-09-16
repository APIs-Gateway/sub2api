package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// P2#12:订阅展示侧周/月「是否重置 / 下次重置时间」改用自然边界(东八区周一/1 号),
// 与计费侧 SubWindow.ResetWindows 同口径——旧的 7×24h / 30×24h 滚动窗会与自然边界错位 0–6 天,
// 使 /v1/usage 展示的已用/重置时间与实际计费不一致。
func TestSubscriptionWindowReset_NaturalBoundaries(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) // 任意时刻
	ws := timezone.StartOfWeek(now)
	ms := timezone.StartOfMonth(now)

	// 周:窗口起点早于本自然周起点 → 该重置;恰为本周起点 → 不重置。
	beforeWeek := ws.Add(-time.Second)
	require.True(t, (&UserSubscription{WeeklyWindowStart: &beforeWeek}).NeedsWeeklyResetAt(now))
	require.False(t, (&UserSubscription{WeeklyWindowStart: &ws}).NeedsWeeklyResetAt(now))

	// 月:窗口起点早于本自然月起点 → 该重置;恰为本月起点 → 不重置。
	beforeMonth := ms.Add(-time.Second)
	require.True(t, (&UserSubscription{MonthlyWindowStart: &beforeMonth}).NeedsMonthlyResetAt(now))
	require.False(t, (&UserSubscription{MonthlyWindowStart: &ms}).NeedsMonthlyResetAt(now))

	// 下次重置 = 下一个自然边界。
	require.Equal(t, ws.AddDate(0, 0, 7), *(&UserSubscription{WeeklyWindowStart: &ws}).WeeklyResetTimeAt(now))
	require.Equal(t, ms.AddDate(0, 1, 0), *(&UserSubscription{MonthlyWindowStart: &ms}).MonthlyResetTimeAt(now))

	// 与旧「滚动窗」分叉的关键场景:窗口起点距今仅 2 天但已跨过自然周一 → 自然口径判「该重置」,
	// 旧的 7×24h 滚动会判「未到」。构造 now=周二、窗口起点=上周日(本周一之前)。
	tuesday := timezone.StartOfWeek(now).AddDate(0, 0, 1) // 本周二 0 点
	lastSunday := timezone.StartOfWeek(now).Add(-24 * time.Hour)
	require.True(t, (&UserSubscription{WeeklyWindowStart: &lastSunday}).NeedsWeeklyResetAt(tuesday),
		"窗口起点在上周(跨过本周一)→ 自然周口径应判重置,即便距今不足 7 天")

	// nil 窗口不触发重置、ResetTime 为 nil。
	require.False(t, (&UserSubscription{}).NeedsWeeklyResetAt(now))
	require.False(t, (&UserSubscription{}).NeedsMonthlyResetAt(now))
	require.Nil(t, (&UserSubscription{}).WeeklyResetTimeAt(now))
	require.Nil(t, (&UserSubscription{}).MonthlyResetTimeAt(now))
}

// weeklyMonthlyResetTrackingUserSubRepo records the window-start values passed to
// ResetWeeklyUsage/ResetMonthlyUsage so tests can assert on what CheckAndResetWindows
// actually persists, without touching a real repository.
type weeklyMonthlyResetTrackingUserSubRepo struct {
	userSubRepoNoop

	resetWeeklyCalled  bool
	weeklyWindowStart  time.Time
	resetMonthlyCalled bool
	monthlyWindowStart time.Time
}

func (r *weeklyMonthlyResetTrackingUserSubRepo) ResetWeeklyUsage(_ context.Context, _ int64, _ *time.Time, newWindowStart time.Time) error {
	r.resetWeeklyCalled = true
	r.weeklyWindowStart = newWindowStart
	return nil
}

func (r *weeklyMonthlyResetTrackingUserSubRepo) ResetMonthlyUsage(_ context.Context, _ int64, _ *time.Time, newWindowStart time.Time) error {
	r.resetMonthlyCalled = true
	r.monthlyWindowStart = newWindowStart
	return nil
}

// Issue #737 audit: upstream d29acc29a580 fixed a rolling-anchor bug where an automatic
// window reset could compute a new window start at or after the subscription's ExpiresAt,
// handing the user a "new" quota window they could never actually use. The two tests below
// pin down why the fork's natural-calendar-boundary model does not have an equivalent bug:
// ValidateAndCheckLimits always checks IsExpired() strictly before it evaluates
// Needs{Weekly,Monthly}Reset, and the only production callers of CheckAndResetWindows
// (EnsureWindowMaintenance, invoked from api_key_auth.go / api_key_auth_google.go) only run
// once that gate has already passed for the current request.

// TestValidateAndCheckLimits_ExpiredSubscriptionNeverTriggersAutomaticWindowReset asserts
// that an already-expired subscription is rejected before any window is touched, even when
// its weekly/monthly windows are stale enough that Needs{Weekly,Monthly}ResetAt would
// otherwise say a reset is due.
func TestValidateAndCheckLimits_ExpiredSubscriptionNeverTriggersAutomaticWindowReset(t *testing.T) {
	now := time.Now()
	staleWeekStart := timezone.StartOfWeek(now).AddDate(0, 0, -14)
	staleMonthStart := timezone.StartOfMonth(now).AddDate(0, -2, 0)
	sub := &UserSubscription{
		Status:             SubscriptionStatusActive,
		ExpiresAt:          now.Add(-time.Minute), // already past expiry
		WeeklyWindowStart:  &staleWeekStart,
		WeeklyUsageUSD:     42,
		MonthlyWindowStart: &staleMonthStart,
		MonthlyUsageUSD:    99,
	}
	// userSubRepoNoop panics on any call: ValidateAndCheckLimits must be a pure in-memory
	// check that never reaches the repository for an already-expired subscription.
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil, nil, nil, nil)

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub, &Group{})

	require.ErrorIs(t, err, ErrSubscriptionExpired)
	require.False(t, needsMaintenance, "expired subscriptions must never request window maintenance")
	require.Equal(t, 42.0, sub.WeeklyUsageUSD, "expired subscription's weekly usage must not be zeroed by a stale-window reset")
	require.Equal(t, 99.0, sub.MonthlyUsageUSD, "expired subscription's monthly usage must not be zeroed by a stale-window reset")
}

// TestCheckAndResetWindows_AutomaticResetNearExpiryNeverStartsPastExpiresAt documents the
// complementary positive case: a still-active subscription whose ExpiresAt falls shortly
// after a stale natural-calendar boundary is allowed to reset, and the new window start
// (startOfDay(now)) is always <= now < ExpiresAt at this call site.
func TestCheckAndResetWindows_AutomaticResetNearExpiryNeverStartsPastExpiresAt(t *testing.T) {
	now := time.Now()
	expiresAt := now.Add(2 * time.Hour) // still active, expires later today
	staleWeekStart := timezone.StartOfWeek(now).AddDate(0, 0, -7)
	staleMonthStart := timezone.StartOfMonth(now).AddDate(0, -1, 0)
	repo := &weeklyMonthlyResetTrackingUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	sub := &UserSubscription{
		ID:                 1,
		UserID:             10,
		GroupID:            20,
		ExpiresAt:          expiresAt,
		WeeklyWindowStart:  &staleWeekStart,
		WeeklyUsageUSD:     42,
		MonthlyWindowStart: &staleMonthStart,
		MonthlyUsageUSD:    99,
	}

	err := svc.CheckAndResetWindows(context.Background(), sub)

	require.NoError(t, err)
	require.True(t, repo.resetWeeklyCalled)
	require.True(t, repo.resetMonthlyCalled)
	require.False(t, repo.weeklyWindowStart.After(expiresAt), "automatic weekly reset must not start a window after ExpiresAt")
	require.False(t, repo.monthlyWindowStart.After(expiresAt), "automatic monthly reset must not start a window after ExpiresAt")
	require.Zero(t, sub.WeeklyUsageUSD)
	require.Zero(t, sub.MonthlyUsageUSD)
}
