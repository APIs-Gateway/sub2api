package securityaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newPromptStorageSQLite(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA foreign_keys = ON`)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE users (id BIGINT PRIMARY KEY);
		CREATE TABLE groups (id BIGINT PRIMARY KEY);
		CREATE TABLE api_keys (id BIGINT PRIMARY KEY);
	`)
	require.NoError(t, err)
	applyPromptAuditMigration(t, db)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func applyPromptAuditMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	contents, err := os.ReadFile("../../migrations/181_prompt_audit.sql")
	require.NoError(t, err)
	for _, statement := range strings.Split(string(contents), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" || strings.HasPrefix(statement, "--") && !strings.Contains(statement, "\nCREATE") && !strings.Contains(statement, "\nINSERT") {
			continue
		}
		_, err := db.Exec(statement)
		require.NoError(t, err, statement)
	}
}

func storageSnapshot() PromptSnapshot {
	return PromptSnapshot{RequestID: "request-11", UsernameSnapshot: "user", UserEmailSnapshot: "user@example.test",
		APIKeyNameSnapshot: "key", GroupName: "group", Provider: "openai", Endpoint: "/v1/chat/completions",
		Protocol: "openai_chat", Model: "guard-model", PromptHash: strings.Repeat("a", 64), RedactedPreview: "redacted",
		PromptLength: 12, MessageCount: 2}
}

func storageResult() *NormalizedResult {
	return &NormalizedResult{Decision: EventCritical, RiskLevel: RiskCritical, Action: ActionBlock,
		Categories: []string{"pii"}, MatchedScanners: []string{"pii"}, ScannerScores: map[string]float64{"pii": 1},
		ScannerEvidence: map[string]string{"pii": "email"}, ScannerBackend: "qwen3guard-openai", ScannerVersion: "guard-model",
		GuardEndpointID: "guard-1", PolicyID: "priority", PolicyVersion: 1, ChunkTotal: 1, LatencyMS: 3}
}

func TestPromptAuditSQLiteRepositoryLifecycleAndPrivacy(t *testing.T) {
	db := newPromptStorageSQLite(t)
	repo := NewSQLRepository(db)
	ctx := context.Background()
	snapshot := storageSnapshot()
	snapshot.ScanText = "raw prompt must stay outside durable storage"

	event, err := repo.RecordBlocking(ctx, snapshot, 7, storageResult(), true)
	require.NoError(t, err)
	require.NotNil(t, event)
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	require.NotContains(t, string(raw), snapshot.ScanText)
	var stored string
	require.NoError(t, db.QueryRow(`SELECT scanner_evidence FROM prompt_audit_events WHERE id = ?`, event.ID).Scan(&stored))
	require.NotContains(t, stored, snapshot.ScanText)

	job, err := repo.CreateStagingWithCapacity(ctx, snapshot, 7, 2, 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), job.ID)
	require.NoError(t, repo.PublishQueued(ctx, job.ID))
	claimed, ok, err := repo.ClaimNextJob(ctx, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, job.ID, claimed.ID)
	require.NoError(t, repo.RefreshLease(ctx, claimed.ID, claimed.ClaimVersion, time.Now()))
	completed, err := repo.Complete(ctx, claimed, storageResult(), true)
	require.NoError(t, err)
	require.NotNil(t, completed)
	stats, err := repo.QueueStats(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.Done)
	require.Zero(t, stats.Active)
}

