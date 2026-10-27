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

func TestAuditLogRepositoryIntegration_AdminAuditColumnsAndFilters(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE audit_logs RESTART IDENTITY")
	require.NoError(t, err)

	repo := NewAuditLogRepository(integrationDB)
	actorID := int64(1)
	tokenID := int64(7)
	now := time.Now().UTC()
	require.NoError(t, repo.Insert(ctx, &service.AuditLog{
		CreatedAt:   now,
		ActorUserID: &actorID,
		ActorLabel:  "token:ops-bot#7",
		AuthKind:    service.AuditAuthKindAdminToken,
		TokenID:     &tokenID,
		Method:      "POST",
		Route:       "/api/v1/admin/users/:id/balance",
		Path:        "/api/v1/admin/users/42/balance",
		TargetType:  "users",
		TargetID:    "42",
		Reason:      "补偿 2026-09 故障",
		StatusCode:  200,
		LatencyMs:   5,
		Before:      []byte(`{"balance":1}`),
	}))
	require.NoError(t, repo.Insert(ctx, &service.AuditLog{
		CreatedAt:  now,
		ActorLabel: "jwt:admin@example.com",
		AuthKind:   service.AuditAuthKindJWT,
		Method:     "DELETE",
		Route:      "/api/v1/admin/groups/:id",
		Path:       "/api/v1/admin/groups/3",
		TargetType: "groups",
		TargetID:   "3",
		StatusCode: 500,
	}))

	statusMin := 400
	cases := []struct {
		name   string
		filter service.AuditLogFilter
		want   string
	}{
		{"auth kind", service.AuditLogFilter{AuthKind: service.AuditAuthKindAdminToken}, "/api/v1/admin/users/:id/balance"},
		{"token id", service.AuditLogFilter{TokenID: &tokenID}, "/api/v1/admin/users/:id/balance"},
		{"route prefix", service.AuditLogFilter{RoutePrefix: "/api/v1/admin/groups"}, "/api/v1/admin/groups/:id"},
		{"target", service.AuditLogFilter{TargetType: "users", TargetID: "42"}, "/api/v1/admin/users/:id/balance"},
		{"status range", service.AuditLogFilter{StatusMin: &statusMin}, "/api/v1/admin/groups/:id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := tc.filter
			filter.Page, filter.PageSize = 1, 10
			result, err := repo.List(ctx, &filter)
			require.NoError(t, err)
			require.Equal(t, 1, result.Total)
			require.Equal(t, tc.want, result.Logs[0].Route)
		})
	}

	result, err := repo.List(ctx, &service.AuditLogFilter{TokenID: &tokenID, Page: 1, PageSize: 10})
	require.NoError(t, err)
	row := result.Logs[0]
	require.Equal(t, "token:ops-bot#7", row.ActorLabel)
	require.Equal(t, "补偿 2026-09 故障", row.Reason)
	require.NotNil(t, row.TokenID)
	require.Equal(t, tokenID, *row.TokenID)
	require.JSONEq(t, `{"balance":1}`, string(row.Before))
	require.Empty(t, row.After)
}
