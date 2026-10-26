package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/lib/pq"
)

// GetCreditCostBuckets 按 (维度, billing_type, subscription_id) 分组汇总 actual_cost。
//
// actual_cost 是站内额度，钱包扣的额度和每张订阅卡扣的额度各有不同的法币单价，
// 所以「花了多少人民币」不能对合计值整体折算，只能先按扣费来源拆开、各自折算
// 再相加。这里只负责拆桶；折算在 service 层用 CreditFiatRate 完成。
//
// 维度表达式刻意复用原有统计查询的同一套表达式（safeDateFormat /
// resolveModelDimensionExpression / usageLogEffectivePlatformExpr），保证同一个
// 接口里人民币合计和额度合计对应的是同一批记录、同一套分组。
func (r *usageLogRepository) GetCreditCostBuckets(ctx context.Context, f usagestats.CreditCostBucketFilter) (buckets []usagestats.CreditCostBucket, err error) {
	if f.UserID <= 0 {
		return nil, fmt.Errorf("credit cost buckets: user id is required")
	}

	keyExpr := "''"
	from := "usage_logs ul"
	conditions := []string{"ul.user_id = $1"}
	args := []any{f.UserID}

	switch f.Dimension {
	case usagestats.CreditBucketNone:
	case usagestats.CreditBucketDate:
		keyExpr = fmt.Sprintf("TO_CHAR(ul.created_at, '%s')", safeDateFormat(f.Granularity))
	case usagestats.CreditBucketModel:
		keyExpr = resolveModelDimensionExpression(f.ModelSource)
	case usagestats.CreditBucketAPIKey:
		keyExpr = "ul.api_key_id::text"
	case usagestats.CreditBucketPlatform:
		keyExpr = usageLogEffectivePlatformExpr
		from = "usage_logs ul LEFT JOIN groups g ON g.id = ul.group_id LEFT JOIN accounts a ON a.id = ul.account_id"
		conditions = append(conditions,
			usageLogSuccessFilterUL,
			usageLogEffectivePlatformExpr+" IS NOT NULL",
			usageLogEffectivePlatformExpr+" <> ''",
		)
	default:
		return nil, fmt.Errorf("credit cost buckets: unsupported dimension %q", f.Dimension)
	}

	if ids := normalizePositiveInt64IDs(f.APIKeyIDs); len(ids) > 0 {
		args = append(args, pq.Array(ids))
		conditions = append(conditions, fmt.Sprintf("ul.api_key_id = ANY($%d)", len(args)))
	}
	if !f.StartTime.IsZero() {
		args = append(args, f.StartTime)
		conditions = append(conditions, fmt.Sprintf("ul.created_at >= $%d", len(args)))
	}
	if !f.EndTime.IsZero() {
		args = append(args, f.EndTime)
		conditions = append(conditions, fmt.Sprintf("ul.created_at < $%d", len(args)))
	}

	sinceExpr := "0::double precision"
	if !f.SplitAt.IsZero() {
		args = append(args, f.SplitAt)
		sinceExpr = fmt.Sprintf("COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $%d), 0)", len(args))
	}

	where := conditions[0]
	for _, cond := range conditions[1:] {
		where += " AND " + cond
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(%s, '') AS bucket_key,
			ul.billing_type,
			COALESCE(ul.subscription_id, 0) AS subscription_id,
			COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
			%s AS actual_cost_since
		FROM %s
		WHERE %s
		GROUP BY 1, 2, 3
	`, keyExpr, sinceExpr, from, where)

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			buckets = nil
		}
	}()

	for rows.Next() {
		var (
			b           usagestats.CreditCostBucket
			billingType int16
		)
		if err := rows.Scan(&b.Key, &billingType, &b.SubscriptionID, &b.ActualCost, &b.ActualCostSince); err != nil {
			return nil, err
		}
		b.BillingType = int8(billingType)
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buckets, nil
}
