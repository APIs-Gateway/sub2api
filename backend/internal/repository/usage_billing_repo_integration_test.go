//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

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
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:  user.ID,
		GroupID: group.ID,
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:        requestID,
		APIKeyID:         apiKey.ID,
		UserID:           user.ID,
		AccountID:        0,
		SubscriptionID:   &subscription.ID,
		SubscriptionCost: 2.5,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 2.5, dailyUsage, 0.000001)
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

// TestUsageBillingRepositoryApply_PerDayTrackingAndSlippageNotCounted 校验 per-day 计量落库：
// 单笔超过当天 D（slippage）照常计费、daily_spent_usd 记当天累计、daily_spent_day 记当前日历天 N；
// 未开透支时该超调不计入 total_overdraft_count。
func TestUsageBillingRepositoryApply_PerDayTrackingAndSlippageNotCounted(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-perday-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      1000,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-perday-group-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-perday-" + uuid.NewString(),
		Name:    "perday",
	})
	now := time.Now()
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		GrantedTotalUSD: 1000,
		DailyAmountUSD:  100,
		ActivatedAt:     &now,
		ExpiresAt:       now.Add(10 * 24 * time.Hour),
		// MaxOverdraftDays = nil → 透支关闭
	})

	// 单笔 250 > 当天 D=100（slippage 超调）：照常计费,但未开透支 → total_overdraft_count 不增。
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 250,
	})
	require.NoError(t, err)

	var consumed, dailySpent float64
	var dailySpentDay, toc int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT consumed_usd, daily_spent_usd, daily_spent_day, total_overdraft_count FROM user_subscriptions WHERE id=$1`, sub.ID).
		Scan(&consumed, &dailySpent, &dailySpentDay, &toc))
	require.InDelta(t, 250, consumed, 1e-6, "consumed_usd")
	require.InDelta(t, 250, dailySpent, 1e-6, "daily_spent_usd 记当天累计用量")
	require.Equal(t, 0, dailySpentDay, "daily_spent_day = 当前日历天 N=0")
	require.Equal(t, 0, toc, "未开透支的 slippage 不计入 total_overdraft_count")
}

// TestUsageBillingRepositoryApply_OverdraftDaysAccumulateAndClose 校验透支「累计预支天数（求和）」与
// 达上限自动关闭：多笔消费使当天用量逐步突破,total_overdraft_count 按 delta 累加,达 5 天即把
// max_overdraft_days 置 NULL（关闭透支）。
func TestUsageBillingRepositoryApply_OverdraftDaysAccumulateAndClose(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-od-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      3000, // = 卡 G,充值(非订阅)余额=0 → 溢出 slippage 仍落本卡
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-od-group-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-od-" + uuid.NewString(),
		Name:    "od",
	})
	now := time.Now()
	five := 5
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:           user.ID,
		GroupID:          group.ID,
		GrantedTotalUSD:  3000,
		DailyAmountUSD:   100,
		MaxOverdraftDays: &five,
		ActivatedAt:      &now,
		ExpiresAt:        now.Add(30 * 24 * time.Hour),
	})

	read := func() (toc int, closed bool, dailySpent float64) {
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			`SELECT total_overdraft_count, max_overdraft_days IS NULL, daily_spent_usd FROM user_subscriptions WHERE id=$1`, sub.ID).
			Scan(&toc, &closed, &dailySpent))
		return
	}
	charge := func(amount float64) {
		_, err := repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: amount,
		})
		require.NoError(t, err)
	}

	// 第1笔 250：当天 250 → preSpend=⌈2.5⌉−1=2 → toc=2；未达上限,透支仍开。
	charge(250)
	toc, closed, ds := read()
	require.Equal(t, 2, toc, "首笔预支 2 天")
	require.False(t, closed, "未达上限,透支仍开启")
	require.InDelta(t, 250, ds, 1e-6)

	// 第2笔 200：当天累计 450 → preSpend=4；delta=2 → toc=4。
	charge(200)
	toc, closed, ds = read()
	require.Equal(t, 4, toc, "累计预支 4 天")
	require.False(t, closed)
	require.InDelta(t, 450, ds, 1e-6)

	// 第3笔 200：当天累计 650 → preSpend=⌈6.5⌉−1=6;delta=2 → toc=min(5,4+2)=5 → 关闭透支。
	charge(200)
	toc, closed, ds = read()
	require.Equal(t, 5, toc, "达到 5 天上限")
	require.True(t, closed, "达上限后透支自动关闭(max_overdraft_days→NULL)")
	require.InDelta(t, 650, ds, 1e-6)
}

// TestUsageBillingRepositoryApply_NextDayResetsDailySpent 校验跨日历天后 daily_spent 惰性重置：
// 昨天 daily_spent_day=0/daily_spent_usd=50,今天(N≥1)首笔扣费应把 daily_spent 重置为本次量、day 记当前 N。
func TestUsageBillingRepositoryApply_NextDayResetsDailySpent(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("ub-nextday-%d@example.com", time.Now().UnixNano()), PasswordHash: "hash", Balance: 1000,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "ub-nextday-" + uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, GroupID: &group.ID, Key: "sk-nextday-" + uuid.NewString(), Name: "nextday",
	})

	activated := time.Now().Add(-25 * time.Hour) // 至少跨 1 个东八区午夜 → 今天 N≥1
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		GrantedTotalUSD: 1000, DailyAmountUSD: 100,
		ConsumedUSD:   50,                   // 昨天消费 50
		DailySpentUSD: 50, DailySpentDay: 0, // 昨天(day0)的记录
		ActivatedAt: &activated, ExpiresAt: time.Now().Add(10 * 24 * time.Hour),
	})

	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 30,
	})
	require.NoError(t, err)

	var consumed, dailySpent float64
	var dailySpentDay int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT consumed_usd, daily_spent_usd, daily_spent_day FROM user_subscriptions WHERE id=$1`, sub.ID).
		Scan(&consumed, &dailySpent, &dailySpentDay))
	require.InDelta(t, 80, consumed, 1e-6, "consumed 累计 50+30")
	require.InDelta(t, 30, dailySpent, 1e-6, "新的一天 daily_spent 重置为本次 30,不累加昨天的 50")
	require.GreaterOrEqual(t, dailySpentDay, 1, "daily_spent_day 更新为当前日历天 N≥1")
}

