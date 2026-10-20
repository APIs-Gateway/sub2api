//go:build unit

package handler

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// /v1/usage 三窗口展示用的纯格式化助手:limit<=0 视为「未配置」→ null;reset 时间按窗口推算。

func TestUsageLimitValue(t *testing.T) {
	require.Nil(t, usageLimitValue(0))
	require.Nil(t, usageLimitValue(-1))
	require.Equal(t, 10.0, usageLimitValue(10))
}

func TestUsageRemainingValue(t *testing.T) {
	require.Nil(t, usageRemainingValue(0, 5)) // 未配置限额 → null
	require.Equal(t, 7.0, usageRemainingValue(10, 3))
	require.Equal(t, 0.0, usageRemainingValue(10, 15)) // 超用 → 夹到 0,不为负
}

func TestUsageSpendableRemainingIncludesWalletFallback(t *testing.T) {
	require.Equal(t, 55.04, usageSpendableRemaining(0, 55.04))
	require.Equal(t, 75.0, usageSpendableRemaining(20, 55))
	require.Equal(t, 20.0, usageSpendableRemaining(20, -5))
	require.Equal(t, 0.0, usageSpendableRemaining(-1, -5))
}

func TestUsageResetAt(t *testing.T) {
	require.Nil(t, usageResetAt(nil, "daily"))

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, start.Add(24*time.Hour), usageResetAt(&start, "daily"))
	require.Equal(t, start.Add(7*24*time.Hour), usageResetAt(&start, "weekly"))
	require.Equal(t, start.AddDate(0, 1, 0), usageResetAt(&start, "monthly"))
	require.Nil(t, usageResetAt(&start, "bogus"))
}

func TestBuildWhamUsageResponseSingleActivePerDayCard(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	today := service.EastDayNumber(now)

	resp := buildWhamUsageResponse(42.5, []service.UserSubscription{
		whamUsageTestSub(today, 30, 12),
	}, now)

	require.NotNil(t, resp.RateLimit.PrimaryWindow)
	require.Equal(t, 60.0, resp.RateLimit.PrimaryWindow.UsedPercent)
	require.Equal(t, int64(86400), resp.RateLimit.PrimaryWindow.LimitWindowSeconds)
	require.Equal(t, service.EastDayStart(today+1).Unix(), resp.RateLimit.PrimaryWindow.ResetAt)
	require.Equal(t, 30.0, resp.Sub2API.DailyLimitUSD)
	require.Equal(t, 18.0, resp.Sub2API.DailyUsedUSD)
	require.Equal(t, 12.0, resp.Sub2API.DailyRemainingUSD)
	require.Equal(t, 42.5, resp.Sub2API.WalletBalanceUSD)
	require.Equal(t, 1, resp.Sub2API.ActiveSubscriptionCount)
	require.Equal(t, service.EastDayStart(today+1).Unix(), resp.Sub2API.ResetAt)
}

func TestBuildWhamUsageResponseLazilyResetsOldTodayDay(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	today := service.EastDayNumber(now)
	sub := whamUsageTestSub(today-1, 30, 3)
	sub.ExpireDay = today + 2

	resp := buildWhamUsageResponse(0, []service.UserSubscription{sub}, now)

	require.NotNil(t, resp.RateLimit.PrimaryWindow)
	require.Equal(t, 0.0, resp.RateLimit.PrimaryWindow.UsedPercent)
	require.Equal(t, 30.0, resp.Sub2API.DailyLimitUSD)
	require.Equal(t, 0.0, resp.Sub2API.DailyUsedUSD)
	require.Equal(t, 30.0, resp.Sub2API.DailyRemainingUSD)
	require.Equal(t, 1, resp.Sub2API.ActiveSubscriptionCount)
}

func TestBuildWhamUsageResponseSumsMultipleActivePerDayCards(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	today := service.EastDayNumber(now)

	resp := buildWhamUsageResponse(0, []service.UserSubscription{
		whamUsageTestSub(today, 10, 4),
		whamUsageTestSub(today, 20, 16),
	}, now)

	require.NotNil(t, resp.RateLimit.PrimaryWindow)
	require.Equal(t, 33.33, resp.RateLimit.PrimaryWindow.UsedPercent)
	require.Equal(t, 30.0, resp.Sub2API.DailyLimitUSD)
	require.Equal(t, 10.0, resp.Sub2API.DailyUsedUSD)
	require.Equal(t, 20.0, resp.Sub2API.DailyRemainingUSD)
	require.Equal(t, 2, resp.Sub2API.ActiveSubscriptionCount)
}

func TestBuildWhamUsageResponseWithoutActivePerDayCardsKeepsWalletBalance(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	today := service.EastDayNumber(now)

	resp := buildWhamUsageResponse(7.25, []service.UserSubscription{
		{Status: service.SubscriptionStatusActive, DailyAmountUSD: 0, TodayDay: today, ExpireDay: today + 1},
		{Status: service.SubscriptionStatusExpired, DailyAmountUSD: 30, TodayRemaining: 12, TodayDay: today, ExpireDay: today + 1},
	}, now)

	require.Nil(t, resp.RateLimit.PrimaryWindow)
	require.Equal(t, 0.0, resp.Sub2API.DailyLimitUSD)
	require.Equal(t, 0.0, resp.Sub2API.DailyUsedUSD)
	require.Equal(t, 0.0, resp.Sub2API.DailyRemainingUSD)
	require.Equal(t, 7.25, resp.Sub2API.WalletBalanceUSD)
	require.Equal(t, 0, resp.Sub2API.ActiveSubscriptionCount)
	require.Equal(t, service.EastDayStart(today+1).Unix(), resp.Sub2API.ResetAt)
}

func TestBuildWhamUsageResponseClampsRemainingIntoCardLimit(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	today := service.EastDayNumber(now)

	resp := buildWhamUsageResponse(0, []service.UserSubscription{
		whamUsageTestSub(today, 10, -5),
		whamUsageTestSub(today, 10, 15),
	}, now)

	require.NotNil(t, resp.RateLimit.PrimaryWindow)
	require.Equal(t, 50.0, resp.RateLimit.PrimaryWindow.UsedPercent)
	require.Equal(t, 20.0, resp.Sub2API.DailyLimitUSD)
	require.Equal(t, 10.0, resp.Sub2API.DailyUsedUSD)
	require.Equal(t, 10.0, resp.Sub2API.DailyRemainingUSD)
	require.Equal(t, 2, resp.Sub2API.ActiveSubscriptionCount)
}

func whamUsageTestSub(today int, dailyAmount, remaining float64) service.UserSubscription {
	return service.UserSubscription{
		Status:          service.SubscriptionStatusActive,
		DailyAmountUSD:  dailyAmount,
		TodayRemaining:  remaining,
		TodayDay:        today,
		StartDay:        today - 1,
		ExpireDay:       today + 3,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 3),
		DailySpentDay:   today,
		DailySpentUSD:   dailyAmount - remaining,
		GrantedTotalUSD: dailyAmount * 5,
	}
}
