package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/lib/pq"
)

// ChannelMonitorGroupStatsAggregationTask 后台定时任务：每 5 分钟聚合最近 30 分钟窗口的分组真实成功率。
// 聚合逻辑：扫 usage_logs / ops_error_logs 按 (group_id, bucket_start) 聚合，写入 channel_monitor_group_stats。
//
// 排除规则（不计入成功或失败）：
//  1. 用户 4xx（status_code 400–499）
//  2. is_business_limited = true
//  3. nginx 边缘伪造响应：usage_logs 里 output_tokens IN (86, 116) 且 upstream_model_mismatch = true 且 total_cost = 0
//  4. 探测 key：api_key_id IN (配置列表)
//  5. 流中失败（ops_error_logs 里 status_code = 200）
//
// 成功：usage_logs 写入且不属于排除类。
// 失败：ops_error_logs 里 status_code >= 500 且不属于排除类。
//
// 此任务由 C2 注册到调度器（复用 OpsCleanupService 的 leader lock + heartbeat 或独立 cron）。
type ChannelMonitorGroupStatsAggregationTask struct {
	db *sql.DB
	// probeKeyIDs 探测 key 的 ID 列表，写入时排除这些 key 的行。
	// C1: 从配置读取（空切片占位）；C2 会从 deploy/config.example.yaml 的实际配置项读取。
	probeKeyIDs []int64
}

// computeAggregationWindow 计算聚合时间窗口。
// 窗口结束时间：当前时间减去延迟余量，截断到分钟。
// 窗口起始时间：窗口结束时间减去聚合窗口大小，再对齐到 10 分钟桶的边界。
func computeAggregationWindow(now time.Time) (windowStart, windowEnd time.Time) {
	windowEnd = now.Add(-time.Duration(channelMonitorGroupStatsLagMinutes) * time.Minute).Truncate(time.Minute)
	windowStart = windowEnd.Add(-time.Duration(channelMonitorGroupStatsWindowMinutes) * time.Minute).Truncate(10 * time.Minute)
	return windowStart, windowEnd
}

// NewChannelMonitorGroupStatsAggregationTask 构造聚合任务。
// probeKeyIDs 从配置读取（C1 传空切片，C2 从 config.ChannelMonitor.ProbeKeyIDs 读取）。
func NewChannelMonitorGroupStatsAggregationTask(db *sql.DB, probeKeyIDs []int64) *ChannelMonitorGroupStatsAggregationTask {
	if probeKeyIDs == nil {
		probeKeyIDs = []int64{}
	}
	return &ChannelMonitorGroupStatsAggregationTask{
		db:          db,
		probeKeyIDs: probeKeyIDs,
	}
}

// RunAggregation 执行一次聚合：扫最近 30 分钟窗口（给 1 分钟延迟余量），按 (group_id, bucket_start) 聚合。
func (t *ChannelMonitorGroupStatsAggregationTask) RunAggregation(ctx context.Context) error {
	if t == nil || t.db == nil {
		return fmt.Errorf("nil aggregation task or db")
	}

	now := time.Now().UTC()
	windowStart, windowEnd := computeAggregationWindow(now)

	slog.Info("channel_monitor_group_stats: aggregation start",
		"window_start", windowStart.Format(time.RFC3339),
		"window_end", windowEnd.Format(time.RFC3339))

	affected, err := t.UpsertGroupStats(ctx, windowStart, windowEnd)
	if err != nil {
		slog.Error("channel_monitor_group_stats: aggregation failed", "error", err)
		return err
	}

	slog.Info("channel_monitor_group_stats: aggregation complete",
		"affected_rows", affected,
		"window_start", windowStart.Format(time.RFC3339),
		"window_end", windowEnd.Format(time.RFC3339))
	return nil
}