// TestUsageBillingRepositoryApply_MultiCardRespectsPerCardDailyLimit 校验多卡与闸门同口径:
// 老卡今日已限速(可花 0)、新卡今日有额度时,扣费应落到新卡,而非继续突破老卡今日限速。
func TestUsageBillingRepositoryApply_MultiCardRespectsPerCardDailyLimit(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("ub-multicard-%d@example.com", time.Now().UnixNano()), PasswordHash: "hash", Balance: 5000,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "ub-multicard-" + uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, GroupID: &group.ID, Key: "sk-multicard-" + uuid.NewString(), Name: "multicard",
	})

	now := time.Now()
	// 老卡:先创建(id 更小) → 今日已用满 D=100、无透支 → 今日可花 0。
	oldCard := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		GrantedTotalUSD: 1000, DailyAmountUSD: 100,
		ConsumedUSD: 100, DailySpentUSD: 100, DailySpentDay: 0,
		ActivatedAt: &now, ExpiresAt: now.Add(10 * 24 * time.Hour),
	})
	// 新卡:今日未用 → 今日可花 100。
	newCard := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		GrantedTotalUSD: 1000, DailyAmountUSD: 100,
		ConsumedUSD: 0, DailySpentUSD: 0, DailySpentDay: 0,
		ActivatedAt: &now, ExpiresAt: now.Add(10 * 24 * time.Hour),
	})

	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 100,
	})
	require.NoError(t, err)

	var oldConsumed, newConsumed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT consumed_usd FROM user_subscriptions WHERE id=$1`, oldCard.ID).Scan(&oldConsumed))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT consumed_usd FROM user_subscriptions WHERE id=$1`, newCard.ID).Scan(&newConsumed))
	require.InDelta(t, 100, oldConsumed, 1e-6, "老卡今日已限速,不应被继续扣")
	require.InDelta(t, 100, newConsumed, 1e-6, "扣费应落到今日有额度的新卡")
}

// TestUsageBillingRepositoryApply_MultiCardExpirySoonestFirstNoPrematureOverdraft 校验「叠加」语义
// (回归 wzreee/671):月卡(到期远、可透支) + 日卡(明天到期)同时活跃时,扣费应①先烧快到期的日卡、
// ②各卡先用满当日正常额度、所有卡正常额度用尽才透支 —— 不应把月卡先烧进透支而日卡闲置作废。
func TestUsageBillingRepositoryApply_MultiCardExpirySoonestFirstNoPrematureOverdraft(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("ub-stack-%d@example.com", time.Now().UnixNano()), PasswordHash: "hash", Balance: 5000,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "ub-stack-" + uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, GroupID: &group.ID, Key: "sk-stack-" + uuid.NewString(), Name: "stack",
	})

	now := time.Now()
	five := 5
	// 月卡:激活较早、到期远(+25d)、可透支。今日未用 → 当日额度 90。
	monthlyActivated := now.Add(-2 * 24 * time.Hour)
	monthly := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		GrantedTotalUSD: 2700, DailyAmountUSD: 90,
		MaxOverdraftDays: &five,
		ActivatedAt:      &monthlyActivated, ExpiresAt: now.Add(25 * 24 * time.Hour),
	})
	// 日卡:今天激活、明天就到期(+1d)。当日额度 30、不可透支。
	daily := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		GrantedTotalUSD: 30, DailyAmountUSD: 30,
		ActivatedAt: &now, ExpiresAt: now.Add(1 * 24 * time.Hour),
	})

	// 花 100(< 两卡当日正常额度合计 120):应日卡 30(先烧到期早的)+ 月卡 70,均在正常额度内、月卡不透支。
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 100,
	})
	require.NoError(t, err)

	var dailyConsumed, monthlyConsumed float64
	var monthlyTOC int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT consumed_usd FROM user_subscriptions WHERE id=$1`, daily.ID).Scan(&dailyConsumed))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT consumed_usd, total_overdraft_count FROM user_subscriptions WHERE id=$1`, monthly.ID).Scan(&monthlyConsumed, &monthlyTOC))
	require.InDelta(t, 30, dailyConsumed, 1e-6, "应先烧到期最早的日卡到其当日限额 30")
	require.InDelta(t, 70, monthlyConsumed, 1e-6, "余下 70 落月卡当日正常额度内")
	require.Equal(t, 0, monthlyTOC, "两卡正常额度未用尽,月卡不应被提前烧进透支")
}

