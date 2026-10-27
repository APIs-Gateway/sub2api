package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.OpsUnpricedBillingRepository = (*opsRepository)(nil)

// opsUnpricedBillingMaxRows 是单次查询返回的（分组、模型）组合数上限。正常情况下组合数是个位数到两位数，
// 这个上限只是防止异常数据拖垮一次查询；超出的是行数最少的那些。
const opsUnpricedBillingMaxRows = 5000

// opsUnpricedBillingPredicate 是「无价计费」的行级口径，必须和 service/ops_unpriced_billing.go 顶部的说明一致：
// 有用量、分组倍率大于 0、两个成本都为 0、且不是上游模型不一致留下的审计行（那类行不计费是有意的）。
// 拼在 buildUsageWhere 生成的 WHERE 之后，所以 created_at 范围条件始终排在最前面。
const opsUnpricedBillingPredicate = `
  AND ul.total_cost = 0
  AND ul.actual_cost = 0
  AND ul.rate_multiplier > 0
  AND ul.upstream_model_mismatch = FALSE
  AND (ul.input_tokens > 0 OR ul.output_tokens > 0 OR ul.cache_creation_tokens > 0 OR ul.cache_read_tokens > 0
       OR ul.image_input_tokens > 0 OR ul.image_output_tokens > 0 OR COALESCE(ul.image_count, 0) > 0)`

// ListUnpricedBillingUsage 返回 [StartTime, EndTime) 内按（分组、模型）聚合的无价用量行，按行数降序。
func (r *opsRepository) ListUnpricedBillingUsage(ctx context.Context, filter *service.OpsUnpricedBillingFilter) ([]*service.OpsUnpricedBillingRow, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return nil, fmt.Errorf("start_time must be <= end_time")
	}

	dashboardFilter := &service.OpsDashboardFilter{
		StartTime: filter.StartTime.UTC(),
		EndTime:   filter.EndTime.UTC(),
		Platform:  strings.TrimSpace(strings.ToLower(filter.Platform)),
		GroupID:   filter.GroupID,
	}
	join, where, args, _ := buildUsageWhere(dashboardFilter, dashboardFilter.StartTime, dashboardFilter.EndTime, 1)

	query := `
WITH hit AS (
  SELECT
    ul.group_id AS group_id,
    ul.model AS model,
    COUNT(*)::bigint AS row_count,
    COALESCE(SUM(ul.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(ul.output_tokens), 0)::bigint AS output_tokens,
    COALESCE(SUM(ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS cache_tokens,
    COALESCE(SUM(COALESCE(ul.image_count, 0)), 0)::bigint AS image_count,
    MIN(ul.created_at) AS first_seen,
    MAX(ul.created_at) AS last_seen
  FROM usage_logs ul
  ` + join + `
  ` + where + opsUnpricedBillingPredicate + `
  GROUP BY ul.group_id, ul.model
)
SELECT
  h.group_id,
  COALESCE(gn.name, ''),
  COALESCE(gn.platform, ''),
  h.model,
  h.row_count,
  h.input_tokens,
  h.output_tokens,
  h.cache_tokens,
  h.image_count,
  h.first_seen,
  h.last_seen
FROM hit h
LEFT JOIN groups gn ON gn.id = h.group_id
ORDER BY h.row_count DESC, h.group_id ASC NULLS LAST, h.model ASC
LIMIT ` + fmt.Sprintf("%d", opsUnpricedBillingMaxRows)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsUnpricedBillingRow, 0, 16)
	for rows.Next() {
		var (
			groupID   sql.NullInt64
			item      service.OpsUnpricedBillingRow
			firstSeen time.Time
			lastSeen  time.Time
		)
		if err := rows.Scan(
			&groupID,
			&item.GroupName,
			&item.Platform,
			&item.Model,
			&item.Rows,
			&item.InputTokens,
			&item.OutputTokens,
			&item.CacheTokens,
			&item.ImageCount,
			&firstSeen,
			&lastSeen,
		); err != nil {
			return nil, err
		}
		if groupID.Valid {
			id := groupID.Int64
			item.GroupID = &id
		}
		item.FirstSeen = firstSeen.UTC()
		item.LastSeen = lastSeen.UTC()
		out = append(out, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
