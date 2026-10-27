package usagestats

import "time"

// CreditBucketDimension 是分桶聚合在 (billing_type, subscription_id) 之外再叠加的维度。
type CreditBucketDimension string

const (
	// CreditBucketNone 不叠加维度，整个筛选范围只按扣费来源分桶。
	CreditBucketNone CreditBucketDimension = ""
	// CreditBucketDate 按 TO_CHAR(created_at, granularity) 分组，与趋势查询同口径。
	CreditBucketDate CreditBucketDimension = "date"
	// CreditBucketModel 按模型分组，模型口径由 ModelSource 决定，与模型统计同口径。
	CreditBucketModel CreditBucketDimension = "model"
	// CreditBucketAPIKey 按 api_key_id 分组。
	CreditBucketAPIKey CreditBucketDimension = "api_key"
	// CreditBucketPlatform 按「有效平台」分组，与仪表盘 by_platform 同口径
	// （同样只统计 actual_cost > 0 的行、排除无法确定平台的行）。
	CreditBucketPlatform CreditBucketDimension = "platform"
)

// CreditCostBucketFilter 描述一次分桶聚合的筛选范围。
//
// 每个调用方都要让筛选条件与它要配对的原始统计查询完全一致，否则「人民币合计」
// 与同一接口返回的额度合计就不是同一批记录。
type CreditCostBucketFilter struct {
	// UserID 必填：只聚合这个用户自己的记录。
	UserID int64
	// APIKeyIDs 非空时只统计这些 Key。
	APIKeyIDs []int64
	// StartTime / EndTime 为零值时表示该侧不设边界（created_at >= start, < end）。
	StartTime time.Time
	EndTime   time.Time
	// SplitAt 非零时，额外给出 created_at >= SplitAt 部分的合计（仪表盘「今日」用）。
	SplitAt time.Time

	Dimension CreditBucketDimension
	// Granularity 仅 CreditBucketDate 使用（day / hour / week / month）。
	Granularity string
	// ModelSource 仅 CreditBucketModel 使用（requested / upstream / mapping）。
	ModelSource string
}

// CreditCostBucket 是一个 (维度值, billing_type, subscription_id) 分组的 actual_cost 合计。
type CreditCostBucket struct {
	// Key 是维度值；CreditBucketNone 时为空串，CreditBucketAPIKey 时是十进制 Key ID。
	Key            string
	BillingType    int8
	SubscriptionID int64
	// ActualCost 是整个筛选范围内的额度合计。
	ActualCost float64
	// ActualCostSince 是 created_at >= SplitAt 部分的额度合计；SplitAt 为零值时恒为 0。
	ActualCostSince float64
}
