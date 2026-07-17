package securityaudit

import (
	"context"
	"database/sql"
	"errors"
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

func TestPromptEventRepositoryListEventsNormalizesBoundsAndEmptyPages(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM prompt_audit_events e WHERE TRUE`).
		WillReturnRows(sqlmockRows("count", int64(0)))
	mock.ExpectQuery("SELECT").WithArgs(100, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	page, err := repo.ListEvents(context.Background(), EventFilter{}, 0, 200)
	require.NoError(t, err)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 100, page.PageSize)
	require.Zero(t, page.Pages)
	require.Empty(t, page.Items)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM prompt_audit_events e WHERE TRUE`).
		WillReturnRows(sqlmockRows("count", int64(0)))
	mock.ExpectQuery("SELECT").WithArgs(20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	page, err = repo.ListEvents(context.Background(), EventFilter{}, 1, 0)
	require.NoError(t, err)
	require.Equal(t, 20, page.PageSize)
}

func TestPromptEventRepositoryListEventsPropagatesQueryAndRowErrors(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM prompt_audit_events e WHERE TRUE`).
		WillReturnError(errors.New("count failed"))
	_, err := repo.ListEvents(context.Background(), EventFilter{}, 1, 20)
	require.EqualError(t, err, "count failed")

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM prompt_audit_events e WHERE TRUE`).
		WillReturnRows(sqlmockRows("count", int64(1)))
	mock.ExpectQuery("SELECT").WithArgs(20, 0).
		WillReturnError(errors.New("list failed"))
	_, err = repo.ListEvents(context.Background(), EventFilter{}, 1, 20)
	require.EqualError(t, err, "list failed")

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM prompt_audit_events e WHERE TRUE`).
		WillReturnRows(sqlmockRows("count", int64(1)))
	mock.ExpectQuery("SELECT").WithArgs(20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	_, err = repo.ListEvents(context.Background(), EventFilter{}, 1, 20)
	require.Error(t, err)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM prompt_audit_events e WHERE TRUE`).
		WillReturnRows(sqlmockRows("count", int64(1)))
	now := time.Unix(1700000000, 0).UTC()
	rows := promptEventRows().AddRow(
		int64(22), int64(12), "request-12", int64(2), "user", "user@example.test", int64(3), "key", int64(4), "group",
		"openai", "/v1/chat/completions", "openai_chat", "guard-model", strings.Repeat("b", 64), "redacted",
		string(EventCritical), string(RiskCritical), string(ActionBlock), []byte(`[]`), []byte(`[]`),
		[]byte(`{}`), []byte(`{}`), "qwen3guard-openai", "guard-model", "guard-1", "priority", 1, int64(7), 1, 3, now,
	).RowError(1, errors.New("row failed"))
	mock.ExpectQuery("SELECT").WithArgs(20, 0).
		WillReturnRows(rows)
	_, err = repo.ListEvents(context.Background(), EventFilter{}, 1, 20)
	require.EqualError(t, err, "row failed")
}

func TestPromptEventRepositoryRejectsUnavailableDatabase(t *testing.T) {
	var repo *PostgreSQLRepository
	_, err := repo.ListEvents(context.Background(), EventFilter{}, 1, 20)
	require.EqualError(t, err, "prompt audit database unavailable")
	_, err = repo.GetEvent(context.Background(), 1)
	require.EqualError(t, err, "prompt audit database unavailable")

	repo = &PostgreSQLRepository{}
	_, err = repo.ListEvents(context.Background(), EventFilter{}, 1, 20)
	require.EqualError(t, err, "prompt audit database unavailable")
	_, err = repo.GetEvent(context.Background(), 1)
	require.EqualError(t, err, "prompt audit database unavailable")
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
	groupID, userID, apiKeyID := int64(4), int64(2), int64(3)
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)
	where, args := buildEventWhere(EventFilter{
		Decision: " CRITICAL ", RiskLevel: " HIGH ", Endpoint: " /v1/chat ", GroupID: &groupID,
		UserID: &userID, APIKeyID: &apiKeyID, RequestID: " req-1 ",
		PromptHash: strings.Repeat("A", 64), Keyword: "  user  ", StartAt: &start, EndAt: &end,
	}, 1)
	require.Contains(t, where, "e.decision=$1")
	require.Contains(t, where, "e.risk_level=$2")
	require.Contains(t, where, "e.endpoint=$3")
	require.Contains(t, where, "e.group_id=$4")
	require.Contains(t, where, "e.user_id=$5")
	require.Contains(t, where, "e.api_key_id=$6")
	require.Contains(t, where, "e.request_id=$7")
	require.Contains(t, where, "e.prompt_hash=$8")
	require.Contains(t, where, "e.request_id ILIKE $9")
	require.Contains(t, where, "e.created_at >= $10")
	require.Contains(t, where, "e.created_at <= $11")
	require.Equal(t, []any{"critical", "high", "/v1/chat", int64(4), int64(2), int64(3), "req-1", strings.Repeat("a", 64), "%user%", start, end}, args)
	require.NotContains(t, where, "full_prompt")
}

func sqlmockRows(name string, value any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{name}).AddRow(value)
}