func TestPromptAuditSQLiteRepositoryCapacityLeaseAndReclaim(t *testing.T) {
	db := newPromptStorageSQLite(t)
	repo := NewSQLRepository(db)
	ctx := context.Background()

	job, err := repo.CreateStagingWithCapacity(ctx, storageSnapshot(), 7, 2, 1)
	require.NoError(t, err)
	_, err = repo.CreateStagingWithCapacity(ctx, storageSnapshot(), 7, 2, 1)
	require.ErrorIs(t, err, ErrQueueFull)
	require.NoError(t, repo.PublishQueued(ctx, job.ID))
	claimed, ok, err := repo.ClaimNextJob(ctx, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.Retry(ctx, claimed.ID, claimed.ClaimVersion, time.Now().Add(-time.Second), "queue_full", "secret"))
	claimed, ok, err = repo.ClaimNextJob(ctx, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.Fail(ctx, claimed.ID, claimed.ClaimVersion, "worker_failed", "secret"))
	require.ErrorIs(t, repo.RefreshLease(ctx, claimed.ID, claimed.ClaimVersion, time.Now()), ErrLeaseLost)

	staging, err := repo.CreateStagingWithCapacity(ctx, storageSnapshot(), 7, 1, 10)
	require.NoError(t, err)
	reclaimed, err := repo.ReclaimStale(ctx, time.Now().Add(time.Second), time.Now().Add(time.Second), 100)
	require.NoError(t, err)
	require.Equal(t, int64(1), reclaimed)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM prompt_audit_jobs WHERE id = ?`, staging.ID).Scan(&status))
	require.Equal(t, "failed", status)
}

func TestPromptAuditMigrationIsPortableAndIdempotent(t *testing.T) {
	db := newPromptStorageSQLite(t)
	applyPromptAuditMigration(t, db)
	contents, err := os.ReadFile("../../migrations/181_prompt_audit.sql")
	require.NoError(t, err)
	raw := strings.ToLower(string(contents))
	for _, forbidden := range []string{"bigserial", "timestamptz", "jsonb", "::json", "pg_", "skip locked", "returning"} {
		require.NotContains(t, raw, forbidden)
	}
	for _, required := range []string{"prompt_audit_sequences", "categories               text", "matched_scanners         text"} {
		require.Contains(t, raw, required)
	}
	nonCommentSQL := make([]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			nonCommentSQL = append(nonCommentSQL, line)
		}
	}
	for _, forbidden := range []string{"raw_prompt", "raw_request", "payload", "token", "authorization", "credential", "ciphertext"} {
		require.NotContains(t, strings.Join(nonCommentSQL, "\n"), forbidden)
	}
}

func TestPromptAuditRepositoryUsesQuestionMarkDialectForSQLite(t *testing.T) {
	db := newPromptStorageSQLite(t)
	repo := NewSQLRepository(db)
	require.Equal(t, promptAuditQuestionMark, repo.dialect)
	require.Equal(t, "?", repo.placeholder(1))
	require.Equal(t, "$2", (&PostgreSQLRepository{dialect: promptAuditPostgreSQL}).placeholder(2))
}

func TestPromptStorageHelpersAndPayloadStore(t *testing.T) {
	require.Equal(t, int64(0), nullableInt64Value(sql.NullInt64{}))
	require.Equal(t, int64(7), nullableInt64Value(sql.NullInt64{Int64: 7, Valid: true}))
	require.Nil(t, nullableInt64Ptr(sql.NullInt64{}))
	ptr := nullableInt64Ptr(sql.NullInt64{Int64: 7, Valid: true})
	require.NotNil(t, ptr)
	require.Equal(t, int64(7), *ptr)
	require.Nil(t, nullableID(0))
	require.Equal(t, int64(7), nullableID(7))
	require.Contains(t, jobColumns("j"), "j.id")
	require.Contains(t, eventColumns("e"), "e.id")

	require.Equal(t, "redacted_error", stableErrorCode("raw response; secret"))
	require.Equal(t, "unknown_error", stableErrorCode(""))
	code, message := sanitizeStoredError("queue_full")
	require.Equal(t, "queue_full", code)
	require.Equal(t, "Prompt Audit queue is unavailable", message)

	require.Equal(t, PayloadKeyPrefix+"7", payloadKey(7))
	nilStore := NewRedisPayloadStore(nil)
	require.Error(t, nilStore.Set(context.Background(), 1, "text", time.Second))
	require.Error(t, nilStore.Set(context.Background(), 0, "text", time.Second))
	_, err := nilStore.Get(context.Background(), 1)
	require.Error(t, err)
	require.Error(t, nilStore.Delete(context.Background(), 1))
	require.Error(t, nilStore.Ping(context.Background()))

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := NewRedisPayloadStore(client)
	require.NoError(t, store.Set(context.Background(), 7, "scan text", 2*DefaultPayloadTTL))
	value, err := store.Get(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "scan text", value)
	ttl, err := client.TTL(context.Background(), payloadKey(7)).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))
	require.LessOrEqual(t, ttl, DefaultPayloadTTL)
	require.NoError(t, store.Ping(context.Background()))
	require.NoError(t, store.Delete(context.Background(), 7))
	_, err = store.Get(context.Background(), 7)
	require.ErrorIs(t, err, redis.Nil)
	require.True(t, errors.Is(err, redis.Nil))
}
