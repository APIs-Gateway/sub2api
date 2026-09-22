package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type groupCapacityGroupIDsMatcher struct {
	want string
}

func (m groupCapacityGroupIDsMatcher) Match(value driver.Value) bool {
	switch got := value.(type) {
	case string:
		return got == m.want
	case []byte:
		return string(got) == m.want
	default:
		return false
	}
}

type nonZeroTimeArgument struct{}

func (nonZeroTimeArgument) Match(value driver.Value) bool {
	timestamp, ok := value.(time.Time)
	return ok && !timestamp.IsZero()
}

func TestListSchedulableCapacityByGroupIDsProjectsCapacityFields(t *testing.T) {
	var capturedSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &capturedSQL}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	start := time.Date(2026, 9, 22, 1, 2, 3, 0, time.UTC)
	end := start.Add(time.Hour)
	mock.ExpectQuery("schedulable capacity projection").
		WithArgs(groupCapacityGroupIDsMatcher{want: "{20,10}"}, service.StatusActive, nonZeroTimeArgument{}).
		WillReturnRows(sqlmock.NewRows([]string{
		"group_id", "account_id", "concurrency", "extra", "session_window_start", "session_window_end", "session_window_status",
	}).
		AddRow(int64(10), int64(101), 4, `{"max_sessions":3,"base_rpm":11}`, start, end, "active").
		AddRow(int64(20), int64(102), 2, "{}", nil, nil, ""))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	rows, err := repo.ListSchedulableCapacityByGroupIDs(context.Background(), []int64{20, 10, 20, 0, -1})
	require.NoError(t, err)
	require.Equal(t, []service.GroupAccountCapacityRow{
		{
			GroupID:             10,
			AccountID:           101,
			Concurrency:         4,
			Extra:               map[string]any{"max_sessions": float64(3), "base_rpm": float64(11)},
			SessionWindowStart:  &start,
			SessionWindowEnd:    &end,
			SessionWindowStatus: "active",
		},
		{GroupID: 20, AccountID: 102, Concurrency: 2, Extra: map[string]any{}},
	}, rows)
	require.NoError(t, mock.ExpectationsWereMet())

	normalized := normalizeSQLWhitespace(capturedSQL)
	for _, clause := range []string{
		"ag.group_id = ANY($1)",
		"a.deleted_at IS NULL",
		"a.status = $2",
		"a.schedulable = TRUE",
		"a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= $3",
		"a.expires_at IS NULL OR a.expires_at > $3 OR a.auto_pause_on_expired = FALSE",
		"a.overload_until IS NULL OR a.overload_until <= $3",
		"a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= $3",
		"ORDER BY ag.group_id ASC, ag.priority ASC, a.priority ASC, a.id ASC",
	} {
		require.True(t, strings.Contains(normalized, clause), "missing capacity query clause %q in: %s", clause, normalized)
	}
}

func TestListSchedulableCapacityByGroupIDsSkipsQueryForNoValidGroups(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	rows, err := newAccountRepositoryWithSQL(nil, db, nil).ListSchedulableCapacityByGroupIDs(context.Background(), []int64{0, -1, 0})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListSchedulableCapacityByGroupIDsReturnsQueryAndDecodeErrors(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		queryErr := errors.New("database unavailable")
		mock.ExpectQuery("SELECT").WillReturnError(queryErr)

		_, err = newAccountRepositoryWithSQL(nil, db, nil).ListSchedulableCapacityByGroupIDs(context.Background(), []int64{10})
		require.ErrorIs(t, err, queryErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("invalid json", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "account_id", "concurrency", "extra", "session_window_start", "session_window_end", "session_window_status",
		}).AddRow(int64(10), int64(101), 4, "not-json", nil, nil, ""))

		_, err = newAccountRepositoryWithSQL(nil, db, nil).ListSchedulableCapacityByGroupIDs(context.Background(), []int64{10})
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("row iteration", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		rowErr := errors.New("cursor failed")
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "account_id", "concurrency", "extra", "session_window_start", "session_window_end", "session_window_status",
		}).AddRow(int64(10), int64(101), 4, "{}", nil, nil, "").RowError(0, rowErr))

		_, err = newAccountRepositoryWithSQL(nil, db, nil).ListSchedulableCapacityByGroupIDs(context.Background(), []int64{10})
		require.ErrorIs(t, err, rowErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestListActiveIDsUsesSQLProjectionAndReturnsErrors(t *testing.T) {
	t.Run("success and empty", func(t *testing.T) {
		var capturedSQL string
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &capturedSQL}))
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("active group IDs").
			WithArgs(service.StatusActive).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(4)).AddRow(int64(9)))

		ids, err := newGroupRepositoryWithSQL(nil, db).ListActiveIDs(context.Background())
		require.NoError(t, err)
		require.Equal(t, []int64{4, 9}, ids)
		require.NoError(t, mock.ExpectationsWereMet())
		require.Equal(t,
			"SELECT id FROM groups WHERE status = $1 AND deleted_at IS NULL ORDER BY sort_order ASC, id ASC",
			normalizeSQLWhitespace(capturedSQL),
		)
	})

	t.Run("query and row errors", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		queryErr := errors.New("groups query failed")
		mock.ExpectQuery("SELECT id").WillReturnError(queryErr)

		_, err = newGroupRepositoryWithSQL(nil, db).ListActiveIDs(context.Background())
		require.ErrorIs(t, err, queryErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("not-an-id"))

		_, err = newGroupRepositoryWithSQL(nil, db).ListActiveIDs(context.Background())
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("iteration error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		rowErr := errors.New("groups cursor failed")
		mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(4)).RowError(0, rowErr))

		_, err = newGroupRepositoryWithSQL(nil, db).ListActiveIDs(context.Background())
		require.ErrorIs(t, err, rowErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
