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

	dbent "github.com/Wei-Shaw/sub2api/ent"
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
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 10,
		TodayDay:       startDay,
		StartDay:       startDay,
		ExpireDay:      startDay + 9,
		ExpiresAt:      service.ExpireDayToExpiresAt(startDay + 9),
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

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var todayRemaining float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT today_remaining FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&todayRemaining))
	require.InDelta(t, 7.5, todayRemaining, 0.000001)
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

// 套餐余额(1:1) → 钱包正余额(×倍率)：套餐先扣满 D，余额按官方×倍率扣，套餐永不为负。
func TestUsageBillingRepositoryApply_PerDayPackageThenWallet(t *testing.T) {
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

	// 官方成本 150、倍率 2：套餐扣 100→0；剩 50 官方走钱包 ×2=100，balance 200→100。
	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID,
		OfficialCost: 150, RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.NotNil(t, res.NewBalance)
	require.InDelta(t, 100, *res.NewBalance, 1e-6, "balance 200 − 50×2")
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 100, *res.WalletDebit, 1e-6, "钱包实扣 = 50×2")
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
	require.InDelta(t, -6, *res.NewBalance, 1e-6, "无卡：10 − 8×2 = −6（钱包可负）")
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 16, *res.WalletDebit, 1e-6)
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

	// 官方成本 4、倍率 2：卡已过期 → 套餐 0、不可透支 → 全走钱包 4×2=8。
	res, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID,
		OfficialCost: 4, RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.NotNil(t, res.WalletDebit)
	require.InDelta(t, 8, *res.WalletDebit, 1e-6, "费用走钱包")
	require.Nil(t, res.SubscriptionID, "过期 active 卡本次未扣卡侧额度 → 不标 subscription")

	// 卡被惰性标 expired。
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT status FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&status))
	require.Equal(t, "expired", status, "过期卡应被惰性标 expired")
}
