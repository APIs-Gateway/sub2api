//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestOpsSLAAttribution_ExcludesClientOwned 验证 issue #16 Part A 的口径纠偏:
// SLA 失败计数(error_count_sla)须排除 error_owner='client',且裸表口径(queryErrorCounts /
// GetErrorTrend / GetErrorDistribution)与物化 rollup(UpsertHourlyMetrics → ops_metrics_hourly)
// 一致;preagg backfill 幂等;ListErrorLogs 的 errors/client/excluded/all 视图与 SLA 同义,
// 且「errors + 显式 owner=client」不再恒空(footgun 修复)。
func TestOpsSLAAttribution_ExcludesClientOwned(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "TRUNCATE ops_metrics_hourly")
	require.NoError(t, err)

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	// 固定一个过去的整点 bucket,所有行落在 [base, base+1h)。
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	at := base.Add(5 * time.Minute)
	end := base.Add(time.Hour)

	// 生产形态种子:owner ∈ {client, provider, platform, NULL}。
	// 期望:client 行(无论是否 business-limited)一律不计入 SLA;provider/platform/NULL 计入。
	type seed struct {
		phase    string
		errType  string
		status   int
		upstream interface{} // *int via int or nil
		owner    interface{} // string or nil
		bizLim   bool
	}
	seeds := []seed{
		// client-owned —— 不计入 SLA
		{"request", "billing_error", 403, nil, "client", true},          // overdraft:business-limited → excluded 桶
		{"request", "invalid_request_error", 426, nil, "client", false}, // 426 WebSocket upgrade required
		{"request", "invalid_request_error", 400, nil, "client", false}, // 本地客户端 400
		// service-attributable —— 计入 SLA
		{"upstream", "upstream_error", 502, 502, "provider", false},
		{"upstream", "invalid_request_error", 422, 422, "provider", false},
		{"internal", "api_error", 500, nil, "platform", false},
		{"internal", "api_error", 500, nil, nil, false}, // NULL owner → 安全默认仍计入
	}

	insert := func(s seed) {
		_, e := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				error_phase, error_type, severity, status_code, upstream_status_code,
				error_owner, is_business_limited, is_count_tokens, platform, created_at
			) VALUES ($1,$2,'error',$3,$4,$5,$6,FALSE,'anthropic',$7)`,
			s.phase, s.errType, s.status, s.upstream, s.owner, s.bizLim, at,
		)
		require.NoError(t, e)
	}
	for _, s := range seeds {
		insert(s)
	}

	const (
		wantTotal = int64(7) // 全部 status>=400
		wantBiz   = int64(1) // overdraft
		wantSLA   = int64(4) // 2 provider + 1 platform + 1 NULL;3 个 client 全部剔除
	)

	dashFilter := &service.OpsDashboardFilter{StartTime: base, EndTime: end}

	// ── ① 裸表 SLA 口径:queryErrorCounts(dashboard:885)──────────────────────
	total, biz, sla, _, _, _, qErr := repo.queryErrorCounts(ctx, dashFilter, base, end)
	require.NoError(t, qErr)
	require.Equal(t, wantTotal, total, "error_total")
	require.Equal(t, wantBiz, biz, "business_limited")
	require.Equal(t, wantSLA, sla, "error_count_sla 须排除 owner=client")

	// ── ② 趋势口径:GetErrorTrend(trends:456)——各 bucket SLA 求和应一致 ────────
	trend, tErr := repo.GetErrorTrend(ctx, dashFilter, 3600)
	require.NoError(t, tErr)
	var trendSLA int64
	for _, p := range trend.Points {
		trendSLA += p.ErrorCountSLA
	}
	require.Equal(t, wantSLA, trendSLA, "趋势 SLA 合计须与裸表一致")

	// ── ③ 分布口径:GetErrorDistribution(trends:567)——各状态码 SLA 求和应一致 ──
	dist, dErr := repo.GetErrorDistribution(ctx, dashFilter)
	require.NoError(t, dErr)
	var distSLA int64
	for _, it := range dist.Items {
		distSLA += it.SLA
	}
	require.Equal(t, wantSLA, distSLA, "分布 SLA 合计须与裸表一致")

	// ── ④ 物化 rollup:UpsertHourlyMetrics(preagg:95)须与裸表 SLA 一致 ──────────
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, base, end))
	readRollupSLA := func() (sla, biz int64) {
		row := integrationDB.QueryRowContext(ctx, `
			SELECT error_count_sla, business_limited_count
			FROM ops_metrics_hourly
			WHERE bucket_start = $1 AND platform IS NULL AND group_id IS NULL`, base)
		require.NoError(t, row.Scan(&sla, &biz))
		return sla, biz
	}
	rSLA, rBiz := readRollupSLA()
	require.Equal(t, wantSLA, rSLA, "rollup error_count_sla 须与裸表一致")
	require.Equal(t, wantBiz, rBiz, "rollup business_limited_count")

	// ── ⑤ backfill 幂等:重跑 preagg,rollup 不变 ──────────────────────────────
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, base, end))
	rSLA2, _ := readRollupSLA()
	require.Equal(t, rSLA, rSLA2, "重跑 preagg 须幂等")

	// ── ⑥ 列表视图(ops_repo:978)与 SLA 同义 + footgun ────────────────────────
	listTotal := func(view, owner string) int {
		res, e := repo.ListErrorLogs(ctx, &service.OpsErrorLogFilter{
			StartTime: &base, EndTime: &end, View: view, Owner: owner,
			Page: 1, PageSize: 100,
		})
		require.NoError(t, e)
		return res.Total
	}
	require.Equal(t, 4, listTotal("errors", ""), "errors 视图 = 服务可归因故障")
	require.Equal(t, 3, listTotal("client", ""), "client 视图 = 全部 owner=client")
	require.Equal(t, 1, listTotal("excluded", ""), "excluded 视图 = business-limited")
	require.Equal(t, 7, listTotal("all", ""), "all 视图 = 不过滤")
	// footgun:errors + 显式 owner=client → 非 business-limited 的 client 行(426/400)=2,不得恒空。
	require.Equal(t, 2, listTotal("errors", "client"), "errors + owner=client 须返回非 business-limited 的 client 行")
}
