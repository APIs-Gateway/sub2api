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

// 回归守卫（Finding A，P0）：无 group 自定义卡（group_id NULL）必须能正常三窗口结算，
// 不得因 settleSubscriptionWindow 把 group_id 扫进 int64 而 "converting NULL to int64" 崩溃。
func TestUsageBillingRepositoryApply_NoGroupCardBilledThroughThreeWindowsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-nogroup-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      50,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-nogroup-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-nogroup-" + uuid.NewString(),
		Name:    "billing-nogroup",
	})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	daily, weekly, monthly := 10.0, 70.0, 300.0
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            0, // 无 group 自定义卡 → group_id NULL
		DailyAmountUSD:     10,
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
		TodayRemaining:     10,
		TodayDay:           startDay,
		StartDay:           startDay,
		ExpireDay:          startDay + 9,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay + 9),
		Status:             service.SubscriptionStatusActive,
	})
	var rawGid *int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT group_id FROM user_subscriptions WHERE id = $1`, card.ID).Scan(&rawGid))
	require.Nil(t, rawGid, "前置：无 group 卡 group_id 必须为 NULL")

	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		SubscriptionID: &card.ID,
		OfficialCost:   4,
		RateMultiplier: 1,
	})
	require.NoError(t, err, "无 group 卡结算不得因 group_id NULL 崩溃（Finding A 回归守卫）")
	require.True(t, res.Applied)
	require.NotNil(t, res.SubscriptionID)
	require.Equal(t, card.ID, *res.SubscriptionID, "有生效卡 → 标 subscription")
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 0, *res.WalletDebit, 1e-6, "订阅全覆盖，钱包不动")

	var d, w, m, bal float64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd FROM user_subscriptions WHERE id = $1`, card.ID).Scan(&d, &w, &m))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1`, user.ID).Scan(&bal))
	require.InDelta(t, 4, d, 1e-6)
	require.InDelta(t, 4, w, 1e-6)
	require.InDelta(t, 4, m, 1e-6)
	require.InDelta(t, 50, bal, 1e-6)
}

// 边界：周窗口（非日窗口）是 binding 约束 —— SubRemaining 取三窗口剩余最小值。
// daily 有大量余量、weekly 仅剩 1 → cover=1，其余落钱包 1:1（md §8 #7）。
func TestUsageBillingRepositoryApply_WeeklyWindowIsBindingConstraintPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-weekbind-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      50,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "usage-billing-weekbind-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-weekbind-" + uuid.NewString(), Name: "weekbind"})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	daily, weekly, monthly := 100.0, 5.0, 100.0
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     100,
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyUsageUSD:      0,
		WeeklyUsageUSD:     4, // 周剩余 = 1（binding）
		MonthlyUsageUSD:    4,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
		TodayRemaining:     100,
		TodayDay:           startDay,
		StartDay:           startDay,
		ExpireDay:          startDay + 9,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay + 9),
		Status:             service.SubscriptionStatusActive,
	})

	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		SubscriptionID: &card.ID,
		OfficialCost:   3,
		RateMultiplier: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 2, *res.WalletDebit, 1e-6, "周窗口 binding：SubRemaining=min(100,1,96)=1，覆盖 1，剩 2 落钱包 1:1")

	var d, w, m, bal float64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd FROM user_subscriptions WHERE id = $1`, card.ID).Scan(&d, &w, &m))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1`, user.ID).Scan(&bal))
	require.InDelta(t, 1, d, 1e-6, "三窗口同步累加 cover=1")
	require.InDelta(t, 5, w, 1e-6, "周用量到顶 4+1=5")
	require.InDelta(t, 5, m, 1e-6)
	require.InDelta(t, 48, bal, 1e-6, "50 − 2")
}

// 边界：单笔成本恰等于 SubRemaining → 全覆盖、钱包不动；紧接第二笔（窗口已满）→ 全落钱包。
func TestUsageBillingRepositoryApply_CostExactlyEqualsRemainingThenOverflowsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-exact-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      50,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "usage-billing-exact-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-exact-" + uuid.NewString(), Name: "exact"})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	daily, weekly, monthly := 5.0, 100.0, 100.0
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     5,
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
		TodayRemaining:     5,
		TodayDay:           startDay,
		StartDay:           startDay,
		ExpireDay:          startDay + 9,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay + 9),
		Status:             service.SubscriptionStatusActive,
	})

	// 第一笔：成本 5 = 日窗口剩余 5（binding）→ 全覆盖、钱包不动。
	res1, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID,
		SubscriptionID: &card.ID, OfficialCost: 5, RateMultiplier: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, res1.WalletDebit)
	require.InDelta(t, 0, *res1.WalletDebit, 1e-6, "成本恰等于 SubRemaining → 全覆盖、钱包不动")

	// 第二笔：日窗口已满（5/5）→ SubRemaining=0 → 全落钱包 1:1。
	res2, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID,
		SubscriptionID: &card.ID, OfficialCost: 1, RateMultiplier: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, res2.WalletDebit)
	require.InDelta(t, 1, *res2.WalletDebit, 1e-6, "日窗口已满 → 落钱包 1:1")

	var d, bal float64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1`, card.ID).Scan(&d))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1`, user.ID).Scan(&bal))
	require.InDelta(t, 5, d, 1e-6, "日用量封顶 5，不超限")
	require.InDelta(t, 49, bal, 1e-6, "50 − 1（仅第二笔走钱包）")
}
