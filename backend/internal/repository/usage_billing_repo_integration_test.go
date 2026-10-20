//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUsageBillingRepositoryApply_DeduplicatesBalanceBilling(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-" + uuid.NewString(),
		Name:   "billing",
		Quota:  1,
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-billing-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:           requestID,
		APIKeyID:            apiKey.ID,
		UserID:              user.ID,
		AccountID:           account.ID,
		AccountType:         service.AccountTypeAPIKey,
		BalanceCost:         1.25,
		APIKeyQuotaCost:     1.25,
		APIKeyRateLimitCost: 1.25,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.NotNil(t, result1)
	require.True(t, result1.Applied)
	require.True(t, result1.APIKeyQuotaExhausted)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.NotNil(t, result2)
	require.False(t, result2.Applied)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)

	var quotaUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id = $1", apiKey.ID).Scan(&quotaUsed))
	require.InDelta(t, 1.25, quotaUsed, 0.000001)

	var usage5h float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT usage_5h FROM api_keys WHERE id = $1", apiKey.ID).Scan(&usage5h))
	require.InDelta(t, 1.25, usage5h, 0.000001)

	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status FROM api_keys WHERE id = $1", apiKey.ID).Scan(&status))
	require.Equal(t, service.StatusAPIKeyQuotaExhausted, status)

	var dedupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&dedupCount))
	require.Equal(t, 1, dedupCount)
}

func TestUsageBillingRepositoryApply_DeduplicatesSubscriptionBilling(t *testing.T) {
	// 该用例断言订阅结算写入三窗口列 daily_usage_usd，但 main 的 usage_billing.Apply 仍是
	// burn-down 结算（写 consumed_usd，需 granted_total_usd>0），三窗口结算由
	// feat/billing-perday-redesign 引入。在三窗口结算合并进 main 前跳过——feat 分支已重写本
	// 测试，合并后去掉此 Skip 即恢复覆盖。
	t.Skip("三窗口结算(daily_usage_usd)尚未合并进 main；Apply 仍走 burn-down consumed_usd")
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-sub-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-group-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-sub-" + uuid.NewString(),
		Name:    "billing-sub",
	})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	daily, weekly, monthly := 10.0, 70.0, 300.0
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     10,
		TodayRemaining:     10,
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

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:      requestID,
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		AccountID:      0,
		SubscriptionID: &subscription.ID,
		OfficialCost:   2.5,
		RateMultiplier: 1,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)
	require.NotNil(t, result1.WalletDebit)
	require.InDelta(t, 0, *result1.WalletDebit, 0.000001)
	require.NotNil(t, result1.SubscriptionID)
	require.Equal(t, subscription.ID, *result1.SubscriptionID)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var dailyUsage, weeklyUsage, monthlyUsage, todayRemaining float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd, today_remaining
		FROM user_subscriptions WHERE id = $1
	`, subscription.ID).Scan(&dailyUsage, &weeklyUsage, &monthlyUsage, &todayRemaining))
	require.InDelta(t, 2.5, dailyUsage, 0.000001)
	require.InDelta(t, 2.5, weeklyUsage, 0.000001)
	require.InDelta(t, 2.5, monthlyUsage, 0.000001)
	require.InDelta(t, 10, todayRemaining, 0.000001, "三窗口结算不应再动 per-day pool 字段")
}

func TestUsageBillingRepositoryApply_SubscriptionFallsThroughToWalletAndDeduplicatesPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-sub-fallthrough-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-sub-fallthrough-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-sub-fallthrough-" + uuid.NewString(),
		Name:    "billing-sub-fallthrough",
	})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	daily, weekly, monthly := 3.0, 70.0, 300.0
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     3,
		TodayRemaining:     3,
		TodayDay:           startDay,
		StartDay:           startDay,
		ExpireDay:          startDay + 9,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay + 9),
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyUsageUSD:      1,
		WeeklyUsageUSD:     1,
		MonthlyUsageUSD:    1,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:      requestID,
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		AccountID:      0,
		SubscriptionID: &subscription.ID,
		OfficialCost:   5,
		RateMultiplier: 2,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)
	require.NotNil(t, result1.SubscriptionID)
	require.Equal(t, subscription.ID, *result1.SubscriptionID)
	require.NotNil(t, result1.WalletDebit)
	require.InDelta(t, 8, *result1.WalletDebit, 0.000001, "实际计费 5×2=10，订阅剩余 2 覆盖后，剩余 8 扣钱包")

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var dailyUsage, weeklyUsage, monthlyUsage, balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd
		FROM user_subscriptions WHERE id = $1
	`, subscription.ID).Scan(&dailyUsage, &weeklyUsage, &monthlyUsage))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 3, dailyUsage, 0.000001)
	require.InDelta(t, 3, weeklyUsage, 0.000001)
	require.InDelta(t, 3, monthlyUsage, 0.000001)
	require.InDelta(t, 92, balance, 0.000001) // 100 − (5×2−2)
}

