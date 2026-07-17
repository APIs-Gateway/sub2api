//go:build integration

package securityaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func openPromptStorageIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PROMPT_AUDIT_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PROMPT_AUDIT_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx))
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS users (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE IF NOT EXISTS groups (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE IF NOT EXISTS api_keys (id BIGSERIAL PRIMARY KEY);
	`)
	require.NoError(t, err)
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "181_prompt_audit.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `TRUNCATE TABLE prompt_audit_events, prompt_audit_jobs, api_keys, users, groups RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestPromptAuditStorageRealDatabasePrivacyAndQueueLifecycle(t *testing.T) {
	db := openPromptStorageIntegrationDB(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	snapshot := storageSnapshot()
	snapshot.ScanText = "raw prompt must stay outside PostgreSQL"
	event, err := repo.RecordBlocking(ctx, snapshot, 7, storageResult(), true)
	require.NoError(t, err)
	require.NotNil(t, event)
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	require.NotContains(t, string(raw), snapshot.ScanText)
	var stored string
	require.NoError(t, db.QueryRow(`SELECT row_to_json(e)::text FROM prompt_audit_events e WHERE id=$1`, event.ID).Scan(&stored))
	require.NotContains(t, stored, snapshot.ScanText)

	job, err := repo.CreateStagingWithCapacity(ctx, snapshot, 7, 2, 10)
	require.NoError(t, err)
	require.NoError(t, repo.PublishQueued(ctx, job.ID))
	claimed, ok, err := repo.ClaimNextJob(ctx, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.RefreshLease(ctx, claimed.ID, claimed.ClaimVersion, time.Now()))
	_, err = repo.Complete(ctx, claimed, storageResult(), true)
	require.NoError(t, err)
	stats, err := repo.QueueStats(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.Done)
}
