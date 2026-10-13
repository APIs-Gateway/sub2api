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

// TestFlushBestEffortBatch_FallbackUsesIndependentCtx 验证 best-effort 批量写入失败后，
// 单条兜底使用独立 context 而非已超时的批次 ctx，确保兜底能成功写入。
func TestFlushBestEffortBatch_FallbackUsesIndependentCtx(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	log := &service.UsageLog{
		UserID:    1,
		APIKeyID:  2,
		AccountID: 3,
		RequestID: "req-be-fallback-ctx",
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

	// 批量 INSERT 失败，触发单条兜底路径
	mock.ExpectExec(`INSERT INTO usage_logs`).
		WillReturnError(fmt.Errorf("simulated batch insert error"))
	// 单条兜底 INSERT 成功
	mock.ExpectExec(`INSERT INTO usage_logs`).
		WillReturnResult(sqlmock.NewResult(1, 1))

	repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{req})

	select {
	case err := <-resultCh:
		require.NoError(t, err, "单条兜底应使用独立 ctx 并写入成功")
	case <-time.After(5 * time.Second):
		t.Fatal("等待兜底结果超时")
	}
	require.NoError(t, mock.ExpectationsWereMet())
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