func TestUsageBillingRepositoryApply_UnconfiguredSubscriptionFallsThroughToWalletPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-unconfigured-sub-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      20,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-unconfigured-sub-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-unconfigured-sub-" + uuid.NewString(),
		Name:    "billing-unconfigured-sub",
	})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     10,
		TodayRemaining:     10,
		TodayDay:           startDay,
		StartDay:           startDay,
		ExpireDay:          startDay + 9,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay + 9),
		DailyUsageUSD:      1,
		WeeklyUsageUSD:     2,
		MonthlyUsageUSD:    3,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})

	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		SubscriptionID: &subscription.ID,
		OfficialCost:   4,
		RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.NotNil(t, result.SubscriptionID, "有生效卡的请求仍应按 subscription 口径记日志，即使未配置限额导致覆盖为 0")
	require.Equal(t, subscription.ID, *result.SubscriptionID)
	require.NotNil(t, result.WalletDebit)
	require.InDelta(t, 8, *result.WalletDebit, 0.000001, "三限额全 NULL 的脏卡必须完全回落钱包（× 倍率 2），不能免费覆盖")

	var dailyUsage, weeklyUsage, monthlyUsage, balance float64
	var dailyLimit, weeklyLimit, monthlyLimit *float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd,
		       daily_limit_usd, weekly_limit_usd, monthly_limit_usd
		FROM user_subscriptions WHERE id = $1
	`, subscription.ID).Scan(&dailyUsage, &weeklyUsage, &monthlyUsage, &dailyLimit, &weeklyLimit, &monthlyLimit))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 1, dailyUsage, 0.000001)
	require.InDelta(t, 2, weeklyUsage, 0.000001)
	require.InDelta(t, 3, monthlyUsage, 0.000001)
	require.Nil(t, dailyLimit)
	require.Nil(t, weeklyLimit)
	require.Nil(t, monthlyLimit)
	require.InDelta(t, 12, balance, 0.000001) // 20 − 4×2
}

func TestUsageBillingRepositoryApply_ExpiredActiveCardIsClosedAndWalletBilledPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-expired-active-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      20,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-expired-active-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-expired-active-" + uuid.NewString(),
		Name:    "billing-expired-active",
	})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	daily, weekly, monthly := 10.0, 70.0, 300.0
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     10,
		TodayRemaining:     10,
		TodayDay:           startDay - 2,
		StartDay:           startDay - 10,
		ExpireDay:          startDay - 1,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay - 1),
		Status:             service.SubscriptionStatusActive,
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyUsageUSD:      1,
		WeeklyUsageUSD:     2,
		MonthlyUsageUSD:    3,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})

	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		SubscriptionID: &subscription.ID,
		OfficialCost:   4,
		RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Nil(t, result.SubscriptionID, "假 active 过期卡不能再标 subscription 计费")
	require.NotNil(t, result.WalletDebit)
	require.InDelta(t, 8, *result.WalletDebit, 0.000001) // 过期卡不覆盖，4 官方刀 × 倍率 2 扣钱包

	var status string
	var dailyUsage, weeklyUsage, monthlyUsage, balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT status, daily_usage_usd, weekly_usage_usd, monthly_usage_usd
		FROM user_subscriptions WHERE id = $1
	`, subscription.ID).Scan(&status, &dailyUsage, &weeklyUsage, &monthlyUsage))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.Equal(t, service.SubscriptionStatusExpired, status)
	require.InDelta(t, 1, dailyUsage, 0.000001)
	require.InDelta(t, 2, weeklyUsage, 0.000001)
	require.InDelta(t, 3, monthlyUsage, 0.000001)
	require.InDelta(t, 12, balance, 0.000001) // 20 − 4×2
}

