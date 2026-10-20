package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// TestUsageLogRepositoryListWithFiltersByRequestID covers upstream #4873 /
// local item16: admin usage log listing can filter by an exact request id.
func TestUsageLogRepositoryListWithFiltersByRequestID(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	filters := usagestats.UsageLogFilters{
		RequestID: "req-abc-123",
	}

	// RequestID is treated as a strong-selectivity filter (see
	// shouldUseFastUsageLogTotal), so ListWithFilters takes the exact-count
	// path: COUNT(*) followed by the paginated SELECT, both scoped by
	// request_id.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM usage_logs WHERE request_id = \$1`).
		WithArgs(filters.RequestID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT .* FROM usage_logs WHERE request_id = \$1 ORDER BY id DESC LIMIT \$2 OFFSET \$3`).
		WithArgs(filters.RequestID, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	logs, page, err := repo.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.Empty(t, logs)
	require.NotNil(t, page)
	require.Equal(t, int64(1), page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryListWithFiltersByRequestIDAndUser covers combining
// request_id with another filter, confirming the placeholder numbering
// ($1, $2, ...) still lines up when multiple conditions are present.
func TestUsageLogRepositoryListWithFiltersByRequestIDAndUser(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	filters := usagestats.UsageLogFilters{
		UserID:    7,
		RequestID: "req-xyz-999",
	}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM usage_logs WHERE user_id = \$1 AND request_id = \$2`).
		WithArgs(filters.UserID, filters.RequestID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`SELECT .* FROM usage_logs WHERE user_id = \$1 AND request_id = \$2 ORDER BY id DESC LIMIT \$3 OFFSET \$4`).
		WithArgs(filters.UserID, filters.RequestID, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	logs, page, err := repo.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.Empty(t, logs)
	require.NotNil(t, page)
	require.Equal(t, int64(0), page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestShouldUseFastUsageLogTotal_RequestIDIsStronglySelective(t *testing.T) {
	require.False(t, shouldUseFastUsageLogTotal(usagestats.UsageLogFilters{RequestID: "req-1"}),
		"a request_id filter should use the exact-count path, like user/api-key/account filters")
	require.True(t, shouldUseFastUsageLogTotal(usagestats.UsageLogFilters{}),
		"an unfiltered query should still use the fast-count path")
}
