//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 并发不同请求在同一张卡上累加三窗口 usage 必须无丢失更新：settleSubscriptionWindow 在事务内
// SELECT ... FOR UPDATE 锁卡行串行化结算，N 笔各异 request_id 的覆盖请求最终三窗口 usage = ΣC。
func TestUsageBillingRepositoryApply_ConcurrentDistinctRequestsAccrueThreeWindowsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-3w-distinct-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      50,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-3w-distinct-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-3w-distinct-" + uuid.NewString(),
		Name:    "billing-3w-distinct",
	})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	// 限额足够大：N 笔全部由订阅覆盖（不落钱包），便于断言三窗口累加值。
	daily, weekly, monthly := 100.0, 700.0, 3000.0
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     100,
		TodayRemaining:     100,
		TodayDay:           startDay,
		StartDay:           startDay,
		ExpireDay:          startDay + 9,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay + 9),
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})

	const n = 10
	const each = 2.0
	start := make(chan struct{})
	errs := make(chan error, n)
	applied := make(chan bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := repo.Apply(ctx, &service.UsageBillingCommand{
				RequestID:      fmt.Sprintf("distinct-%d-%s", i, uuid.NewString()),
				APIKeyID:       apiKey.ID,
				UserID:         user.ID,
				SubscriptionID: &subscription.ID,
				OfficialCost:   each,
				RateMultiplier: 1,
			})
			errs <- err
			applied <- res != nil && res.Applied
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(applied)

	for err := range errs {
		require.NoError(t, err)
	}
	appliedCount := 0
	for ok := range applied {
		if ok {
			appliedCount++
		}
	}
	require.Equal(t, n, appliedCount, "每笔不同 request_id 都应独立结算")

	var dailyUsage, weeklyUsage, monthlyUsage, balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd
		FROM user_subscriptions WHERE id = $1
	`, subscription.ID).Scan(&dailyUsage, &weeklyUsage, &monthlyUsage))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, n*each, dailyUsage, 0.000001, "并发累加不得丢失更新")
	require.InDelta(t, n*each, weeklyUsage, 0.000001)
	require.InDelta(t, n*each, monthlyUsage, 0.000001)
	require.InDelta(t, 50, balance, 0.000001, "订阅全覆盖，钱包不动")
}

// DB 层跨自然日边界惰性重置：daily_window_start 落在昨天的卡，结算前应先把 daily_usage 归零、
// 起点推进到今天，再按本笔成本累加（周/月窗口为当前周期、不重置）。
// 关键判别：若未重置，旧 daily_usage(8) + 5 会撞破 daily_limit(10)→ 部分落钱包；重置后全覆盖、钱包不动。
func TestUsageBillingRepositoryApply_StaleDailyWindowResetsBeforeAccruePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-3w-reset-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-3w-reset-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-3w-reset-" + uuid.NewString(),
		Name:    "billing-3w-reset",
	})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	yesterdayStart := timezone.StartOfDay(now.AddDate(0, 0, -1))
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	// 日限额小(10) 会 bind；周/月限额大(700/3000)、当前周期，确保只有日窗口需要重置。
	daily, weekly, monthly := 10.0, 700.0, 3000.0
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     10,
		TodayRemaining:     10,
		TodayDay:           startDay,
		StartDay:           startDay - 2,
		ExpireDay:          startDay + 9,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay + 9),
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyUsageUSD:      8, // 昨天的旧用量，应被重置
		WeeklyUsageUSD:     8,
		MonthlyUsageUSD:    8,
		DailyWindowStart:   &yesterdayStart, // 落在昨天 → 触发日窗口重置
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})

	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		SubscriptionID: &subscription.ID,
		OfficialCost:   5,
		RateMultiplier: 1,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.NotNil(t, result.WalletDebit)
	require.InDelta(t, 0, *result.WalletDebit, 0.000001, "日窗口重置后全覆盖，不应动钱包")

	var dailyUsage, weeklyUsage, monthlyUsage, balance float64
	var dailyWindowStart time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd, daily_window_start
		FROM user_subscriptions WHERE id = $1
	`, subscription.ID).Scan(&dailyUsage, &weeklyUsage, &monthlyUsage, &dailyWindowStart))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))

	require.InDelta(t, 5, dailyUsage, 0.000001, "日窗口归零后只累加本笔 5（未重置则会是 13 > 10）")
	require.InDelta(t, 13, weeklyUsage, 0.000001, "周窗口未重置，累加为 8+5")
	require.InDelta(t, 13, monthlyUsage, 0.000001, "月窗口未重置，累加为 8+5")
	require.InDelta(t, 100, balance, 0.000001, "全覆盖，钱包不动")
	require.True(t, dailyWindowStart.Equal(timezone.StartOfDay(now)), "日窗口起点应推进到今天")
}