func TestUsageBillingRepositoryApply_ConcurrentSameRequestOnlyAppliesOncePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-sub-concurrent-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-sub-concurrent-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-sub-concurrent-" + uuid.NewString(),
		Name:    "billing-sub-concurrent",
	})
	now := time.Now()
	startDay := service.EastDayNumber(now)
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	daily, weekly, monthly := 3.0, 70.0, 300.0
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            group.ID,
		DailyAmountUSD:     3,
		TodayRemaining:     3,
		TodayDay:           startDay,
		StartDay:           startDay,
		ExpireDay:          startDay + 9,
		ExpiresAt:          service.ExpireDayToExpiresAt(startDay + 9),
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyUsageUSD:      1,
		WeeklyUsageUSD:     1,
		MonthlyUsageUSD:    1,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:      requestID,
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		SubscriptionID: &subscription.ID,
		OfficialCost:   5,
		RateMultiplier: 2,
	}

	const n = 8
	start := make(chan struct{})
	results := make(chan bool, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// 每 goroutine 用各自的 cmd 副本：生产每请求都新建 cmd（buildUsageBillingCommand），
			// Apply→Normalize 会就地写 RequestID/RequestFingerprint，共享同一指针会 data race。
			// 字段相同 → Normalize 算出同一指纹 → dedup（同 request_id+api_key_id）语义不变。
			c := *cmd
			res, err := repo.Apply(ctx, &c)
			errs <- err
			results <- res != nil && res.Applied
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(results)

	applied := 0
	for err := range errs {
		require.NoError(t, err)
	}
	for ok := range results {
		if ok {
			applied++
		}
	}
	require.Equal(t, 1, applied, "same request_id/api_key_id must apply side effects once under concurrency")

	var dailyUsage, weeklyUsage, monthlyUsage, balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd
		FROM user_subscriptions WHERE id = $1
	`, subscription.ID).Scan(&dailyUsage, &weeklyUsage, &monthlyUsage))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 3, dailyUsage, 0.000001)
	require.InDelta(t, 3, weeklyUsage, 0.000001)
	require.InDelta(t, 3, monthlyUsage, 0.000001)
	require.InDelta(t, 92, balance, 0.000001) // 100 − (5×2−2)

	var dedupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&dedupCount))
	require.Equal(t, 1, dedupCount)
}

func TestUsageBillingRepositoryApply_RequestFingerprintConflict(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-conflict-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-conflict-" + uuid.NewString(),
		Name:   "billing-conflict",
	})

	requestID := uuid.NewString()
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 1.25,
	})
	require.NoError(t, err)

	_, err = repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 2.50,
	})
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
}

func TestUsageBillingRepositoryApply_UpdatesAccountQuota(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-account-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-account-" + uuid.NewString(),
		Name:   "billing-account",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-billing-account-quota-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
		Extra: map[string]any{
			"quota_limit": 100.0,
		},
	})

	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:        uuid.NewString(),
		APIKeyID:         apiKey.ID,
		UserID:           user.ID,
		AccountID:        account.ID,
		AccountType:      service.AccountTypeAPIKey,
		AccountQuotaCost: 3.5,
	})
	require.NoError(t, err)

	var quotaUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE((extra->>'quota_used')::numeric, 0) FROM accounts WHERE id = $1", account.ID).Scan(&quotaUsed))
	require.InDelta(t, 3.5, quotaUsed, 0.000001)
}

func TestUsageBillingRepositoryApply_EnqueuesSchedulerOutboxOnQuotaCrossing(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	newFixture := func(t *testing.T, extra map[string]any) (int64, int64) {
		t.Helper()
		user := mustCreateUser(t, client, &service.User{
			Email:        fmt.Sprintf("usage-billing-outbox-user-%d-%s@example.com", time.Now().UnixNano(), uuid.NewString()),
			PasswordHash: "hash",
		})
		apiKey := mustCreateApiKey(t, client, &service.APIKey{
			UserID: user.ID,
			Key:    "sk-usage-billing-outbox-" + uuid.NewString(),
			Name:   "billing-outbox",
		})
		account := mustCreateAccount(t, client, &service.Account{
			Name:  "usage-billing-outbox-" + uuid.NewString(),
			Type:  service.AccountTypeAPIKey,
			Extra: extra,
		})
		return apiKey.ID, account.ID
	}

	outboxCountFor := func(t *testing.T, accountID int64) int {
		t.Helper()
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
			service.SchedulerOutboxEventAccountChanged, accountID,
		).Scan(&count))
		return count
	}

	t.Run("daily_first_crossing_enqueues", func(t *testing.T) {
		apiKeyID, accountID := newFixture(t, map[string]any{
			"quota_daily_limit": 10.0,
		})
		// 第一次低于日限额：不应入队 outbox
		_, err := repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 4,
		})
		require.NoError(t, err)
		require.Equal(t, 0, outboxCountFor(t, accountID), "below limit should not enqueue")

		// 第二次跨越日限额：应入队一次 outbox
		_, err = repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 8,
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "crossing daily limit should enqueue once")

		// 再次递增（已超）：不应重复入队
		_, err = repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 2,
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "subsequent increments beyond limit should not re-enqueue")
	})

	t.Run("weekly_first_crossing_enqueues", func(t *testing.T) {
		apiKeyID, accountID := newFixture(t, map[string]any{
			"quota_weekly_limit": 10.0,
		})
		_, err := repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 15, // 单次即跨越
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "single-shot crossing weekly limit should enqueue once")
	})
}

func TestDashboardAggregationRepositoryCleanupUsageBillingDedup_BatchDeletesOldRows(t *testing.T) {
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(integrationDB)

	oldRequestID := "dedup-old-" + uuid.NewString()
	newRequestID := "dedup-new-" + uuid.NewString()
	oldCreatedAt := time.Now().UTC().AddDate(0, 0, -400)
	newCreatedAt := time.Now().UTC().Add(-time.Hour)

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint, created_at)
		VALUES ($1, 1, $2, $3), ($4, 1, $5, $6)
	`,
		oldRequestID, strings.Repeat("a", 64), oldCreatedAt,
		newRequestID, strings.Repeat("b", 64), newCreatedAt,
	)
	require.NoError(t, err)

	require.NoError(t, repo.CleanupUsageBillingDedup(ctx, time.Now().UTC().AddDate(0, 0, -365)))

	var oldCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1", oldRequestID).Scan(&oldCount))
	require.Equal(t, 0, oldCount)

	var newCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1", newRequestID).Scan(&newCount))
	require.Equal(t, 1, newCount)

	var archivedCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup_archive WHERE request_id = $1", oldRequestID).Scan(&archivedCount))
	require.Equal(t, 1, archivedCount)
}

