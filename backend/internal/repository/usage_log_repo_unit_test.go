//go:build unit

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSafeDateFormat(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
		expected    string
	}{
		// 合法值
		{"hour", "hour", "YYYY-MM-DD HH24:00"},
		{"day", "day", "YYYY-MM-DD"},
		{"week", "week", "IYYY-IW"},
		{"month", "month", "YYYY-MM"},

		// 非法值回退到默认
		{"空字符串", "", "YYYY-MM-DD"},
		{"未知粒度 year", "year", "YYYY-MM-DD"},
		{"未知粒度 minute", "minute", "YYYY-MM-DD"},

		// 恶意字符串
		{"SQL 注入尝试", "'; DROP TABLE users; --", "YYYY-MM-DD"},
		{"带引号", "day'", "YYYY-MM-DD"},
		{"带括号", "day)", "YYYY-MM-DD"},
		{"Unicode", "日", "YYYY-MM-DD"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeDateFormat(tc.granularity)
			require.Equal(t, tc.expected, got, "safeDateFormat(%q)", tc.granularity)
		})
	}
}

func TestBuildUsageLogBatchInsertQuery_UsesConflictDoNothing(t *testing.T) {
	log := &service.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		AccountID:    3,
		RequestID:    "req-batch-no-update",
		Model:        "gpt-5",
		InputTokens:  10,
		OutputTokens: 5,
		TotalCost:    1.2,
		ActualCost:   1.2,
		CreatedAt:    time.Now().UTC(),
	}
	prepared := prepareUsageLogInsert(log)

	query, _ := buildUsageLogBatchInsertQuery([]string{usageLogBatchKey(log.RequestID, log.APIKeyID)}, map[string]usageLogInsertPrepared{
		usageLogBatchKey(log.RequestID, log.APIKeyID): prepared,
	})

	require.Contains(t, query, "ON CONFLICT (request_id, api_key_id) DO NOTHING")
	require.NotContains(t, strings.ToUpper(query), "DO UPDATE")
}

func TestUsageLogRepositoryListWithFiltersUpstreamModelMismatch(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	mismatch := true
	filters := usagestats.UsageLogFilters{
		UpstreamModelMismatch: &mismatch,
		ExactTotal:            true,
	}

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM usage_logs WHERE upstream_model_mismatch = \\$1").
		WithArgs(mismatch).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT .* FROM usage_logs WHERE upstream_model_mismatch = \\$1 ORDER BY id DESC LIMIT \\$2 OFFSET \\$3").
		WithArgs(mismatch, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	logs, page, err := repo.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.Empty(t, logs)
	require.NotNil(t, page)
	require.Equal(t, int64(0), page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersUpstreamModelMismatch(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	mismatch := true
	filters := usagestats.UsageLogFilters{
		UpstreamModelMismatch: &mismatch,
	}

	mock.ExpectQuery("(?s)FROM usage_logs\\s+WHERE upstream_model_mismatch = \\$1.*GROUP BY GROUPING SETS").
		WithArgs(mismatch).
		WillReturnRows(sqlmock.NewRows([]string{
			"inbound_grouped",
			"upstream_grouped",
			"inbound_endpoint",
			"upstream_endpoint",
			"requests",
			"input_tokens",
			"output_tokens",
			"cache_creation_tokens",
			"cache_read_tokens",
			"cost",
			"actual_cost",
			"account_cost",
			"avg_duration_ms",
		}))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(0), stats.TotalRequests)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestFlushBestEffortBatch_CtxOrLockError_SkipsPerItemRetry 验证批量写入失败原因是
// ctx 超时或 PG 锁超时时，错误直接回给调用方，不发起任何逐条 INSERT。
// 旧代码（每条独立 10s）会在这里发起 len(batch) 次额外 INSERT，导致测试因
// 未预期的 ExecContext 调用而在 mock.ExpectationsWereMet() 报错。
func TestFlushBestEffortBatch_CtxOrLockError_SkipsPerItemRetry(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"context.DeadlineExceeded", context.DeadlineExceeded},
		{"context.Canceled", context.Canceled},
		{"PG 55P03 lock_not_available", &pq.Error{Code: "55P03"}},
		{"PG 57014 statement_timeout", &pq.Error{Code: "57014"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}

			log := &service.UsageLog{
				UserID:    1,
				APIKeyID:  2,
				AccountID: 3,
				RequestID: "req-be-lockfail",
				Model:     "gpt-5",
				CreatedAt: time.Now().UTC(),
			}
			prepared := prepareUsageLogInsert(log)
			resultCh := make(chan error, 1)
			req := usageLogBestEffortRequest{
				prepared: prepared,
				apiKeyID: log.APIKeyID,
				resultCh: resultCh,
			}

			// 批量 INSERT 失败，原因是 ctx/lock 错误。
			// 不注册任何逐条 INSERT 预期；若实现错误地重试，mock 会报多余的调用。
			mock.ExpectExec(`INSERT INTO usage_logs`).WillReturnError(tc.err)

			repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{req})

			select {
			case err := <-resultCh:
				require.Error(t, err, "ctx/lock 错误应直接回给调用方")
			case <-time.After(2 * time.Second):
				t.Fatal("等待结果超时")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "不应有多余的逐条 INSERT")
		})
	}
}

