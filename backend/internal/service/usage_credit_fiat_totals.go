package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/shopspring/decimal"
)

// CreditCostBucketReader 是 usage 仓储的可选能力：按扣费来源分桶汇总额度。
//
// 刻意不并进 UsageLogRepository：那个接口有大量测试桩实现，加方法会让它们全部
// 编译失败。只有真实的仓储实现它，测试桩不实现时法币合计自动不可用。
type CreditCostBucketReader interface {
	GetCreditCostBuckets(ctx context.Context, filter usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error)
}

// CreditFiatTotals 是按维度汇总后的法币合计，键与分桶维度的 Key 一致。
type CreditFiatTotals struct {
	// Total 是整个筛选范围的法币合计。
	Total map[string]float64
	// Since 是 created_at >= SplitAt 部分的法币合计（未设置 SplitAt 时为空）。
	Since map[string]float64
}

// CreditFiatTotals 把一个筛选范围内的额度合计折算成法币。
//
// 额度合计不能整体折算：同一范围里混着钱包扣费和若干张订阅卡的扣费，每一类的
// 法币单价都不同（钱包 1/m，订阅卡 u(D)），整体按 1/m 折算会把订阅用户的花费
// 高估到接近两倍。所以先按 (billing_type, subscription_id) 分桶，再逐桶用与
// 用量列表 fiat_cost 相同的 CreditFiatRate 折算后相加——合计恰好等于逐条
// fiat_cost 之和。
//
// ok=false 表示本次不提供法币合计，调用方不要填法币字段，让前端回落到额度展示：
//   - 充值倍率为 1（如 free 站）：前端此时只按美元展示，没必要多跑一次聚合查询；
//   - 仓储不支持分桶、或查询失败：法币只是展示增强，不能让统计接口因此报错。
func (s *UsageService) CreditFiatTotals(
	ctx context.Context,
	multiplier float64,
	cfg SubscriptionPricingConfig,
	filter usagestats.CreditCostBucketFilter,
) (*CreditFiatTotals, bool) {
	if s == nil || filter.UserID <= 0 {
		return nil, false
	}
	if normalizeBalanceRechargeMultiplier(multiplier) == defaultBalanceRechargeMultiplier {
		return nil, false
	}
	reader, ok := s.usageRepo.(CreditCostBucketReader)
	if !ok {
		return nil, false
	}
	buckets, err := reader.GetCreditCostBuckets(ctx, filter)
	if err != nil {
		return nil, false
	}

	rate := s.buildCreditFiatRateForSubscriptions(ctx, filter.UserID, multiplier, cfg, collectBucketSubscriptionIDs(buckets))
	return sumCreditFiatBuckets(rate, buckets, !filter.SplitAt.IsZero()), true
}

// sumCreditFiatBuckets 逐桶折算并按维度 Key 相加。用 decimal 累加，避免几百个
// 小额桶相加时出现浮点尾差，结果与 CreditFiatRate 同样保留 8 位小数。
func sumCreditFiatBuckets(rate *CreditFiatRate, buckets []usagestats.CreditCostBucket, withSince bool) *CreditFiatTotals {
	total := make(map[string]decimal.Decimal)
	since := make(map[string]decimal.Decimal)
	for _, b := range buckets {
		total[b.Key] = total[b.Key].Add(decimal.NewFromFloat(rate.Convert(b.ActualCost, b.BillingType, b.SubscriptionID)))
		if withSince {
			since[b.Key] = since[b.Key].Add(decimal.NewFromFloat(rate.Convert(b.ActualCostSince, b.BillingType, b.SubscriptionID)))
		}
	}

	out := &CreditFiatTotals{Total: make(map[string]float64, len(total))}
	for k, v := range total {
		out.Total[k] = v.Round(creditFiatRateScale).InexactFloat64()
	}
	if withSince {
		out.Since = make(map[string]float64, len(since))
		for k, v := range since {
			out.Since[k] = v.Round(creditFiatRateScale).InexactFloat64()
		}
	}
	return out
}

// collectBucketSubscriptionIDs 挑出需要查卡的订阅 ID 并去重，判定规则与
// collectSubscriptionIDs 一致：只有订阅扣费且带卡 ID 的桶才按卡单价折算。
func collectBucketSubscriptionIDs(buckets []usagestats.CreditCostBucket) []int64 {
	seen := make(map[int64]struct{})
	ids := make([]int64, 0, 4)
	for _, b := range buckets {
		if b.BillingType != BillingTypeSubscription || b.SubscriptionID <= 0 {
			continue
		}
		if _, ok := seen[b.SubscriptionID]; ok {
			continue
		}
		seen[b.SubscriptionID] = struct{}{}
		ids = append(ids, b.SubscriptionID)
	}
	return ids
}
