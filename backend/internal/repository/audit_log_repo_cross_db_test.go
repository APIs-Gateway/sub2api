//go:build unit

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestAuditLogRepositorySQLiteSchemaAndQueries(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:audit_log_cross_db?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	migration, err := migrations.FS.ReadFile("178_audit_logs.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)

	repo := NewAuditLogRepository(db)
	createdAt := time.Now().UTC().Add(-time.Hour)
	inserted, err := repo.BatchInsert(ctx, []*service.AuditLog{{
		CreatedAt:  createdAt,
		ActorEmail: "admin@example.com",
		Action:     "admin.user.update",
		Method:     "POST",
		Path:       "/api/v1/admin/users/1",
		Extra:      map[string]any{"source": "sqlite"},
	}})
	require.NoError(t, err)
	require.EqualValues(t, 1, inserted)

	result, err := repo.List(ctx, &service.AuditLogFilter{Query: "ADMIN.USER", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 1, result.Total)
	require.Equal(t, map[string]any{"source": "sqlite"}, result.Logs[0].Extra)

	deleted, err := repo.DeleteBefore(ctx, time.Now().UTC(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
}

func TestAuditLogQuestionDialectUsesPortableSQL(t *testing.T) {
	where, args := buildAuditLogsWhereForDialect(&service.AuditLogFilter{Query: "admin%user"}, auditLogQuestionDialect)
	require.NotContains(t, where, "ILIKE")
	require.NotContains(t, where, "$1")
	require.Len(t, args, 3)

	query, args, count, err := buildAuditLogInsertQueryWithIDs(
		[]*service.AuditLog{{Action: "auth.login"}},
		[]int64{1},
		auditLogQuestionDialect,
	)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Len(t, args, 17)
	require.Equal(t, 17, strings.Count(query, "?"))
	require.NotContains(t, query, "$1")
}