func TestUsageBillingRepositoryApply_DeduplicatesAgainstArchivedKey(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	aggRepo := newDashboardAggregationRepositoryWithSQL(integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-archive-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-archive-" + uuid.NewString(),
		Name:   "billing-archive",
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 1.25,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)

	_, err = integrationDB.ExecContext(ctx, `
		UPDATE usage_billing_dedup
		SET created_at = $1
		WHERE request_id = $2 AND api_key_id = $3
	`, time.Now().UTC().AddDate(0, 0, -400), requestID, apiKey.ID)
	require.NoError(t, err)
	require.NoError(t, aggRepo.CleanupUsageBillingDedup(ctx, time.Now().UTC().AddDate(0, 0, -365)))

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)
}

// ===== per-day 结算（settlePerDaySubscription）=====

func perDayBillingFixture(t *testing.T, client *dbent.Client, balance float64) (*service.User, *service.APIKey) {
	t.Helper()
	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-perday-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      balance,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-perday-group-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-perday-" + uuid.NewString(),
		Name:    "perday",
	})
	return user, apiKey
}

// 套餐余额 → 钱包正余额：先把官方价按倍率折成实际金额，套餐先扣满 D，余额进钱包，套餐永不为负。
func TestUsageBillingRepositoryApply_PerDayPackageThenWallet(t *testing.T) {
	t.Skip("legacy per-day pool contract retired by three-window billing; covered by TestUsageBillingRepositoryApply_SubscriptionFallsThroughToWalletAndDeduplicatesPostgres")
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 200)
	now := time.Now()
	startDay := service.EastDayNumber(now)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 100,
		TodayRemaining: 100,
		TodayDay:       startDay,
		StartDay:       startDay,
		ExpireDay:      startDay + 9,
		ExpiresAt:      now.Add(10 * 24 * time.Hour),
	})

	// 官方成本 150、倍率 2：实际金额 300；套餐扣 100→0；剩 200 走钱包，balance 200→0。
	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID,
		OfficialCost: 150, RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.NotNil(t, res.NewBalance)
	require.InDelta(t, 0, *res.NewBalance, 1e-6, "balance 200 − (150×2−100)")
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 200, *res.WalletDebit, 1e-6, "钱包实扣 = 150×2−100")
	// 有生效卡 → 返回卡 ID（上层据此把 usage_log 标 subscription + 写 subscription_id）。
	require.NotNil(t, res.SubscriptionID, "有卡应返回 SubscriptionID")
	require.Equal(t, sub.ID, *res.SubscriptionID)

	var todayRem float64
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT today_remaining, status FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&todayRem, &status))
	require.InDelta(t, 0, todayRem, 1e-6, "套餐余额扣到 0，永不为负")
	require.Equal(t, "active", status, "未过期不应被标记 expired")
}

