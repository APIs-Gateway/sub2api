package repository

import (
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	require.Len(t, args, auditLogInsertColumnCount)
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
		"latency_ms", "extra", "actor_label", "auth_kind", "token_id", "route",
		"target_type", "target_id", "reason", "before", "after",
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
		"jwt:admin@example.com",
		service.AuditAuthKindJWT,
		nil,
		"/api/v1/admin/users/:id",
		"users",
		"1",
		"",
		nil,
		nil,
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
	require.Nil(t, result.Logs[0].TokenID)
	require.Equal(t, "jwt:admin@example.com", result.Logs[0].ActorLabel)
	require.Equal(t, service.AuditAuthKindJWT, result.Logs[0].AuthKind)
	require.Equal(t, "/api/v1/admin/users/:id", result.Logs[0].Route)
	require.Equal(t, "users", result.Logs[0].TargetType)
	require.Equal(t, "1", result.Logs[0].TargetID)
	require.Empty(t, result.Logs[0].Before)
	require.Empty(t, result.Logs[0].After)
	require.Equal(t, map[string]any{"source": "test"}, result.Logs[0].Extra)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditLogRepositoryBatchInsertAndDeleteBefore(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	args := make([]driver.Value, 2*auditLogInsertColumnCount)
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

func TestBuildAuditLogsWhere_AdminAuditFilters(t *testing.T) {
	tokenID := int64(9)
	statusMin, statusMax := 400, 499
	filter := &service.AuditLogFilter{
		AuthKind:    service.AuditAuthKindAdminToken,
		TokenID:     &tokenID,
		RoutePrefix: "/api/v1/admin/users_%",
		TargetType:  "users",
		TargetID:    "42",
		StatusMin:   &statusMin,
		StatusMax:   &statusMax,
	}

	where, args := buildAuditLogsWhere(filter)
	require.Contains(t, where, "l.auth_kind = $1")
	require.Contains(t, where, "l.token_id = $2")
	require.Contains(t, where, "l.route LIKE $3 ESCAPE '\\'")
	require.Contains(t, where, "l.target_type = $4")
	require.Contains(t, where, "l.target_id = $5")
	require.Contains(t, where, "l.status_code >= $6")
	require.Contains(t, where, "l.status_code <= $7")
	require.Equal(t, []any{
		service.AuditAuthKindAdminToken, tokenID, "/api/v1/admin/users\\_\\%%",
		"users", "42", 400, 499,
	}, args)
}

func TestBuildAuditLogInsertQuery_AdminAuditColumns(t *testing.T) {
	tokenID := int64(7)
	entry := &service.AuditLog{
		CreatedAt:  time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
		ActorLabel: "token:ops-bot#7",
		AuthKind:   service.AuditAuthKindAdminToken,
		TokenID:    &tokenID,
		Route:      "/api/v1/admin/users/:id/balance",
		TargetType: "users",
		TargetID:   "42",
		Reason:     "补偿 2026-09 故障",
	}

	query, args, count, err := buildAuditLogInsertQuery([]*service.AuditLog{entry})
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Contains(t, query, `actor_label, auth_kind, token_id, route, target_type, target_id, reason, "before", "after"`)
	require.Len(t, args, auditLogInsertColumnCount)
	require.Equal(t, "token:ops-bot#7", args[16])
	require.Equal(t, service.AuditAuthKindAdminToken, args[17])
	require.Equal(t, tokenID, args[18])
	require.Equal(t, "/api/v1/admin/users/:id/balance", args[19])
	require.Equal(t, "users", args[20])
	require.Equal(t, "42", args[21])
	require.Equal(t, "补偿 2026-09 故障", args[22])
	require.Nil(t, args[23], "before is left NULL")
	require.Nil(t, args[24], "after is left NULL")

	// Without a token the column is NULL, not zero.
	_, args, _, err = buildAuditLogInsertQuery([]*service.AuditLog{{Action: "x"}})
	require.NoError(t, err)
	require.Nil(t, args[18])
}

func TestBuildAuditLogInsertQuery_MakesValuesSafeForPostgres(t *testing.T) {
	entry := &service.AuditLog{
		Path:        "/api/v1/admin/x\x00y",
		UserAgent:   "agent-\xff-end",
		RequestBody: "{\"a\":\"b\x00\"}",
		Reason:      "r\x00s",
		Extra:       map[string]any{"panic": "boom\x00!", "nested": map[string]any{"k": "v\x00"}},
		Before:      []byte("not json"),
		After:       []byte(`{"ok":true}`),
	}

	_, args, count, err := buildAuditLogInsertQuery([]*service.AuditLog{entry})
	require.NoError(t, err)
	require.Equal(t, 1, count)

	for index, arg := range args {
		if text, ok := arg.(string); ok {
			require.NotContains(t, text, "\x00", "argument %d contains a NUL byte", index)
			require.True(t, utf8.ValidString(text), "argument %d is not valid UTF-8", index)
		}
	}
	require.Equal(t, "/api/v1/admin/xy", args[8])
	require.Equal(t, "rs", args[22])
	require.Equal(t, `{"nested":{"k":"v"},"panic":"boom!"}`, args[15])
	require.Nil(t, args[23], "invalid JSON snapshot is dropped, not sent to jsonb")
	require.Equal(t, `{"ok":true}`, args[24])
}
