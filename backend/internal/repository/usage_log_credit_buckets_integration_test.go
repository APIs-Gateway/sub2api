//go:build integration

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// creditBucketKey 把分桶结果按 (维度, billing_type, subscription_id) 索引，方便断言。
type creditBucketKey struct {
	key   string
	bt    int8
	subID int64
}

func indexCreditBuckets(buckets []usagestats.CreditCostBucket) map[creditBucketKey]usagestats.CreditCostBucket {
	out := make(map[creditBucketKey]usagestats.CreditCostBucket, len(buckets))
	for _, b := range buckets {
		out[creditBucketKey{b.Key, b.BillingType, b.SubscriptionID}] = b
	}
	return out
}

func TestUsageLog_GetCreditCostBuckets(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user := mustCreateUser(t, client, &service.User{Email: "credit-buckets@test.com"})
	other := mustCreateUser(t, client, &service.User{Email: "credit-buckets-other@test.com"})
	key1 := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-credit-bucket-1", Name: "k1"})
	key2 := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-credit-bucket-2", Name: "k2"})
	otherKey := mustCreateApiKey(t, client, &service.APIKey{UserID: other.ID, Key: "sk-credit-bucket-3", Name: "k3"})
	account := mustCreateAccount(t, client, &service.Account{Name: "acc-credit-buckets", Platform: service.PlatformOpenAI})
	card := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, DailyAmountUSD: 30})

	now := time.Now().UTC().Truncate(time.Second)
	splitAt := now.Add(-time.Hour)
	earlier := now.Add(-3 * time.Hour)

	insert := func(u, k int64, model string, bt int8, subID *int64, cost float64, at time.Time) {
		t.Helper()
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: u, APIKeyID: k, AccountID: account.ID,
			Model: model, InputTokens: 1, OutputTokens: 1,
			TotalCost: cost, ActualCost: cost,
			BillingType: bt, SubscriptionID: subID,
			CreatedAt: at,
		})
		require.NoError(t, err)
	}
	insert(user.ID, key1.ID, "gpt-a", service.BillingTypeBalance, nil, 1.0, earlier)
	insert(user.ID, key1.ID, "gpt-b", service.BillingTypeSubscription, &card.ID, 2.0, now)
	insert(user.ID, key2.ID, "gpt-a", service.BillingTypeSubscription, &card.ID, 3.0, now)
	// 失败占位行（actual_cost = 0）：平台维度要和 by_platform 一样排除它。
	insert(user.ID, key2.ID, "gpt-a", service.BillingTypeBalance, nil, 0, now)
	// 别的用户的记录无论如何都不能被统计进来。
	insert(other.ID, otherKey.ID, "gpt-a", service.BillingTypeBalance, nil, 100, now)

	start := now.Add(-24 * time.Hour)
	end := now.Add(time.Hour)

	t.Run("no dimension splits by billing source", func(t *testing.T) {
		buckets, err := repo.GetCreditCostBuckets(ctx, usagestats.CreditCostBucketFilter{
			UserID: user.ID, StartTime: start, EndTime: end, SplitAt: splitAt,
		})
		require.NoError(t, err)
		idx := indexCreditBuckets(buckets)
		require.Len(t, idx, 2)
		wallet := idx[creditBucketKey{"", service.BillingTypeBalance, 0}]
		require.InDelta(t, 1.0, wallet.ActualCost, 1e-9)
		require.InDelta(t, 0.0, wallet.ActualCostSince, 1e-9, "钱包那笔在 SplitAt 之前")
		sub := idx[creditBucketKey{"", service.BillingTypeSubscription, card.ID}]
		require.InDelta(t, 5.0, sub.ActualCost, 1e-9)
		require.InDelta(t, 5.0, sub.ActualCostSince, 1e-9)
	})

	t.Run("time window bounds rows", func(t *testing.T) {
		buckets, err := repo.GetCreditCostBuckets(ctx, usagestats.CreditCostBucketFilter{
			UserID: user.ID, StartTime: splitAt, EndTime: end,
		})
		require.NoError(t, err)
		idx := indexCreditBuckets(buckets)
		_, hasWalletCost := idx[creditBucketKey{"", service.BillingTypeBalance, 0}]
		require.True(t, hasWalletCost, "零费用的失败行仍落在钱包桶里（无维度时不过滤失败行，与总计 SQL 一致）")
		require.InDelta(t, 0.0, idx[creditBucketKey{"", service.BillingTypeBalance, 0}].ActualCost, 1e-9)
		require.InDelta(t, 5.0, idx[creditBucketKey{"", service.BillingTypeSubscription, card.ID}].ActualCost, 1e-9)
	})

	t.Run("api key dimension and filter", func(t *testing.T) {
		buckets, err := repo.GetCreditCostBuckets(ctx, usagestats.CreditCostBucketFilter{
			UserID: user.ID, APIKeyIDs: []int64{key1.ID}, StartTime: start, EndTime: end,
			Dimension: usagestats.CreditBucketAPIKey,
		})
		require.NoError(t, err)
		idx := indexCreditBuckets(buckets)
		k1 := strconv.FormatInt(key1.ID, 10)
		require.Len(t, idx, 2)
		require.InDelta(t, 1.0, idx[creditBucketKey{k1, service.BillingTypeBalance, 0}].ActualCost, 1e-9)
		require.InDelta(t, 2.0, idx[creditBucketKey{k1, service.BillingTypeSubscription, card.ID}].ActualCost, 1e-9)
	})

	t.Run("model dimension", func(t *testing.T) {
		buckets, err := repo.GetCreditCostBuckets(ctx, usagestats.CreditCostBucketFilter{
			UserID: user.ID, StartTime: start, EndTime: end,
			Dimension: usagestats.CreditBucketModel, ModelSource: usagestats.ModelSourceRequested,
		})
		require.NoError(t, err)
		idx := indexCreditBuckets(buckets)
		require.InDelta(t, 1.0, idx[creditBucketKey{"gpt-a", service.BillingTypeBalance, 0}].ActualCost, 1e-9)
		require.InDelta(t, 3.0, idx[creditBucketKey{"gpt-a", service.BillingTypeSubscription, card.ID}].ActualCost, 1e-9)
		require.InDelta(t, 2.0, idx[creditBucketKey{"gpt-b", service.BillingTypeSubscription, card.ID}].ActualCost, 1e-9)
	})

	t.Run("platform dimension excludes failed rows", func(t *testing.T) {
		buckets, err := repo.GetCreditCostBuckets(ctx, usagestats.CreditCostBucketFilter{
			UserID: user.ID, SplitAt: splitAt, Dimension: usagestats.CreditBucketPlatform,
		})
		require.NoError(t, err)
		idx := indexCreditBuckets(buckets)
		require.Len(t, idx, 2)
		wallet := idx[creditBucketKey{service.PlatformOpenAI, service.BillingTypeBalance, 0}]
		require.InDelta(t, 1.0, wallet.ActualCost, 1e-9)
		require.InDelta(t, 0.0, wallet.ActualCostSince, 1e-9)
		require.InDelta(t, 5.0, idx[creditBucketKey{service.PlatformOpenAI, service.BillingTypeSubscription, card.ID}].ActualCostSince, 1e-9)
	})

	t.Run("date dimension uses trend format", func(t *testing.T) {
		buckets, err := repo.GetCreditCostBuckets(ctx, usagestats.CreditCostBucketFilter{
			UserID: user.ID, StartTime: start, EndTime: end,
			Dimension: usagestats.CreditBucketDate, Granularity: "hour",
		})
		require.NoError(t, err)
		var total float64
		for _, b := range buckets {
			require.Regexp(t, `^\d{4}-\d{2}-\d{2} \d{2}:00$`, b.Key)
			total += b.ActualCost
		}
		require.InDelta(t, 6.0, total, 1e-9)
	})

	t.Run("rejects missing user and unknown dimension", func(t *testing.T) {
		_, err := repo.GetCreditCostBuckets(ctx, usagestats.CreditCostBucketFilter{})
		require.Error(t, err)
		_, err = repo.GetCreditCostBuckets(ctx, usagestats.CreditCostBucketFilter{UserID: user.ID, Dimension: "group"})
		require.Error(t, err)
	})
}
