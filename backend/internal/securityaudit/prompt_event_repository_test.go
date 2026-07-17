package securityaudit

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestPromptEventRepositoryListEvents(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)
	groupID := int64(4)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM prompt_audit_events e WHERE TRUE AND e.decision=\$1 AND e.group_id=\$2 AND e.created_at >= \$3 AND e.created_at <= \$4`).
		WithArgs(string(EventCritical), groupID, start, end).
		WillReturnRows(sqlmockRows("count", int64(3)))
	mock.ExpectQuery("SELECT").
		WithArgs(string(EventCritical), groupID, start, end, 2, 2).
		WillReturnRows(promptEventRows())

	page, err := repo.ListEvents(context.Background(), EventFilter{
		Decision: string(EventCritical), GroupID: &groupID, StartAt: &start, EndAt: &end,
	}, 2, 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), page.Total)
	require.Equal(t, 2, page.Page)
	require.Equal(t, 2, page.PageSize)
	require.Equal(t, 2, page.Pages)
	require.Len(t, page.Items, 1)
	require.Empty(t, page.Items[0].Snapshot.ScanText)
}

func TestPromptEventRepositoryGetEventMapsNotFound(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)

	mock.ExpectQuery("SELECT").WithArgs(int64(21)).WillReturnRows(promptEventRows())
	event, err := repo.GetEvent(context.Background(), 21)
	require.NoError(t, err)
	require.Equal(t, int64(21), event.ID)
	require.Equal(t, "redacted", event.Snapshot.RedactedPreview)

	mock.ExpectQuery("SELECT").WithArgs(int64(99)).WillReturnError(sql.ErrNoRows)
	_, err = repo.GetEvent(context.Background(), 99)
	require.ErrorIs(t, err, ErrEventNotFound)
}

func TestBuildEventWhereCanonicalizesFiltersWithoutRawPrompt(t *testing.T) {
	where, args := buildEventWhere(EventFilter{
		Decision: " CRITICAL ", PromptHash: strings.Repeat("A", 64), Keyword: "  user  ",
	}, 1)
	require.Contains(t, where, "e.decision=$1")
	require.Contains(t, where, "e.prompt_hash=$2")
	require.Contains(t, where, "e.request_id ILIKE $3")
	require.Equal(t, []any{"critical", strings.Repeat("a", 64), "%user%"}, args)
	require.NotContains(t, where, "full_prompt")
}

func sqlmockRows(name string, value any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{name}).AddRow(value)
}