// UpsertGroupStats 聚合 [windowStart, windowEnd) 窗口内的分组成功率，写入 channel_monitor_group_stats。
// 返回 upsert 影响的行数（INSERT + UPDATE）。
// 导出供集成测试使用。
func (t *ChannelMonitorGroupStatsAggregationTask) UpsertGroupStats(ctx context.Context, windowStart, windowEnd time.Time) (int64, error) {
	// 对齐窗口参数到 10 分钟边界，防止调用方传入未对齐的参数导致数据覆盖问题。
	windowStart = windowStart.Truncate(10 * time.Minute)
	windowEnd = windowEnd.Truncate(time.Minute)

	// 开启事务，设置 statement_timeout（参考 pricing_snapshot_repo_admin.go RecentBillingModels）。
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin aggregation tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '30s'"); err != nil {
		return 0, fmt.Errorf("set statement_timeout: %w", err)
	}

	// 单次 INSERT ... SELECT UNION 聚合成功 + 失败计数，idempotent upsert。
	// 使用 date_trunc 将每分钟对齐到 10 分钟桶起点。
	upsertSQL := `
WITH success_base AS (
    -- 成功计数：usage_logs 写入且排除探测 key + nginx 伪造响应
    SELECT
        group_id,
        date_trunc('minute', created_at) -
            INTERVAL '1 minute' * (EXTRACT(MINUTE FROM date_trunc('minute', created_at))::int % 10) AS bucket_start,
        COUNT(*) AS success_count,
        0 AS error_5xx_count
    FROM usage_logs
    WHERE created_at >= $1 AND created_at < $2
      AND group_id IS NOT NULL
      AND (ARRAY_LENGTH($3::BIGINT[], 1) IS NULL OR api_key_id <> ALL($3::BIGINT[]))
      AND NOT (output_tokens IN (86, 116) AND upstream_model_mismatch = true AND total_cost = 0)
    GROUP BY group_id, bucket_start
),
error_base AS (
    -- 失败计数：ops_error_logs 里 status_code >= 500 且排除用户 4xx、业务限流、流中失败（status_code=200）
    SELECT
        group_id,
        date_trunc('minute', created_at) -
            INTERVAL '1 minute' * (EXTRACT(MINUTE FROM date_trunc('minute', created_at))::int % 10) AS bucket_start,
        0 AS success_count,
        COUNT(*) AS error_5xx_count
    FROM ops_error_logs
    WHERE created_at >= $1 AND created_at < $2
      AND group_id IS NOT NULL
      AND status_code >= 500
      AND NOT is_business_limited
      AND (ARRAY_LENGTH($3::BIGINT[], 1) IS NULL OR api_key_id <> ALL($3::BIGINT[]))
    GROUP BY group_id, bucket_start
),
combined AS (
    SELECT group_id, bucket_start, success_count, error_5xx_count FROM success_base
    UNION ALL
    SELECT group_id, bucket_start, success_count, error_5xx_count FROM error_base
),
aggregated AS (
    SELECT
        group_id,
        bucket_start,
        SUM(success_count) AS success_count,
        SUM(error_5xx_count) AS error_5xx_count,
        SUM(success_count) + SUM(error_5xx_count) AS total_count,
        CASE
            WHEN SUM(success_count) + SUM(error_5xx_count) > 0
            THEN SUM(success_count)::NUMERIC / (SUM(success_count) + SUM(error_5xx_count))::NUMERIC
            ELSE NULL
        END AS success_rate
    FROM combined
    GROUP BY group_id, bucket_start
)
INSERT INTO channel_monitor_group_stats
    (group_id, bucket_start, bucket_minutes, success_count, error_5xx_count, total_count, success_rate, created_at)
SELECT
    group_id,
    bucket_start,
    10,  -- bucket_minutes 固定 10
    success_count,
    error_5xx_count,
    total_count,
    success_rate,
    NOW()
FROM aggregated
ON CONFLICT (group_id, bucket_start, bucket_minutes) DO UPDATE SET
    success_count = EXCLUDED.success_count,
    error_5xx_count = EXCLUDED.error_5xx_count,
    total_count = EXCLUDED.total_count,
    success_rate = EXCLUDED.success_rate
`

	res, err := tx.ExecContext(ctx, upsertSQL, windowStart, windowEnd, pq.Array(t.probeKeyIDs))
	if err != nil {
		return 0, fmt.Errorf("upsert group_stats [%s, %s): %w",
			windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339), err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit aggregation tx: %w", err)
	}

	affected, _ := res.RowsAffected()
	return affected, nil
}