// TestUsageBillingRepositoryApply_DepletesAndExpires 校验把本卡剩余扣到 0 时即时置 expired,
// 超出 remaining 的部分由余额承担。
func TestUsageBillingRepositoryApply_DepletesAndExpires(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("ub-deplete-%d@example.com", time.Now().UnixNano()), PasswordHash: "hash", Balance: 200,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "ub-deplete-" + uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, GroupID: &group.ID, Key: "sk-deplete-" + uuid.NewString(), Name: "deplete",
	})

	now := time.Now()
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		GrantedTotalUSD: 200, DailyAmountUSD: 100,
		ActivatedAt: &now, ExpiresAt: now.Add(30 * 24 * time.Hour),
	})

	// 余额=卡剩余 200(充值/非订阅余额=0)。单笔 200 > 今日额度 100:Pass1 扣 100、溢出 100 因无充值
	// 余额而 slippage 落本卡 → 卡扣到 0 → expired。
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 200,
	})
	require.NoError(t, err)

	var consumed float64
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT consumed_usd, status FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&consumed, &status))
	require.InDelta(t, 200, consumed, 1e-6, "本卡扣到 remaining=200")
	require.Equal(t, service.SubscriptionStatusExpired, status, "扣到 0 即 expired")

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.InDelta(t, 0, balance, 1e-6, "余额扣减全额 200")
}

// TestUsageBillingRepositoryApply_RechargeBalanceFundsOverflowProtectsCard 校验「充值余额兜底」:
// 订阅卡今日额度已用尽、但用户有充值(非订阅)余额时,溢出由充值余额承担,订阅卡 consumed 不增、锁定额度不被烧。
func TestUsageBillingRepositoryApply_RechargeBalanceFundsOverflowProtectsCard(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("ub-recharge-%d@example.com", time.Now().UnixNano()), PasswordHash: "hash",
		Balance: 950, // = 卡 remaining 900 + 充值 50
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "ub-recharge-" + uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, GroupID: &group.ID, Key: "sk-recharge-" + uuid.NewString(), Name: "recharge",
	})
	now := time.Now()
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		GrantedTotalUSD: 1000, DailyAmountUSD: 100,
		ConsumedUSD: 100, DailySpentUSD: 100, DailySpentDay: 0, // 今日 D=100 已用满
		ActivatedAt: &now, ExpiresAt: now.Add(10 * 24 * time.Hour),
	})

	// 充值余额=50。请求 30:今日订阅额度已尽 → 由充值承担,卡不动。
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 30,
	})
	require.NoError(t, err)

	var consumed, dailySpent, balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT consumed_usd, daily_spent_usd FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&consumed, &dailySpent))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.InDelta(t, 100, consumed, 1e-6, "订阅卡 consumed 不增(由充值余额承担)")
	require.InDelta(t, 100, dailySpent, 1e-6, "daily_spent 不变")
	require.InDelta(t, 920, balance, 1e-6, "余额扣 30(从充值部分)")
}

// TestUsageBillingRepositoryApply_RechargeExhaustedThenSlippageToCard 校验充值余额不足时:
// 先吃光充值,剩余部分 slippage 落订阅卡(保 balance≥Σremaining)。
func TestUsageBillingRepositoryApply_RechargeExhaustedThenSlippageToCard(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("ub-recharge2-%d@example.com", time.Now().UnixNano()), PasswordHash: "hash",
		Balance: 910, // = 卡 remaining 900 + 充值 10
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "ub-recharge2-" + uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, GroupID: &group.ID, Key: "sk-recharge2-" + uuid.NewString(), Name: "recharge2",
	})
	now := time.Now()
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, GroupID: group.ID,
		GrantedTotalUSD: 1000, DailyAmountUSD: 100,
		ConsumedUSD: 100, DailySpentUSD: 100, DailySpentDay: 0,
		ActivatedAt: &now, ExpiresAt: now.Add(10 * 24 * time.Hour),
	})

	// 充值=10。请求 30:今日额度尽 → 充值吃 10、剩 20 slippage 落卡。
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 30,
	})
	require.NoError(t, err)

	var consumed, balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT consumed_usd FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&consumed))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.InDelta(t, 120, consumed, 1e-6, "充值 10 之外的 20 由卡承担 → consumed 100+20")
	require.InDelta(t, 880, balance, 1e-6, "余额扣 30")
}
