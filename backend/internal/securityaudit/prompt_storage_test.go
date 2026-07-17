package securityaudit

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newPromptStorageSQLMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	return db, mock
}

func promptJobRows(status string) *sqlmock.Rows {
	now := time.Unix(1700000000, 0).UTC()
	return sqlmock.NewRows([]string{
		"id", "request_id", "user_id", "username_snapshot", "user_email_snapshot", "api_key_id",
		"api_key_name_snapshot", "group_id", "group_name", "provider", "endpoint", "protocol", "model",
		"prompt_hash", "redacted_preview", "prompt_length", "message_count", "execution_mode",
		"config_version", "status", "attempts", "max_attempts", "claim_version", "next_attempt_at",
		"processing_started_at", "processed_at", "last_error_code", "last_error_message", "created_at", "updated_at",
	}).AddRow(
		int64(11), "request-11", int64(2), "user", "user@example.test", int64(3), "key", int64(4), "group",
		"openai", "/v1/chat/completions", "openai_chat", "guard-model", strings.Repeat("a", 64), "redacted",
		12, 2, string(ModeAsync), int64(7), status, 1, 3, int64(5), now, nil, nil, "", "", now, now,
	)
}

func promptEventRows() *sqlmock.Rows {
	now := time.Unix(1700000000, 0).UTC()
	return sqlmock.NewRows([]string{
		"id", "job_id", "request_id", "user_id", "username_snapshot", "user_email_snapshot", "api_key_id",
		"api_key_name_snapshot", "group_id", "group_name", "provider", "endpoint", "protocol", "model",
		"prompt_hash", "redacted_preview", "decision", "risk_level", "action", "categories", "matched_scanners",
		"scanner_scores", "scanner_evidence", "scanner_backend", "scanner_version", "guard_endpoint_id", "policy_id",
		"policy_version", "config_version", "chunk_total", "latency_ms", "created_at",
	}).AddRow(
		int64(21), int64(11), "request-11", int64(2), "user", "user@example.test", int64(3), "key", int64(4), "group",
		"openai", "/v1/chat/completions", "openai_chat", "guard-model", strings.Repeat("a", 64), "redacted",
		string(EventCritical), string(RiskCritical), string(ActionBlock), []byte(`["pii"]`), []byte(`["pii"]`),
		[]byte(`{"pii":1}`), []byte(`{"pii":"email"}`), "qwen3guard-openai", "guard-model", "guard-1", "priority",
		1, int64(7), 1, 3, now,
	)
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

func TestPromptStorageHelpersAndScanCodecs(t *testing.T) {
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

	db, mock := newPromptStorageSQLMock(t)
	mock.ExpectQuery("SELECT job").WillReturnRows(promptJobRows("processing"))
	job, err := scanJob(db.QueryRow("SELECT job"))
	require.NoError(t, err)
	require.Equal(t, int64(11), job.ID)
	require.Equal(t, int64(2), job.Snapshot.UserID)
	require.Equal(t, int64(4), *job.Snapshot.GroupID)
	require.Equal(t, ModeAsync, job.ExecutionMode)

	mock.ExpectQuery("SELECT event").WillReturnRows(promptEventRows())
	event, err := scanEvent(db.QueryRow("SELECT event"))
	require.NoError(t, err)
	require.Equal(t, int64(21), event.ID)
	require.Equal(t, []string{"pii"}, event.Categories)
	require.Len(t, event.IssueSummaries, 1)
}

func TestPromptStorageCreateAdmissionAndSimpleUpdates(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WithArgs(promptAuditAdmissionLockKey).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM prompt_audit_jobs").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO prompt_audit_jobs").WillReturnRows(promptJobRows("staging"))
	mock.ExpectCommit()
	job, err := repo.CreateStagingWithCapacity(ctx, storageSnapshot(), 7, 3, 10)
	require.NoError(t, err)
	require.Equal(t, int64(11), job.ID)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WithArgs(promptAuditAdmissionLockKey).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM prompt_audit_jobs").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(10))
	mock.ExpectRollback()
	_, err = repo.CreateStagingWithCapacity(ctx, storageSnapshot(), 7, 3, 10)
	require.ErrorIs(t, err, ErrQueueFull)

	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='queued'").WithArgs(int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.PublishQueued(ctx, 11))
	mock.ExpectExec("UPDATE prompt_audit_jobs[[:space:]]+SET status='failed'").WithArgs(int64(11), "payload_store_failed", "Prompt Audit operation failed").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.MarkStagingFailed(ctx, 11, "payload_store_failed", "raw secret must not persist"))
}