// TestFlushBestEffortBatch_GenericError_ConcurrentFallbackBoundedByTotalTimeout 验证
// 批量写入因普通错误失败时，兜底路径：
//  1. 并发执行，N 条的总耗时 ≈ 单条延迟，而非 N × 单条延迟（串行特征）。
//  2. 所有条目共用 usageLogFallbackTotalTimeout 总截止时间，而不是每条独享 10s。
//
// 旧代码串行执行，N=3 × delay=40ms = 120ms；新代码并发，总耗时 ≈ 40ms。
// 若将 timeout 改回每条 10s 的旧代码，耗时断言（< 100ms）仍会通过（mock 响应很快），
// 但并发与总截止时间的组合通过耗时上界来保证。
func TestFlushBestEffortBatch_GenericError_ConcurrentFallbackBoundedByTotalTimeout(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	// 缩短总超时，使测试快速结束，同时仍能区分"共用总截止"与"每条独享 10s"。
	origTimeout := usageLogFallbackTotalTimeout
	origConcurrency := usageLogFallbackConcurrency
	usageLogFallbackTotalTimeout = 500 * time.Millisecond
	usageLogFallbackConcurrency = 8
	defer func() {
		usageLogFallbackTotalTimeout = origTimeout
		usageLogFallbackConcurrency = origConcurrency
	}()

	const n = 3
	const itemDelay = 40 * time.Millisecond

	reqs := make([]usageLogBestEffortRequest, n)
	for i := range reqs {
		ul := &service.UsageLog{
			UserID:    int64(i + 1),
			APIKeyID:  int64(i + 1),
			AccountID: int64(i + 1),
			RequestID: fmt.Sprintf("req-be-conc-%d", i),
			Model:     "gpt-5",
			CreatedAt: time.Now().UTC(),
		}
		reqs[i] = usageLogBestEffortRequest{
			prepared: prepareUsageLogInsert(ul),
			apiKeyID: ul.APIKeyID,
			resultCh: make(chan error, 1),
		}
	}

	// 批量 INSERT 失败（普通错误，触发逐条兜底路径）。
	mock.ExpectExec(`INSERT INTO usage_logs`).WillReturnError(fmt.Errorf("simulated batch error"))
	// 每条兜底成功，各延迟 itemDelay 模拟真实写入。
	for i := 0; i < n; i++ {
		mock.ExpectExec(`INSERT INTO usage_logs`).
			WillDelayFor(itemDelay).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}

	start := time.Now()
	repo.flushBestEffortBatch(db, reqs)
	elapsed := time.Since(start)

	// 并发执行：总耗时应接近单条延迟，而非 N×延迟（串行）。
	// 给并发调度留 3× 余量仍远低于串行下限 n×itemDelay。
	concurrentUpperBound := itemDelay * 3
	require.Less(t, elapsed, concurrentUpperBound,
		"兜底应并发执行：elapsed=%v 不应超过 %v（串行下限 %v）",
		elapsed, concurrentUpperBound, time.Duration(n)*itemDelay)

	for i, req := range reqs {
		select {
		case err := <-req.resultCh:
			require.NoError(t, err, "req[%d] 兜底应写入成功", i)
		case <-time.After(2 * time.Second):
			t.Fatalf("等待 req[%d] 结果超时", i)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestFlushBestEffortBatch_GenericError_TotalDeadlineEnforced 验证当兜底总超时短于
// 单条延迟时，所有条目都因共用截止时间超时而失败——而旧代码（每条独享 10s ctx）
// 会让 mock delay(60ms) < 10s 的条目成功（结果为 nil），测试因此失败，从而区分前后。
func TestFlushBestEffortBatch_GenericError_TotalDeadlineEnforced(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	// 总超时短于每条的模拟延迟，确保共用截止时间会先到期。
	const totalTimeout = 30 * time.Millisecond
	const itemDelay = 60 * time.Millisecond // > totalTimeout

	origTimeout := usageLogFallbackTotalTimeout
	origConcurrency := usageLogFallbackConcurrency
	usageLogFallbackTotalTimeout = totalTimeout
	usageLogFallbackConcurrency = 8
	defer func() {
		usageLogFallbackTotalTimeout = origTimeout
		usageLogFallbackConcurrency = origConcurrency
	}()

	const n = 2
	reqs := make([]usageLogBestEffortRequest, n)
	for i := range reqs {
		ul := &service.UsageLog{
			UserID:    int64(i + 1),
			APIKeyID:  int64(i + 1),
			AccountID: int64(i + 1),
			RequestID: fmt.Sprintf("req-be-deadline-%d", i),
			Model:     "gpt-5",
			CreatedAt: time.Now().UTC(),
		}
		reqs[i] = usageLogBestEffortRequest{
			prepared: prepareUsageLogInsert(ul),
			apiKeyID: ul.APIKeyID,
			resultCh: make(chan error, 1),
		}
	}

	// 批量 INSERT 失败，触发兜底路径。
	mock.ExpectExec(`INSERT INTO usage_logs`).WillReturnError(fmt.Errorf("simulated batch error"))
	// 兜底 INSERT 每条都有 itemDelay > totalTimeout 的延迟；
	// 共用 ctx 过期后 db/sql 会取消 ExecContext，未必每条都到达 mock，故不调用 ExpectationsWereMet。
	for i := 0; i < n; i++ {
		mock.ExpectExec(`INSERT INTO usage_logs`).
			WillDelayFor(itemDelay).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}

	start := time.Now()
	repo.flushBestEffortBatch(db, reqs)
	elapsed := time.Since(start)

	// 总耗时应受 totalTimeout 约束，而非 n×itemDelay（串行）。
	// 旧代码（每条独享 10s）：delay=60ms < 10s，所有条目成功，此处断言 error 会失败。
	require.Less(t, elapsed, time.Duration(n)*itemDelay,
		"共用截止时间下总耗时 %v 不应达到串行上限 %v", elapsed, time.Duration(n)*itemDelay)

	// 所有条目都应收到错误（共用 ctx 超时）。
	// 旧代码下条目会成功（nil），此断言会失败，从而区分改动前后。
	for i, req := range reqs {
		select {
		case err := <-req.resultCh:
			require.Error(t, err, "req[%d] 应因共用 ctx 超时而收到错误", i)
		case <-time.After(2 * time.Second):
			t.Fatalf("等待 req[%d] 结果超时", i)
		}
	}
}

// TestFlushCreateBatch_FallbackAllItemsReceiveResults 验证 flushCreateBatch 兜底路径
// （requestID 为空的条目）并发执行后，所有条目都能收到写入结果，批处理器不会被串行阻塞。
func TestFlushCreateBatch_FallbackAllItemsReceiveResults(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	const n = 6 // 超过 fallbackConcurrency/2，验证信号量不会死锁
	reqs := make([]usageLogCreateRequest, n)
	for i := range reqs {
		ul := &service.UsageLog{
			UserID:    int64(i + 1),
			APIKeyID:  int64(i + 1),
			AccountID: int64(i + 1),
			// RequestID 为空 → 直接进入兜底路径，跳过批量 INSERT
			Model:     "gpt-5",
			CreatedAt: time.Now().UTC(),
		}
		reqs[i] = usageLogCreateRequest{
			log:      ul,
			prepared: prepareUsageLogInsert(ul),
			resultCh: make(chan usageLogCreateResult, 1),
		}
		// createSingle 使用 QueryRowContext（INSERT … RETURNING id, created_at）
		mock.ExpectQuery(`INSERT INTO usage_logs`).
			WillReturnRows(
				sqlmock.NewRows([]string{"id", "created_at"}).
					AddRow(int64(i+1), time.Now().UTC()),
			)
	}

	repo.flushCreateBatch(db, reqs)

	for i, req := range reqs {
		select {
		case res := <-req.resultCh:
			require.NoError(t, res.err, "req[%d] 兜底应写入成功", i)
		case <-time.After(5 * time.Second):
			t.Fatalf("等待 req[%d] 结果超时", i)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
