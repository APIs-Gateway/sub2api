package dto

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserSubscriptionFromService_SeparatesCalendarDayFromConsumptionDay(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	now := time.Now().In(loc)
	activatedAt := now.AddDate(0, 0, -2)

	sub := &service.UserSubscription{
		ID:              10,
		UserID:          20,
		GroupID:         30,
		StartsAt:        activatedAt,
		ActivatedAt:     &activatedAt,
		ExpiresAt:       activatedAt.AddDate(0, 0, 30),
		Status:          service.SubscriptionStatusActive,
		GrantedTotalUSD: 300,
		DailyAmountUSD:  10,
		ConsumedUSD:     50,
	}

	got := UserSubscriptionFromService(sub)
	require.NotNil(t, got)
	require.InDelta(t, 5, got.ConsumptionDay, 1e-9)
	require.Equal(t, 2, got.CalendarDay)
}

func TestUserSubscriptionFromService_LazilyResetsStaleActiveWindowsForDisplay(t *testing.T) {
	t.Parallel()

	now := time.Now()
	yesterdayStart := timezone.StartOfDay(now.AddDate(0, 0, -1))
	lastWeekStart := timezone.StartOfWeek(now.AddDate(0, 0, -8))
	lastMonthStart := timezone.StartOfMonth(now.AddDate(0, -1, 0))

	sub := &service.UserSubscription{
		ID:                 11,
		UserID:             21,
		StartsAt:           now.AddDate(0, 0, -10),
		ExpiresAt:          now.AddDate(0, 0, 10),
		Status:             service.SubscriptionStatusActive,
		DailyWindowStart:   &yesterdayStart,
		WeeklyWindowStart:  &lastWeekStart,
		MonthlyWindowStart: &lastMonthStart,
		DailyUsageUSD:      90,
		WeeklyUsageUSD:     180,
		MonthlyUsageUSD:    270,
		DailyLimitUSD:      floatPtr(90),
		WeeklyLimitUSD:     floatPtr(900),
		MonthlyLimitUSD:    floatPtr(3000),
	}

	got := UserSubscriptionFromService(sub)
	require.NotNil(t, got)
	require.Zero(t, got.DailyUsageUSD)
	require.Zero(t, got.WeeklyUsageUSD)
	require.Zero(t, got.MonthlyUsageUSD)
	require.NotNil(t, got.DailyWindowStart)
	require.NotNil(t, got.WeeklyWindowStart)
	require.NotNil(t, got.MonthlyWindowStart)
	require.Equal(t, timezone.StartOfDay(now), *got.DailyWindowStart)
	require.Equal(t, timezone.StartOfWeek(now), *got.WeeklyWindowStart)
	require.Equal(t, timezone.StartOfMonth(now), *got.MonthlyWindowStart)

	require.Equal(t, 90.0, sub.DailyUsageUSD, "DTO display reset must not mutate service model")
	require.Equal(t, yesterdayStart, *sub.DailyWindowStart)
}

func TestUserSubscriptionFromService_PreservesUnboundedWindowsForDisplay(t *testing.T) {
	t.Parallel()

	now := time.Now()
	sub := &service.UserSubscription{
		ID:              12,
		UserID:          22,
		StartsAt:        now.AddDate(0, 0, -10),
		ExpiresAt:       now.AddDate(0, 0, 10),
		Status:          service.SubscriptionStatusActive,
		DailyUsageUSD:   1.23,
		WeeklyUsageUSD:  2.34,
		MonthlyUsageUSD: 3.45,
	}

	got := UserSubscriptionFromService(sub)
	require.NotNil(t, got)
	require.Equal(t, 1.23, got.DailyUsageUSD)
	require.Equal(t, 2.34, got.WeeklyUsageUSD)
	require.Equal(t, 3.45, got.MonthlyUsageUSD)
	require.Nil(t, got.DailyWindowStart)
	require.Nil(t, got.WeeklyWindowStart)
	require.Nil(t, got.MonthlyWindowStart)
}

func floatPtr(v float64) *float64 {
	return &v
}
