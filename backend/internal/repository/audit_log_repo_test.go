package repository

import (
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildAuditLogsWhere_UsesParameterizedEscapedFilters(t *testing.T) {
	userID := int64(42)
	success := false
	filter := &service.AuditLogFilter{
		ActorUserID: &userID,
		ActorEmail:  "audit%example",
		Action:      "admin_user_",
		Method:      "post",
		Success:     &success,
		Query:       "50%_",
	}

	where, args := buildAuditLogsWhere(filter)
	require.Contains(t, where, "l.actor_user_id = $1")
	require.Contains(t, where, "l.actor_email ILIKE $2 ESCAPE '\\'")
	require.Contains(t, where, "l.action ILIKE $3 ESCAPE '\\'")
	require.Contains(t, where, "l.method = $4")
	require.Contains(t, where, "l.status_code >= 400")
	require.Contains(t, where, "l.path ILIKE $5 ESCAPE '\\'")
	require.Len(t, args, 5)
	require.Equal(t, userID, args[0])
	require.Equal(t, "%audit\\%example%", args[1])
	require.Equal(t, "%admin\\_user\\_%", args[2])
	require.Equal(t, "POST", args[3])
	require.Equal(t, "%50\\%\\_%", args[4])
}

func TestBuildAuditLogInsertQuery_RejectsInvalidExtraAndSkipsNil(t *testing.T) {
	userID := int64(7)
	createdAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	entry := &service.AuditLog{
		CreatedAt:   createdAt,
		ActorUserID: &userID,
		ActorEmail:  strings.Repeat("a", 300),
		ActorRole:   strings.Repeat("管理员", 40),
		Extra:       map[string]any{"source": "test"},
	}

	query, args, count, err := buildAuditLogInsertQuery([]*service.AuditLog{nil, entry})
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Contains(t, query, "INSERT INTO audit_logs")
	require.Len(t, args, 16)
	require.Equal(t, createdAt, args[0])
	require.Equal(t, userID, args[1])
	actorEmail, ok := args[2].(string)
	if !ok {
		t.Fatalf("expected actor email argument to be a string, got %T", args[2])
	}
	if got := len([]rune(actorEmail)); got != 255 {
		t.Fatalf("expected actor email to be truncated to 255 runes, got %d", got)
	}
	actorRole, ok := args[3].(string)
	if !ok {
		t.Fatalf("expected actor role argument to be a string, got %T", args[3])
	}
	if got := len([]rune(actorRole)); got != 32 {
		t.Fatalf("expected actor role to be truncated to 32 runes, got %d", got)
	}
	extraJSON, ok := args[15].(string)
	if !ok {
		t.Fatalf("expected extra JSON argument to be a string, got %T", args[15])
	}
	if extraJSON != "{\"source\":\"test\"}" {
		t.Fatalf("unexpected extra JSON: %s", extraJSON)
	}

	_, _, _, err = buildAuditLogInsertQuery([]*service.AuditLog{{
		Extra: map[string]any{"bad": func() {}},
	}})
	require.Error(t, err)
}

func TestAuditLogRepositoryList_ClampsPageAndScansNullableActor(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM audit_logs l WHERE 1=1")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	rows := sqlmock.NewRows([]string{
		"id", "created_at", "actor_user_id", "actor_email", "actor_role",
		"auth_method", "credential_masked", "action", "method", "path",
		"request_id", "client_ip", "user_agent", "request_body", "status_code",
		"latency_ms", "extra",
	}).AddRow(
		int64(1),
		time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		nil,
		"admin@example.com",
		"admin",
		service.AuditAuthMethodJWT,
		"Bearer sk-****1234",
		"admin.user.update",
		"POST",
		"/api/v1/admin/users/1",
		"req-1",
		"127.0.0.1",
		"test",
		"{\"name\":\"visible\"}",
		200,
		int64(12),
		"{\"source\":\"test\"}",
	)
	mock.ExpectQuery("(?s)SELECT .* FROM audit_logs l WHERE 1=1 ORDER BY l.created_at DESC, l.id DESC OFFSET \\$1 LIMIT \\$2").
		WithArgs(200, 200).
		WillReturnRows(rows)

	repo := &auditLogRepository{db: db}
	result, err := repo.List(t.Context(), &service.AuditLogFilter{Page: 2, PageSize: 500})
	require.NoError(t, err)
	require.Equal(t, 1, result.Total)
	require.Equal(t, 2, result.Page)
	require.Equal(t, service.AuditLogMaxPageSize, result.PageSize)
	require.Len(t, result.Logs, 1)
	require.Nil(t, result.Logs[0].ActorUserID)
	require.Equal(t, map[string]any{"source": "test"}, result.Logs[0].Extra)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditLogRepositoryBatchInsertAndDeleteBefore(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	args := make([]driver.Value, 32)
	for index := range args {
		args[index] = sqlmock.AnyArg()
	}
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(args...).
		WillReturnResult(sqlmock.NewResult(1, 2))

	repo := &auditLogRepository{db: db}
	inserted, err := repo.BatchInsert(t.Context(), []*service.AuditLog{
		{Action: "one"},
		{Action: "two"},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, inserted)

	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec("DELETE FROM audit_logs").
		WithArgs(cutoff, 100).
		WillReturnResult(sqlmock.NewResult(0, 3))
	deleted, err := repo.DeleteBefore(t.Context(), cutoff, 100)
	require.NoError(t, err)
	require.EqualValues(t, 3, deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}
