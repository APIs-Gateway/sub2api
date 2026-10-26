//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// creditBucketRepoStub 只实现分桶这一个可选能力；内嵌的 UsageLogRepository 为 nil，
// 其他方法被调用会直接 panic，正好保证 CreditFiatTotals 不碰别的仓储方法。
type creditBucketRepoStub struct {
	UsageLogRepository
	buckets []usagestats.CreditCostBucket
	err     error
	got     usagestats.CreditCostBucketFilter
	calls   int
}

func (s *creditBucketRepoStub) GetCreditCostBuckets(_ context.Context, f usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
	s.calls++
	s.got = f
	return s.buckets, s.err
}

// 核心口径：合计必须逐桶按各自单价折算——钱包桶 ÷ m、订阅桶 × 该卡 u(D)——
// 而且结果要等于把同样的额度当成逐条用量记录、用列表接口的折算器算出来的和。
func TestCreditFiatTotals_ConvertsEachBucketAtItsOwnPrice(t *testing.T) {
	client, _ := newCreditFiatEntClient(t)
	owner := createCreditFiatUser(t, client, "totals-owner@example.com")
	card := createCreditFiatCard(t, client, owner.ID, 30)
	cfg := prodPricingConfig()

	repo := &creditBucketRepoStub{buckets: []usagestats.CreditCostBucket{
		{Key: "1", BillingType: BillingTypeBalance, ActualCost: 13, ActualCostSince: 6.5},
		{Key: "1", BillingType: BillingTypeSubscription, SubscriptionID: card.ID, ActualCost: 20, ActualCostSince: 10},
		{Key: "2", BillingType: BillingTypeSubscription, SubscriptionID: card.ID, ActualCost: 4},
	}}
	svc := NewUsageService(repo, nil, client, nil)

	filter := usagestats.CreditCostBucketFilter{UserID: owner.ID, SplitAt: time.Now(), Dimension: usagestats.CreditBucketAPIKey}
	totals, ok := svc.CreditFiatTotals(context.Background(), 13, cfg, filter)
	require.True(t, ok)
	require.Equal(t, filter, repo.got, "筛选条件要原样交给仓储")

	u := cfg.UnitPrice(30)
	require.Greater(t, u, 0.0)
	require.InDelta(t, 13.0/13+20*u, totals.Total["1"], 1e-8)
	require.InDelta(t, 6.5/13+10*u, totals.Since["1"], 1e-8)
	require.InDelta(t, 4*u, totals.Total["2"], 1e-8)
	require.InDelta(t, 0, totals.Since["2"], 1e-8)

	// 与用量列表逐条 fiat_cost 求和的结果一致。
	rate := svc.BuildCreditFiatRate(context.Background(), owner.ID, 13, cfg, []UsageLog{
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(card.ID)},
	})
	perRow := rate.Convert(13, BillingTypeBalance, 0) + rate.Convert(20, BillingTypeSubscription, card.ID)
	require.InDelta(t, perRow, totals.Total["1"], 1e-8)
}

// 别人的卡不能被用来折算：查卡带了 user_id 过滤，查不到就回落到钱包单价。
func TestCreditFiatTotals_ForeignCardFallsBackToWalletPrice(t *testing.T) {
	client, _ := newCreditFiatEntClient(t)
	owner := createCreditFiatUser(t, client, "totals-a@example.com")
	stranger := createCreditFiatUser(t, client, "totals-b@example.com")
	card := createCreditFiatCard(t, client, stranger.ID, 30)

	repo := &creditBucketRepoStub{buckets: []usagestats.CreditCostBucket{
		{BillingType: BillingTypeSubscription, SubscriptionID: card.ID, ActualCost: 26},
	}}
	svc := NewUsageService(repo, nil, client, nil)

	totals, ok := svc.CreditFiatTotals(context.Background(), 13, prodPricingConfig(), usagestats.CreditCostBucketFilter{UserID: owner.ID})
	require.True(t, ok)
	require.InDelta(t, 2.0, totals.Total[""], 1e-8)
	require.Nil(t, totals.Since, "未设置 SplitAt 时不产出 Since")
}

func TestCreditFiatTotals_Unavailable(t *testing.T) {
	cfg := prodPricingConfig()
	filter := usagestats.CreditCostBucketFilter{UserID: 7}

	t.Run("multiplier 1 skips the query", func(t *testing.T) {
		repo := &creditBucketRepoStub{}
		svc := NewUsageService(repo, nil, nil, nil)
		_, ok := svc.CreditFiatTotals(context.Background(), 1, cfg, filter)
		require.False(t, ok)
		_, ok = svc.CreditFiatTotals(context.Background(), 0, cfg, filter)
		require.False(t, ok, "倍率缺失按 1 处理")
		require.Zero(t, repo.calls, "free 站不该多跑聚合查询")
	})

	t.Run("repository without bucket support", func(t *testing.T) {
		svc := NewUsageService(nil, nil, nil, nil)
		_, ok := svc.CreditFiatTotals(context.Background(), 13, cfg, filter)
		require.False(t, ok)
	})

	t.Run("query failure", func(t *testing.T) {
		svc := NewUsageService(&creditBucketRepoStub{err: errors.New("boom")}, nil, nil, nil)
		_, ok := svc.CreditFiatTotals(context.Background(), 13, cfg, filter)
		require.False(t, ok)
	})

	t.Run("missing user", func(t *testing.T) {
		repo := &creditBucketRepoStub{}
		svc := NewUsageService(repo, nil, nil, nil)
		_, ok := svc.CreditFiatTotals(context.Background(), 13, cfg, usagestats.CreditCostBucketFilter{})
		require.False(t, ok)
		require.Zero(t, repo.calls)
	})
}

func TestCollectBucketSubscriptionIDs(t *testing.T) {
	ids := collectBucketSubscriptionIDs([]usagestats.CreditCostBucket{
		{BillingType: BillingTypeSubscription, SubscriptionID: 3},
		{BillingType: BillingTypeSubscription, SubscriptionID: 3, Key: "x"},
		{BillingType: BillingTypeSubscription, SubscriptionID: 0},
		{BillingType: BillingTypeBalance, SubscriptionID: 9},
		{BillingType: BillingTypeSubscription, SubscriptionID: 5},
	})
	require.Equal(t, []int64{3, 5}, ids)
}

func TestCardFiatPerCredit(t *testing.T) {
	cfg := prodPricingConfig()
	require.InDelta(t, cfg.UnitPrice(30), CardFiatPerCredit(cfg, 30), 1e-12)
	for _, d := range []float64{0, -1} {
		require.Zero(t, CardFiatPerCredit(cfg, d))
	}
}
