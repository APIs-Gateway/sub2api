package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var unpricedBillingColumns = []string{
	"group_id", "name", "platform", "model", "row_count",
	"input_tokens", "output_tokens", "cache_tokens", "image_count",
	"first_seen", "last_seen",
}

func TestOpsRepositoryListUnpricedBillingUsage_AggregatesAndExcludesMismatchAuditRows(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}

	start := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	first := start.Add(time.Hour)
	last := start.Add(5 * time.Hour)

	// 行级口径必须同时包含：created_at 范围打头、两个成本为 0、倍率大于 0、排除上游模型不一致的审计行。
	mock.ExpectQuery(
		regexp.QuoteMeta("FROM usage_logs ul")+`\s+WHERE ul\.created_at >= \$1 AND ul\.created_at < \$2`+
			`\s+AND ul\.total_cost = 0\s+AND ul\.actual_cost = 0\s+AND ul\.rate_multiplier > 0`+
			`\s+AND ul\.upstream_model_mismatch = FALSE`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(unpricedBillingColumns).
			AddRow(int64(16), "openai-main", "openai", "gpt-x", int64(7), int64(700), int64(0), int64(30), int64(0), first, last).
			AddRow(nil, "", "", "orphan-model", int64(2), int64(0), int64(5), int64(0), int64(3), first, first))

	rows, err := repo.ListUnpricedBillingUsage(context.Background(), &service.OpsUnpricedBillingFilter{
		StartTime: start,
		EndTime:   end,
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	require.NotNil(t, rows[0].GroupID)
	require.EqualValues(t, 16, *rows[0].GroupID)
	require.Equal(t, "openai-main", rows[0].GroupName)
	require.Equal(t, "openai", rows[0].Platform)
	require.Equal(t, "gpt-x", rows[0].Model)
	require.EqualValues(t, 7, rows[0].Rows)
	require.EqualValues(t, 700, rows[0].InputTokens)
	require.EqualValues(t, 30, rows[0].CacheTokens)
	require.Equal(t, first, rows[0].FirstSeen)
	require.Equal(t, last, rows[0].LastSeen)

	require.Nil(t, rows[1].GroupID, "没有分组的用量 group_id 为 nil")
	require.Equal(t, "orphan-model", rows[1].Model)
	require.EqualValues(t, 3, rows[1].ImageCount)
	require.EqualValues(t, 5, rows[1].OutputTokens)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryListUnpricedBillingUsage_AppliesPlatformAndGroupScope(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}

	start := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	groupID := int64(33)

	mock.ExpectQuery(`ul\.group_id = \$3.*COALESCE\(NULLIF\(g\.platform,''\), a\.platform\) = \$4`).
		WithArgs(start, end, groupID, "openai").
		WillReturnRows(sqlmock.NewRows(unpricedBillingColumns))

	rows, err := repo.ListUnpricedBillingUsage(context.Background(), &service.OpsUnpricedBillingFilter{
		StartTime: start,
		EndTime:   end,
		Platform:  " OpenAI ",
		GroupID:   &groupID,
	})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryListUnpricedBillingUsage_QueryIsBoundedAndOrdered(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`GROUP BY ul\.group_id, ul\.model.*ORDER BY h\.row_count DESC, h\.group_id ASC NULLS LAST, h\.model ASC\s+LIMIT 5000`).
		WithArgs(start, start.Add(time.Hour)).
		WillReturnRows(sqlmock.NewRows(unpricedBillingColumns))

	_, err := repo.ListUnpricedBillingUsage(context.Background(), &service.OpsUnpricedBillingFilter{
		StartTime: start,
		EndTime:   start.Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryListUnpricedBillingUsage_RejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	var nilRepo *opsRepository
	_, err := nilRepo.ListUnpricedBillingUsage(ctx, &service.OpsUnpricedBillingFilter{StartTime: now.Add(-time.Hour), EndTime: now})
	require.Error(t, err)

	_, err = (&opsRepository{}).ListUnpricedBillingUsage(ctx, &service.OpsUnpricedBillingFilter{StartTime: now.Add(-time.Hour), EndTime: now})
	require.Error(t, err)

	db, _ := newSQLMock(t)
	repo := &opsRepository{db: db}
	_, err = repo.ListUnpricedBillingUsage(ctx, nil)
	require.Error(t, err)
	_, err = repo.ListUnpricedBillingUsage(ctx, &service.OpsUnpricedBillingFilter{EndTime: now})
	require.Error(t, err)
	_, err = repo.ListUnpricedBillingUsage(ctx, &service.OpsUnpricedBillingFilter{StartTime: now})
	require.Error(t, err)
	_, err = repo.ListUnpricedBillingUsage(ctx, &service.OpsUnpricedBillingFilter{StartTime: now, EndTime: now.Add(-time.Hour)})
	require.Error(t, err)
}

func TestOpsRepositoryListUnpricedBillingUsage_PropagatesQueryAndScanErrors(t *testing.T) {
	start := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	filter := &service.OpsUnpricedBillingFilter{StartTime: start, EndTime: start.Add(time.Hour)}

	t.Run("query error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		boom := errors.New("db down")
		mock.ExpectQuery(`FROM usage_logs ul`).WillReturnError(boom)
		_, err := (&opsRepository{db: db}).ListUnpricedBillingUsage(context.Background(), filter)
		require.ErrorIs(t, err, boom)
	})

	t.Run("scan error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(`FROM usage_logs ul`).WillReturnRows(sqlmock.NewRows(unpricedBillingColumns).
			AddRow(int64(1), "g", "openai", "m", "not-a-number", int64(0), int64(0), int64(0), int64(0), start, start))
		_, err := (&opsRepository{db: db}).ListUnpricedBillingUsage(context.Background(), filter)
		require.Error(t, err)
	})

	t.Run("row iteration error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		boom := errors.New("connection reset")
		mock.ExpectQuery(`FROM usage_logs ul`).WillReturnRows(sqlmock.NewRows(unpricedBillingColumns).
			AddRow(int64(1), "g", "openai", "m", int64(1), int64(1), int64(0), int64(0), int64(0), start, start).
			RowError(0, boom))
		_, err := (&opsRepository{db: db}).ListUnpricedBillingUsage(context.Background(), filter)
		require.Error(t, err)
	})
}