// 透支：套餐+钱包皆空、开透支 → 借未来天（expire_day−1 + 用户级月度计数+1）按 1:1 扣。
func TestUsageBillingRepositoryApply_PerDayOverdraftBorrowsDay(t *testing.T) {
	t.Skip("legacy automatic per-day overdraft retired; manual overdraft is covered by subscription_overdraft_http_integration_test.go")
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 0)
	now := time.Now()
	startDay := service.EastDayNumber(now)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 100,
		TodayRemaining: 0, // 当天已用尽
		TodayDay:       startDay,
		StartDay:       startDay,
		ExpireDay:      startDay + 9,
		OverdraftOn:    true,
		ExpiresAt:      now.Add(10 * 24 * time.Hour),
	})

	// 官方成本 100、倍率 1：套餐 0、钱包 0 → 透支借 1 天（expire_day 9→8，月度计数 0→1）按 1:1 扣 100。
	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID,
		OfficialCost: 100, RateMultiplier: 1,
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 0, *res.WalletDebit, 1e-6, "透支 1:1、不动钱包")

	var expireDay int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT expire_day FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&expireDay))
	require.Equal(t, startDay+8, expireDay, "透支借 1 天：expire_day−1")

	var monthCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT monthly_overdraft_count FROM users WHERE id=$1`, user.ID).Scan(&monthCount))
	require.Equal(t, 1, monthCount, "用户级月度透支计数 +1")
}

func TestUsageBillingRepositoryApply_PerDayTracksPackageSpentAcrossOverdraft(t *testing.T) {
	t.Skip("legacy automatic per-day overdraft retired; three-window settlement never borrows days")
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 0)
	today := service.TodayEastDayNumber()
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 10,
		TodayRemaining: 0,
		TodayDay:       today,
		DailySpentUSD:  10,
		DailySpentDay:  today,
		StartDay:       today,
		ExpireDay:      today + 5,
		OverdraftOn:    true,
		ExpiresAt:      service.ExpireDayToExpiresAt(today + 5),
	})

	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		OfficialCost:   12,
		RateMultiplier: 1,
	})
	require.NoError(t, err)
	require.True(t, res.OverdraftApplied)
	require.NotNil(t, res.SubscriptionID)

	var todayRemaining float64
	var dailySpent float64
	var dailySpentDay int
	var expireDay int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT today_remaining, daily_spent_usd, daily_spent_day, expire_day
		FROM user_subscriptions
		WHERE id = $1
	`, sub.ID).Scan(&todayRemaining, &dailySpent, &dailySpentDay, &expireDay))
	require.InDelta(t, 8, todayRemaining, 1e-6, "第二个借天未用完的 8 应留在今天")
	require.InDelta(t, 22, dailySpent, 1e-6, "今日套餐侧实际已扣 = 原 10 + 本次透支 12")
	require.Equal(t, today, dailySpentDay)
	require.Equal(t, today+3, expireDay, "12 官方刀需要借 2 个未来天")
}

// 无生效卡：纯钱包标准计费（官方价×倍率），钱包可扣负。
func TestUsageBillingRepositoryApply_PerDayNoCardWalletOnly(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 10)
	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID,
		OfficialCost: 8, RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.NotNil(t, res.NewBalance)
	require.InDelta(t, -6, *res.NewBalance, 1e-6, "无卡：10 − 8×2 = −6")
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 16, *res.WalletDebit, 1e-6) // 8 × 倍率 2
	require.Nil(t, res.SubscriptionID, "无卡 → 不标 subscription")
}