func TestPromptStorageClaimLeaseCompletionAndRetry(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	mock.ExpectQuery("WITH candidate AS").WillReturnRows(promptJobRows("processing"))
	job, claimed, err := repo.ClaimNextJob(ctx, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, int64(11), job.ID)
	mock.ExpectExec("UPDATE prompt_audit_jobs SET processing_started_at").WithArgs(int64(11), int64(5), now).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.RefreshLease(ctx, 11, 5, now))

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='done'").WithArgs(int64(11), int64(5)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO prompt_audit_events").WillReturnRows(promptEventRows())
	mock.ExpectCommit()
	job.ClaimVersion = 5
	event, err := repo.Complete(ctx, job, storageResult(), true)
	require.NoError(t, err)
	require.NotNil(t, event)

	next := now.Add(time.Minute)
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='retry'").WithArgs(int64(11), int64(5), next, "queue_full", "Prompt Audit queue is unavailable").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.Retry(ctx, 11, 5, next, "queue_full", "raw error"))
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='failed'").WithArgs(int64(11), int64(5), "worker_failed", "Prompt Audit operation failed").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.Fail(ctx, 11, 5, "worker_failed", "raw error"))
	mock.ExpectExec("WITH stale AS").WithArgs(now, now, 100).WillReturnResult(sqlmock.NewResult(0, 2))
	reclaimed, err := repo.ReclaimStale(ctx, now, now, 0)
	require.NoError(t, err)
	require.Equal(t, int64(2), reclaimed)
}

func TestPromptStorageQueueStatsAndBlockingRecord(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	mock.ExpectQuery("SELECT status, COUNT\\(\\*\\)").WillReturnRows(sqlmock.NewRows([]string{"status", "count"}).
		AddRow("staging", int64(1)).AddRow("queued", int64(2)).AddRow("processing", int64(3)).
		AddRow("retry", int64(4)).AddRow("done", int64(5)).AddRow("failed", int64(6)).AddRow("unknown", int64(99)))
	stats, err := repo.QueueStats(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(10), stats.Active)
	require.Equal(t, int64(5), stats.Done)
	require.Equal(t, int64(6), stats.Failed)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO prompt_audit_jobs").WillReturnRows(promptJobRows("done"))
	mock.ExpectQuery("INSERT INTO prompt_audit_events").WillReturnRows(promptEventRows())
	mock.ExpectCommit()
	event, err := repo.RecordBlocking(ctx, storageSnapshot(), 7, storageResult(), true)
	require.NoError(t, err)
	require.Equal(t, int64(21), event.ID)
	_, err = repo.RecordBlocking(ctx, storageSnapshot(), 7, nil, true)
	require.Error(t, err)
}

func TestPromptStoragePayloadStoreRoundTripAndValidation(t *testing.T) {
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

func TestPromptAuditMigrationContainsOnlyRedactedPersistenceColumns(t *testing.T) {
	contents, err := os.ReadFile("../../migrations/181_prompt_audit.sql")
	require.NoError(t, err)
	raw := strings.ToLower(string(contents))
	require.Contains(t, raw, "create table if not exists prompt_audit_jobs")
	require.Contains(t, raw, "create table if not exists prompt_audit_events")
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
