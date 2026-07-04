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
		// client_via_upstream(issue #16 Part B/A2):上游判定的请求形 4xx,同样不计入 SLA
		{"upstream", "invalid_request_error", 422, 422, "client_via_upstream", false},
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
		wantTotal = int64(8) // 全部 status>=400
		wantBiz   = int64(1) // overdraft
		wantSLA   = int64(4) // 2 provider + 1 platform + 1 NULL;3 个 client + 1 client_via_upstream 全部剔除
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
	require.Equal(t, 4, listTotal("errors", ""), "errors 视图 = 服务可归因故障(排除 client + client_via_upstream)")
	require.Equal(t, 4, listTotal("client", ""), "client 视图 = client(3)+ client_via_upstream(1)")
	require.Equal(t, 1, listTotal("excluded", ""), "excluded 视图 = business-limited")
	require.Equal(t, 8, listTotal("all", ""), "all 视图 = 不过滤")
	// footgun:errors + 显式 owner=client → 非 business-limited 的 client 行(426/400)=2,不得恒空。
	require.Equal(t, 2, listTotal("errors", "client"), "errors + owner=client 须返回非 business-limited 的 client 行")
	// client_via_upstream 行单独可查。
	require.Equal(t, 1, listTotal("all", "client_via_upstream"), "owner=client_via_upstream 行单独可查")
	// footgun:errors + 显式 owner=client_via_upstream → 非 business-limited 的 cvu 行(1),不得恒空。
	require.Equal(t, 1, listTotal("errors", "client_via_upstream"), "errors + owner=client_via_upstream 须返回非 business-limited 的 cvu 行")
}

// TestOpsSLAAttribution_StreamingClientViaUpstream 覆盖 issue #16 Part B 的 T4(流式归因)端到端口径:
// 流式已开始时对外 status=200(无法再改 HTTP 码),但上游真实是请求形 4xx,归因为 client_via_upstream。
// 断言:① 因 status_code=200<400,裸表/物化 SLA 与总错误均不计该行(口径以 client-facing status 为准);
// ② 列表 client 视图含该行、errors 视图不含(owner 维度排除 client_via_upstream);
// ③ rollup error_count_total 同样不含(物化层与裸表一致)。
func TestOpsSLAAttribution_StreamingClientViaUpstream(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "TRUNCATE ops_metrics_hourly")
	require.NoError(t, err)

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	base := time.Date(2026, 2, 20, 8, 0, 0, 0, time.UTC)
	at := base.Add(3 * time.Minute)
	end := base.Add(time.Hour)

	insert := func(phase, errType string, status int, upstream interface{}, owner interface{}, bizLim bool) {
		_, e := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				error_phase, error_type, severity, status_code, upstream_status_code,
				error_owner, is_business_limited, is_count_tokens, platform, created_at
			) VALUES ($1,$2,'error',$3,$4,$5,$6,FALSE,'anthropic',$7)`,
			phase, errType, status, upstream, owner, bizLim, at,
		)
		require.NoError(t, e)
	}

	// 流式 cvu 行:对外 200,上游 422(归因 client_via_upstream,业务上非 business-limited)。
	insert("upstream", "invalid_request_error", 200, 422, "client_via_upstream", false)
	// 一个常规 provider 5xx(对外 502,计入 SLA)作对照,确保口径不是"全 0"假阳性。
	insert("upstream", "upstream_error", 502, 502, "provider", false)

	const (
		wantTotal = int64(1) // 仅 502 那行 status>=400;流式 200 行不计
		wantSLA   = int64(1) // 仅 provider 502
	)

	dashFilter := &service.OpsDashboardFilter{StartTime: base, EndTime: end}

	// ── 裸表口径 ───────────────────────────────────────────────────────────
	total, biz, sla, _, _, _, qErr := repo.queryErrorCounts(ctx, dashFilter, base, end)
	require.NoError(t, qErr)
	require.Equal(t, wantTotal, total, "对外 200 的流式 cvu 行不计入 error_total")
	require.Equal(t, int64(0), biz, "无 business-limited 行")
	require.Equal(t, wantSLA, sla, "SLA 仅 provider 502;流式 cvu 行天然不计(200<400)")

	// ── 物化 rollup 与裸表一致 ─────────────────────────────────────────────
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, base, end))
	var rTotal, rSLA int64
	row := integrationDB.QueryRowContext(ctx, `
		SELECT error_count_total, error_count_sla
		FROM ops_metrics_hourly
		WHERE bucket_start = $1 AND platform IS NULL AND group_id IS NULL`, base)
	require.NoError(t, row.Scan(&rTotal, &rSLA))
	require.Equal(t, wantTotal, rTotal, "rollup error_count_total 须与裸表一致(不含流式 cvu)")
	require.Equal(t, wantSLA, rSLA, "rollup error_count_sla 须与裸表一致")

	// ── 列表视图分层 ──────────────────────────────────────────────────────
	// 默认列表有 status>=400 基线守卫(buildOpsErrorLogsWhere):对外 200 的流式 cvu 行不进默认列表,
	// 须经 phase=upstream 视角(守卫旁路,用于上游健康排查)才可见。两层都断言,确保既不泄漏到默认故障列表、
	// 又能被运营按上游维度查到。
	listTotal := func(phase, view, owner string) int {
		res, e := repo.ListErrorLogs(ctx, &service.OpsErrorLogFilter{
			StartTime: &base, EndTime: &end, Phase: phase, View: view, Owner: owner,
			Page: 1, PageSize: 100,
		})
		require.NoError(t, e)
		return res.Total
	}
	// 默认列表(无 phase 过滤,status>=400 守卫生效):仅 provider 502;流式 200 cvu 行被守卫挡掉。
	require.Equal(t, 1, listTotal("", "errors", ""), "默认 errors 视图仅 provider 502")
	require.Equal(t, 0, listTotal("", "client", ""), "默认 client 视图看不到对外 200 的流式 cvu 行(status>=400 守卫)")
	require.Equal(t, 1, listTotal("", "all", ""), "默认 all 视图也受守卫约束,仅 502 行")
	// phase=upstream 视角(守卫旁路):流式 cvu 行可见,且 errors 视图按 owner 仍排除它。
	require.Equal(t, 2, listTotal("upstream", "all", ""), "upstream 视角可见两行(含对外 200 的流式 cvu)")
	require.Equal(t, 1, listTotal("upstream", "errors", ""), "upstream 视角 errors 仍排除 client_via_upstream,仅 502")
	require.Equal(t, 1, listTotal("upstream", "client", ""), "upstream 视角 client 视图恰含流式 cvu 行")
}