// 过期但 status='active' 的卡（到期任务未扫到）：本次费用其实走钱包、卡被惰性标 expired，
// **不应**标 subscription（SubscriptionID=nil），否则账本/日志分裂。
func TestUsageBillingRepositoryApply_PerDayStaleActiveCardNotSubscription(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 100) // 钱包足够
	now := time.Now()
	startDay := service.EastDayNumber(now)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 10,
		TodayRemaining: 10,            // 残留值，会被惰性覆盖为 0
		TodayDay:       startDay - 2,  // 旧值，触发惰性重置
		StartDay:       startDay - 11, // 11 天前激活
		ExpireDay:      startDay - 1,  // 已过期（today > expire_day）
		Status:         service.SubscriptionStatusActive,
		ExpiresAt:      now.Add(-1 * time.Hour), // 时间戳也已过，但到期任务未扫到 → status 仍 active
	})

	// 官方成本 4、倍率 2：卡已过期 → 套餐 0 → 全走钱包 4×2=8。
	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID,
		OfficialCost: 4, RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 8, *res.WalletDebit, 1e-6, "费用走钱包（× 倍率 2）")
	require.Nil(t, res.SubscriptionID, "过期假 active 卡不属于有效订阅卡 → 不标 subscription")

	// 卡被惰性标 expired。
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT status FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&status))
	require.Equal(t, "expired", status, "过期卡应被惰性标 expired")
}

// 有未过期有效卡时，即使当天套餐余额已为 0、透支关闭、费用全由钱包层支付，
// 也仍属于「有卡 → 套餐瀑布」请求，应返回 SubscriptionID 供 usage_log 标 subscription。
func TestUsageBillingRepositoryApply_PerDayActiveCardWalletOnlyStillSubscription(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 100)
	now := time.Now()
	startDay := service.EastDayNumber(now)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 10,
		TodayRemaining: 0,
		TodayDay:       startDay,
		StartDay:       startDay,
		ExpireDay:      startDay + 5,
		OverdraftOn:    false,
		ExpiresAt:      service.ExpireDayToExpiresAt(startDay + 5),
	})

	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		OfficialCost:   4,
		RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.NotNil(t, res.SubscriptionID, "有有效卡即订阅计费识别，即使本次全走钱包层")
	require.Equal(t, sub.ID, *res.SubscriptionID)
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 8, *res.WalletDebit, 1e-6) // 4 × 倍率 2（未配置三窗口限额 → 套餐不覆盖）

	var balance, todayRem float64
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT today_remaining, status FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&todayRem, &status))
	require.InDelta(t, 92, balance, 1e-6) // 100 − 4×2
	require.InDelta(t, 0, todayRem, 1e-6)
	require.Equal(t, "active", status)
}

// 同一 request_id 重放必须跳过整笔 settle 副作用：不能重复扣钱包、不能重复借未来天、不能重复累加月度透支。
func TestUsageBillingRepositoryApply_PerDayDedupSkipsOverdraftAndWalletSideEffects(t *testing.T) {
	t.Skip("legacy automatic per-day overdraft retired; usage billing dedup is covered by three-window concurrent/dedup tests")
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 10)
	now := time.Now()
	startDay := service.EastDayNumber(now)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 100,
		TodayRemaining: 0,
		TodayDay:       startDay,
		StartDay:       startDay,
		ExpireDay:      startDay + 1,
		OverdraftOn:    true,
		ExpiresAt:      service.ExpireDayToExpiresAt(startDay + 1),
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:      requestID,
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		OfficialCost:   160,
		RateMultiplier: 2,
	}
	res1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, res1.Applied)
	require.True(t, res1.OverdraftApplied)

	res2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, res2.Applied)

	var balance float64
	var monthCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT balance, monthly_overdraft_count FROM users WHERE id=$1`, user.ID).Scan(&balance, &monthCount))
	require.InDelta(t, -110, balance, 1e-6, "首笔：钱包正余额10覆盖5官方，透支100，剩55官方×2落钱包负数；重放不能再扣")
	require.Equal(t, 1, monthCount, "重放不能重复月度透支计数")

	var expireDay int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT expire_day FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&expireDay))
	require.Equal(t, startDay, expireDay, "重放不能重复 expire_day−1")
}

// 并发流式结算同时打到同一用户时，FOR UPDATE 锁必须保证月度透支最多 5 次，后续缺口落钱包负数。
func TestUsageBillingRepositoryApply_PerDayConcurrentOverdraftCapsMonthlyLimit(t *testing.T) {
	t.Skip("legacy automatic per-day overdraft retired; manual overdraft concurrency is covered by subscription_overdraft_http_integration_test.go")
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 0)
	now := time.Now()
	startDay := service.EastDayNumber(now)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 1,
		TodayRemaining: 0,
		TodayDay:       startDay,
		StartDay:       startDay,
		ExpireDay:      startDay + 20,
		OverdraftOn:    true,
		ExpiresAt:      service.ExpireDayToExpiresAt(startDay + 20),
	})

	const requests = 10
	start := make(chan struct{})
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := repo.Apply(ctx, &service.UsageBillingCommand{
				RequestID:      fmt.Sprintf("perday-concurrent-overdraft-%s-%02d", uuid.NewString(), i),
				APIKeyID:       apiKey.ID,
				UserID:         user.ID,
				OfficialCost:   1,
				RateMultiplier: 1,
			})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var balance float64
	var monthCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT balance, monthly_overdraft_count FROM users WHERE id=$1`, user.ID).Scan(&balance, &monthCount))
	require.Equal(t, service.MaxMonthlyOverdraftUses, monthCount, "并发不能突破每月 5 次透支")
	require.InDelta(t, -5, balance, 1e-6, "10 笔中 5 笔透支，剩余 5 笔缺口落钱包负数")

	var expireDay int
	var todayRemaining float64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT expire_day, today_remaining FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&expireDay, &todayRemaining))
	require.Equal(t, startDay+15, expireDay, "只允许借走 5 个未来天")
	require.InDelta(t, 0, todayRemaining, 1e-6, "每笔成本等于 D，借来的当天额度应刚好用完")
}

