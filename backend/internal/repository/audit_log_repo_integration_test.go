//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAuditLogRepositoryIntegration_AppendListAndDeleteBefore(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE audit_logs RESTART IDENTITY")
	require.NoError(t, err)

	actorID := int64(42)
	oldCreatedAt := time.Now().UTC().Add(-48 * time.Hour)
	newCreatedAt := time.Now().UTC()
	repo := NewAuditLogRepository(integrationDB)
	inserted, err := repo.BatchInsert(ctx, []*service.AuditLog{
		{
			CreatedAt:        oldCreatedAt,
			ActorUserID:      &actorID,
			ActorEmail:       "admin@example.com",
			ActorRole:        "admin",
			AuthMethod:       service.AuditAuthMethodJWT,
			CredentialMasked: "Bearer sk-****1234",
			Action:           "admin.user.update",
			Method:           "POST",
			Path:             "/api/v1/admin/users/42",
			RequestID:        "audit-old",
			ClientIP:         "127.0.0.1",
			UserAgent:        "integration-test",
			RequestBody:      service.RedactAuditBody([]byte("{\"password\":\"secret\",\"name\":\"visible\"}"), "application/json"),
			StatusCode:       200,
			LatencyMs:        12,
			Extra:            map[string]any{"source": "integration"},
		},
		{
			CreatedAt:  newCreatedAt,
			ActorEmail: "other@example.com",
			Action:     "auth.login",
			Method:     "POST",
			Path:       "/api/v1/auth/login",
			StatusCode: 401,
		},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, inserted)

	var storedBody string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT request_body FROM audit_logs WHERE request_id = $1", "audit-old",
	).Scan(&storedBody))
	require.NotContains(t, storedBody, "secret")
	require.Contains(t, storedBody, "visible")

	result, err := repo.List(ctx, &service.AuditLogFilter{
		ActorUserID: &actorID,
		Page:        1,
		PageSize:    10,
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Total)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "audit-old", result.Logs[0].RequestID)
	require.Equal(t, map[string]any{"source": "integration"}, result.Logs[0].Extra)

	deleted, err := repo.DeleteBefore(ctx, newCreatedAt.Add(-time.Hour), 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)

	var remaining int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&remaining))
	require.Equal(t, 1, remaining)
}
