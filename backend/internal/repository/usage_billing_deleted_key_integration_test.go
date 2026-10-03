//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUsageBillingRepositoryApply_DeletedKeyStillSettlesPostgres(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		for _, counters := range []string{"quota", "rate-limit", "both"} {
			name := "wallet/" + counters
			if subscription {
				name = "subscription/" + counters
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				client := testEntClient(t)
				repo := NewUsageBillingRepository(client, integrationDB)
				user := mustCreateUser(t, client, &service.User{
					Email: "deleted-key-billing-" + uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 100,
				})
				apiKey := mustCreateApiKey(t, client, &service.APIKey{
					UserID: user.ID, Key: "sk-deleted-key-billing-" + uuid.NewString(), Name: "billing", Quota: 50, RateLimit5h: 50,
				})
				account := mustCreateAccount(t, client, &service.Account{
					Name: "deleted-key-billing-" + uuid.NewString(), Type: service.AccountTypeAPIKey,
					Extra: map[string]any{"quota_limit": 100.0},
				})
				var cardID int64
				if subscription {
					group := mustCreateGroup(t, client, &service.Group{
						Name: "deleted-key-billing-" + uuid.NewString(), Platform: service.PlatformAnthropic,
						SubscriptionType: service.SubscriptionTypeSubscription,
					})
					now := time.Now()
					dayStart, weekStart, monthStart := timezone.StartOfDay(now), timezone.StartOfWeek(now), timezone.StartOfMonth(now)
					daily, weekly, monthly := 3.0, 70.0, 300.0
					card := mustCreateSubscription(t, client, &service.UserSubscription{
						UserID: user.ID, GroupID: group.ID, ExpiresAt: now.Add(48 * time.Hour),
						DailyLimitUSD: &daily, WeeklyLimitUSD: &weekly, MonthlyLimitUSD: &monthly,
						DailyUsageUSD: 1, WeeklyUsageUSD: 1, MonthlyUsageUSD: 1,
						DailyWindowStart: &dayStart, WeeklyWindowStart: &weekStart, MonthlyWindowStart: &monthStart,
					})
					cardID = card.ID
				}
				// Use the fork's real deletion path, including its tombstone and audit behavior.
				require.NoError(t, NewAPIKeyRepository(client, integrationDB).DeleteWithAudit(ctx, apiKey.ID))
				cmd := &service.UsageBillingCommand{
					RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, AccountID: account.ID,
					AccountType: service.AccountTypeAPIKey, OfficialCost: 5, RateMultiplier: 2, AccountQuotaCost: 5,
				}
				if counters != "rate-limit" {
					cmd.APIKeyQuotaCost = 10
				}
				if counters != "quota" {
					cmd.APIKeyRateLimitCost = 10
				}
				result, err := repo.Apply(ctx, cmd)
				require.NoError(t, err)
				require.True(t, result.Applied)
				require.False(t, result.APIKeyQuotaExhausted)
				require.NotNil(t, result.QuotaState)
				require.InDelta(t, 5, result.QuotaState.TotalUsed, 0.000001)
				walletDebit := 10.0
				if subscription {
					walletDebit = 8 // 5 × 2 actual cost; subscription has 2 remaining.
					require.NotNil(t, result.SubscriptionID)
					require.Equal(t, cardID, *result.SubscriptionID)
				} else {
					require.Nil(t, result.SubscriptionID)
				}
				require.NotNil(t, result.WalletDebit)
				require.InDelta(t, walletDebit, *result.WalletDebit, 0.000001)
				require.NotNil(t, result.NewBalance)
				require.InDelta(t, 100-walletDebit, *result.NewBalance, 0.000001)
				replay, err := repo.Apply(ctx, cmd)
				require.NoError(t, err)
				require.False(t, replay.Applied)

				var balance, accountUsed float64
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
				require.InDelta(t, 100-walletDebit, balance, 0.000001)
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id = $1", account.ID).Scan(&accountUsed))
				require.InDelta(t, 5, accountUsed, 0.000001)
				var quotaUsed, usage5h, usage1d, usage7d float64
				var status string
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used, usage_5h, usage_1d, usage_7d, status FROM api_keys WHERE id = $1", apiKey.ID).
					Scan(&quotaUsed, &usage5h, &usage1d, &usage7d, &status))
				require.Zero(t, quotaUsed)
				require.Zero(t, usage5h)
				require.Zero(t, usage1d)
				require.Zero(t, usage7d)
				require.Equal(t, service.StatusAPIKeyActive, status)
				var dedupCount int
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", cmd.RequestID, apiKey.ID).Scan(&dedupCount))
				require.Equal(t, 1, dedupCount)
				if subscription {
					var dailyUsage, weeklyUsage, monthlyUsage float64
					require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd FROM user_subscriptions WHERE id = $1", cardID).
						Scan(&dailyUsage, &weeklyUsage, &monthlyUsage))
					require.InDelta(t, 3, dailyUsage, 0.000001)
					require.InDelta(t, 3, weeklyUsage, 0.000001)
					require.InDelta(t, 3, monthlyUsage, 0.000001)
				}
			})
		}
	}
}