// 跨月时旧月度透支计数必须惰性重置，否则上月用满 5 次会在新月误把可透支请求打到钱包负数。
func TestUsageBillingRepositoryApply_PerDayOverdraftResetsStaleMonthCount(t *testing.T) {
	t.Skip("legacy automatic per-day overdraft retired; manual overdraft month reset lives in subscription_overdraft tests")
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 0)
	_, err := integrationDB.ExecContext(ctx,
		`UPDATE users SET monthly_overdraft_count = $1, monthly_overdraft_month = $2 WHERE id = $3`,
		service.MaxMonthlyOverdraftUses, "200001", user.ID)
	require.NoError(t, err)

	now := time.Now()
	startDay := service.EastDayNumber(now)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 10,
		TodayRemaining: 0,
		TodayDay:       startDay,
		StartDay:       startDay,
		ExpireDay:      startDay + 3,
		OverdraftOn:    true,
		ExpiresAt:      service.ExpireDayToExpiresAt(startDay + 3),
	})

	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		OfficialCost:   10,
		RateMultiplier: 1,
	})
	require.NoError(t, err)
	require.True(t, res.OverdraftApplied)

	var balance float64
	var monthCount int
	var month string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT balance, monthly_overdraft_count, monthly_overdraft_month FROM users WHERE id=$1`, user.ID).Scan(&balance, &monthCount, &month))
	require.InDelta(t, 0, balance, 1e-6)
	require.Equal(t, 1, monthCount, "新月应先清零再记录本次透支")
	require.Equal(t, service.EastMonthKey(now), month)

	var expireDay int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT expire_day FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&expireDay))
	require.Equal(t, startDay+2, expireDay)
}

// 跨东八区自然日且卡仍有效时，真实 DB 回写必须把 today_remaining 惰性覆盖为 D 后再扣。
func TestUsageBillingRepositoryApply_PerDayResetsRemainingOnNewValidDay(t *testing.T) {
	t.Skip("legacy per-day pool contract retired; three-window reset is covered by window engine and usage billing three-window tests")
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user, apiKey := perDayBillingFixture(t, client, 0)
	now := time.Now()
	today := service.EastDayNumber(now)
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        *apiKey.GroupID,
		DailyAmountUSD: 10,
		TodayRemaining: 3,
		TodayDay:       today - 1,
		StartDay:       today - 1,
		ExpireDay:      today + 2,
		ExpiresAt:      service.ExpireDayToExpiresAt(today + 2),
	})

	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		OfficialCost:   4,
		RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.NotNil(t, res.SubscriptionID)
	require.NotNil(t, res.NewBalance)

	var todayRem float64
	var todayDay int
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT today_remaining, today_day FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&todayRem, &todayDay))
	require.InDelta(t, 6, todayRem, 1e-6, "先跨天覆盖为 D=10，再扣 4")
	require.Equal(t, today, todayDay)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.InDelta(t, 0, balance, 1e-6, "套餐足够时钱包不应被倍率扣费")
}
