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

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
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
	return sqlmock.NewRows([]string{"id", "request_id", "user_id", "username_snapshot", "user_email_snapshot", "api_key_id", "api_key_name_snapshot", "group_id", "group_name", "provider", "endpoint", "protocol", "model", "prompt_hash", "redacted_preview", "prompt_length", "message_count", "execution_mode", "config_version", "status", "attempts", "max_attempts", "claim_version", "next_attempt_at", "processing_started_at", "processed_at", "last_error_code", "last_error_message", "created_at", "updated_at"}).AddRow(11, "request-11", 2, "user", "user@example.test", 3, "key", 4, "group", "openai", "/v1/chat/completions", "openai_chat", "guard-model", strings.Repeat("a", 64), "redacted", 12, 2, string(ModeAsync), 7, status, 1, 3, 5, now, nil, nil, "", "", now, now)
}

func promptEventRows() *sqlmock.Rows {
	now := time.Unix(1700000000, 0).UTC()
	return sqlmock.NewRows([]string{"id", "job_id", "request_id", "user_id", "username_snapshot", "user_email_snapshot", "api_key_id", "api_key_name_snapshot", "group_id", "group_name", "provider", "endpoint", "protocol", "model", "prompt_hash", "redacted_preview", "decision", "risk_level", "action", "categories", "matched_scanners", "scanner_scores", "scanner_evidence", "scanner_backend", "scanner_version", "guard_endpoint_id", "policy_id", "policy_version", "config_version", "chunk_total", "latency_ms", "created_at"}).AddRow(21, 11, "request-11", 2, "user", "user@example.test", 3, "key", 4, "group", "openai", "/v1/chat/completions", "openai_chat", "guard-model", strings.Repeat("a", 64), "redacted", string(EventCritical), string(RiskCritical), string(ActionBlock), []byte(`["pii"]`), []byte(`["pii"]`), []byte(`{"pii":1}`), []byte(`{"pii":"email"}`), "qwen3guard-openai", "guard-model", "guard-1", "priority", 1, 7, 1, 3, now)
}

// promptEventDetailRows mirrors promptEventRows but adds the full_prompt
// column that eventDetailColumns/scanEvent(row, true) select only for the
// single-event detail read (PostgreSQLRepository.GetEvent). fullPrompt is
// typically empty (store_full_prompts disabled) or a canary string
// (store_full_prompts enabled) depending on what the test is asserting.
func promptEventDetailRows(fullPrompt string) *sqlmock.Rows {
	now := time.Unix(1700000000, 0).UTC()
	return sqlmock.NewRows([]string{"id", "job_id", "request_id", "user_id", "username_snapshot", "user_email_snapshot", "api_key_id", "api_key_name_snapshot", "group_id", "group_name", "provider", "endpoint", "protocol", "model", "prompt_hash", "redacted_preview", "decision", "risk_level", "action", "categories", "matched_scanners", "scanner_scores", "scanner_evidence", "scanner_backend", "scanner_version", "guard_endpoint_id", "policy_id", "policy_version", "config_version", "chunk_total", "latency_ms", "created_at", "full_prompt"}).AddRow(21, 11, "request-11", 2, "user", "user@example.test", 3, "key", 4, "group", "openai", "/v1/chat/completions", "openai_chat", "guard-model", strings.Repeat("a", 64), "redacted", string(EventCritical), string(RiskCritical), string(ActionBlock), []byte(`["pii"]`), []byte(`["pii"]`), []byte(`{"pii":1}`), []byte(`{"pii":"email"}`), "qwen3guard-openai", "guard-model", "guard-1", "priority", 1, 7, 1, 3, now, fullPrompt)
}

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

// promptAuditMigrationFiles lists, in apply order, every migration these
// tests replay directly against a bare test database. Extend this list
// whenever a new migration touches prompt_audit_jobs/prompt_audit_events.
var promptAuditMigrationFiles = []string{
	"181_prompt_audit.sql",
	"189_prompt_audit_full_prompt.sql",
}

func applyPromptAuditMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range promptAuditMigrationFiles {
		applyPromptAuditMigrationFile(t, db, name)
	}
}

// applyPromptAuditMigrationFile replays one migration file's statements
// directly, bypassing the schema_migrations checksum tracking that gives the
// real migration runner (backend/internal/repository/migrations_runner.go)
// its idempotency in production. Tests call this more than once per file
// (see TestPromptAuditStoragePostgreSQLIntegration) to prove each migration
// tolerates a retried/interrupted deployment, so "already applied" errors
// from a bare ADD COLUMN (used instead of the non-portable
// ADD COLUMN IF NOT EXISTS - see 189_prompt_audit_full_prompt.sql) are
// swallowed here rather than in the migration itself.
func applyPromptAuditMigrationFile(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	contents, err := os.ReadFile("../../migrations/" + name)
	require.NoError(t, err)
	for _, statement := range strings.Split(string(contents), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" || strings.HasPrefix(statement, "--") && !strings.Contains(statement, "\nCREATE") && !strings.Contains(statement, "\nINSERT") && !strings.Contains(statement, "\nALTER") {
			continue
		}
		if _, err := db.Exec(statement); err != nil && !isAlreadyAppliedMigrationError(err) {
			require.NoError(t, err, statement)
		}
	}
}

// isAlreadyAppliedMigrationError recognizes the dialect-specific "column
// already exists" errors a bare ADD COLUMN raises when replayed a second
// time outside production's schema_migrations tracking.
func isAlreadyAppliedMigrationError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate column") || strings.Contains(message, "already exists")
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

	event, err := repo.RecordBlocking(ctx, snapshot, 7, storageResult(), true, false)
	require.NoError(t, err)
	require.NotNil(t, event)
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	require.NotContains(t, string(raw), snapshot.ScanText)
	var stored string
	require.NoError(t, db.QueryRow(`SELECT scanner_evidence FROM prompt_audit_events WHERE id = ?`, event.ID).Scan(&stored))
	require.NotContains(t, stored, snapshot.ScanText)
	var storedFullPrompt string
	require.NoError(t, db.QueryRow(`SELECT full_prompt FROM prompt_audit_events WHERE id = ?`, event.ID).Scan(&storedFullPrompt))
	require.Empty(t, storedFullPrompt, "store_full_prompts defaults to false, so full_prompt must stay redacted-empty")

	job, err := repo.CreateStagingWithCapacity(ctx, snapshot, 7, 2, 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), job.ID)
	require.NoError(t, repo.PublishQueued(ctx, job.ID))
	claimed, ok, err := repo.ClaimNextJob(ctx, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, job.ID, claimed.ID)
	require.NoError(t, repo.RefreshLease(ctx, claimed.ID, claimed.ClaimVersion, time.Now()))
	completed, err := repo.Complete(ctx, claimed, storageResult(), true, false)
	require.NoError(t, err)
	require.NotNil(t, completed)
	require.NoError(t, db.QueryRow(`SELECT full_prompt FROM prompt_audit_events WHERE id = ?`, completed.ID).Scan(&storedFullPrompt))
	require.Empty(t, storedFullPrompt, "async completion must also leave full_prompt redacted-empty when disabled")
	stats, err := repo.QueueStats(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.Done)
	require.Zero(t, stats.Active)
}

// TestPromptAuditSQLiteRepositoryStoreFullPromptsCombinations exercises the
// four combinations issue #585 calls out explicitly: store_full_prompts is a
// gate on the full_prompt column only, independent of store_pass_events'
// existing gate on whether the row is written at all, for both the blocking
// (RecordBlocking) and async (Complete) paths.
func TestPromptAuditSQLiteRepositoryStoreFullPromptsCombinations(t *testing.T) {
	db := newPromptStorageSQLite(t)
	repo := NewSQLRepository(db)
	ctx := context.Background()

	passSnapshot := storageSnapshot()
	passSnapshot.RequestID = "request-pass-full-prompt"
	passSnapshot.FullPrompt = "PROMPT_CANARY_pass_event_full_prompt"
	passResult := &NormalizedResult{Decision: EventPass}

	// pass event, store_pass_events=false, store_full_prompts=true: the row
	// is still skipped entirely, because store_full_prompts only controls
	// what a stored row contains, not whether pass events get stored.
	skipped, err := repo.RecordBlocking(ctx, passSnapshot, 7, passResult, false, true)
	require.NoError(t, err)
	require.Nil(t, skipped)

	// pass event, store_pass_events=true, store_full_prompts=true: stored,
	// and full_prompt is populated on the durable row (but not on the
	// returned object, nor via ListEvents).
	passStored, err := repo.RecordBlocking(ctx, passSnapshot, 7, passResult, true, true)
	require.NoError(t, err)
	require.NotNil(t, passStored)
	raw, err := json.Marshal(passStored)
	require.NoError(t, err)
	require.NotContains(t, string(raw), passSnapshot.FullPrompt)
	var storedPassFullPrompt string
	require.NoError(t, db.QueryRow(`SELECT full_prompt FROM prompt_audit_events WHERE id = ?`, passStored.ID).Scan(&storedPassFullPrompt))
	require.Equal(t, passSnapshot.FullPrompt, storedPassFullPrompt)
	passDetail, err := repo.GetEvent(ctx, passStored.ID)
	require.NoError(t, err)
	require.Equal(t, passSnapshot.FullPrompt, passDetail.Snapshot.FullPrompt)
	passPage, err := repo.ListEvents(ctx, EventFilter{RequestID: passSnapshot.RequestID}, 1, 20)
	require.NoError(t, err)
	require.Len(t, passPage.Items, 1)
	require.Empty(t, passPage.Items[0].Snapshot.FullPrompt)

	// risk event, store_pass_events=false, store_full_prompts=true, via the
	// async CreateStagingWithCapacity -> Complete path: always stored
	// (risk events ignore store_pass_events), and full_prompt is populated
	// even though the job row itself never carried it.
	riskSnapshot := storageSnapshot()
	riskSnapshot.RequestID = "request-risk-full-prompt"
	riskSnapshot.FullPrompt = "PROMPT_CANARY_risk_event_full_prompt"
	job, err := repo.CreateStagingWithCapacity(ctx, riskSnapshot, 7, 2, 10)
	require.NoError(t, err)
	require.NoError(t, repo.PublishQueued(ctx, job.ID))
	claimed, ok, err := repo.ClaimNextJob(ctx, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	riskStored, err := repo.Complete(ctx, claimed, storageResult(), false, true)
	require.NoError(t, err)
	require.NotNil(t, riskStored)
	var storedRiskFullPrompt string
	require.NoError(t, db.QueryRow(`SELECT full_prompt FROM prompt_audit_events WHERE id = ?`, riskStored.ID).Scan(&storedRiskFullPrompt))
	require.Equal(t, riskSnapshot.FullPrompt, storedRiskFullPrompt)
}

// TestShouldStorePromptAuditEvent mirrors upstream's own table test for the
// helper that keeps store_pass_events scoped to safe results: risk events
// (flag/critical) are always stored once Prompt Audit is enabled, while pass
// events are only stored when explicitly opted in.
func TestShouldStorePromptAuditEvent(t *testing.T) {
	for _, tc := range []struct {
		name            string
		decision        EventDecision
		storePassEvents bool
		want            bool
	}{
		{"pass_disabled", EventPass, false, false},
		{"pass_enabled", EventPass, true, true},
		{"flag_disabled", EventFlag, false, true},
		{"flag_enabled", EventFlag, true, true},
		{"critical_disabled", EventCritical, false, true},
		{"critical_enabled", EventCritical, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldStorePromptAuditEvent(tc.decision, tc.storePassEvents))
		})
	}
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

// TestPromptAuditFullPromptMigrationIsPortableAndIdempotent mirrors
// TestPromptAuditMigrationIsPortableAndIdempotent for 189_prompt_audit_full_prompt.sql:
// the column must land on prompt_audit_events only, via a bare ADD COLUMN
// (portable across PostgreSQL/MySQL 5.7/SQLite, unlike upstream's Postgres-only
// ADD COLUMN IF NOT EXISTS), and applying it twice (idempotency via
// schema_migrations in production, tolerated here via
// isAlreadyAppliedMigrationError) must not fail or touch prompt_audit_jobs.
func TestPromptAuditFullPromptMigrationIsPortableAndIdempotent(t *testing.T) {
	db := newPromptStorageSQLite(t)
	applyPromptAuditMigrationFile(t, db, "189_prompt_audit_full_prompt.sql")

	contents, err := os.ReadFile("../../migrations/189_prompt_audit_full_prompt.sql")
	require.NoError(t, err)
	raw := strings.ToLower(string(contents))
	for _, forbidden := range []string{"if not exists", "bigserial", "timestamptz", "jsonb", "::json", "pg_"} {
		require.NotContains(t, raw, forbidden)
	}
	require.Contains(t, raw, "alter table prompt_audit_events add column full_prompt")
	require.NotContains(t, raw, "prompt_audit_jobs")

	var columnCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('prompt_audit_events') WHERE name = 'full_prompt'`).Scan(&columnCount))
	require.Equal(t, 1, columnCount, "full_prompt must exist exactly once on prompt_audit_events")

	var jobsColumnCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('prompt_audit_jobs') WHERE name = 'full_prompt'`).Scan(&jobsColumnCount))
	require.Zero(t, jobsColumnCount, "prompt_audit_jobs must never gain a full_prompt column")
}

func TestPromptAuditRepositoryUsesQuestionMarkDialectForSQLite(t *testing.T) {
	db := newPromptStorageSQLite(t)
	repo := NewSQLRepository(db)
	require.Equal(t, promptAuditQuestionMark, repo.dialect)
	require.Equal(t, "?", repo.placeholder(1))
	require.Equal(t, "$2", (&PostgreSQLRepository{dialect: promptAuditPostgreSQL}).placeholder(2))
}

func TestPromptAuditPostgreSQLRepositoryClaimAndUpdates(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	mock.ExpectQuery("WITH candidate AS").WithArgs(now).WillReturnRows(promptJobRows("processing"))
	job, claimed, err := repo.ClaimNextJob(ctx, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, int64(11), job.ID)
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='queued'").WithArgs(int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.PublishQueued(ctx, 11))
	mock.ExpectExec("UPDATE prompt_audit_jobs[[:space:]]+SET status='failed'").WithArgs(int64(11), "queue_full", "Prompt Audit queue is unavailable").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.MarkStagingFailed(ctx, 11, "queue_full", "secret"))
	mock.ExpectExec("UPDATE prompt_audit_jobs SET processing_started_at").WithArgs(int64(11), int64(5), now).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.RefreshLease(ctx, 11, 5, now))
	mock.ExpectExec("UPDATE prompt_audit_jobs SET processing_started_at").WithArgs(int64(11), int64(5), now).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT 1 FROM prompt_audit_jobs").WithArgs(int64(11), int64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	require.NoError(t, repo.RefreshLease(ctx, 11, 5, now))
	mock.ExpectExec("UPDATE prompt_audit_jobs SET processing_started_at").WithArgs(int64(11), int64(5), now).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT 1 FROM prompt_audit_jobs").WithArgs(int64(11), int64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}))
	require.ErrorIs(t, repo.RefreshLease(ctx, 11, 5, now), ErrLeaseLost)
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='retry'").WithArgs(int64(11), int64(5), now, "queue_full", "Prompt Audit queue is unavailable").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.Retry(ctx, 11, 5, now, "queue_full", "secret"))
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='failed'").WithArgs(int64(11), int64(5), "worker_failed", "Prompt Audit operation failed").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.Fail(ctx, 11, 5, "worker_failed", "secret"))
}

func TestPromptAuditPostgreSQLRepositorySequenceAllocation(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT next_id FROM prompt_audit_sequences").WithArgs("job").WillReturnRows(sqlmock.NewRows([]string{"next_id"}).AddRow(9))
	mock.ExpectExec("UPDATE prompt_audit_sequences SET next_id=\\$1").WithArgs(int64(10), "job", int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	id, err := repo.reserveID(ctx, tx, "job")
	require.NoError(t, err)
	require.Equal(t, int64(9), id)
	mock.ExpectCommit()
	require.NoError(t, tx.Commit())

	mock.ExpectBegin()
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		mock.ExpectQuery("SELECT next_id FROM prompt_audit_sequences").WithArgs("event").WillReturnRows(sqlmock.NewRows([]string{"next_id"}).AddRow(3))
		mock.ExpectExec("UPDATE prompt_audit_sequences SET next_id=\\$1").WithArgs(int64(4), "event", int64(3)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	_, err = repo.reserveID(ctx, tx, "event")
	require.ErrorIs(t, err, ErrQueueAdmissionBusy)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
}

func TestPromptAuditPostgreSQLRepositoryAdmissionAndCompletion(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WithArgs(promptAuditAdmissionLockKey).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM prompt_audit_jobs").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT next_id FROM prompt_audit_sequences").WithArgs("job").WillReturnRows(sqlmock.NewRows([]string{"next_id"}).AddRow(11))
	mock.ExpectExec("UPDATE prompt_audit_sequences SET next_id=\\$1").WithArgs(int64(12), "job", int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO prompt_audit_jobs").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT prompt_audit_jobs.id").WithArgs(int64(11)).WillReturnRows(promptJobRows("staging"))
	mock.ExpectCommit()
	job, err := repo.CreateStagingWithCapacity(ctx, storageSnapshot(), 7, 3, 10)
	require.NoError(t, err)
	require.Equal(t, int64(11), job.ID)

	job.ClaimVersion = 5
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='done'").WithArgs(int64(11), int64(5)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT next_id FROM prompt_audit_sequences").WithArgs("event").WillReturnRows(sqlmock.NewRows([]string{"next_id"}).AddRow(21))
	mock.ExpectExec("UPDATE prompt_audit_sequences SET next_id=\\$1").WithArgs(int64(22), "event", int64(21)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO prompt_audit_events").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT prompt_audit_events.id").WithArgs(int64(21)).WillReturnRows(promptEventRows())
	mock.ExpectCommit()
	event, err := repo.Complete(ctx, job, storageResult(), true, false)
	require.NoError(t, err)
	require.Equal(t, int64(21), event.ID)
}

func TestPromptAuditPostgreSQLRepositoryReclaimAndQueueStats(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	stagingBefore := time.Unix(1700000100, 0).UTC()
	processingBefore := time.Unix(1700000200, 0).UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,status,attempts,max_attempts FROM prompt_audit_jobs").
		WithArgs(stagingBefore, processingBefore, 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "attempts", "max_attempts"}).
			AddRow(int64(11), "staging", int64(0), int64(3)).
			AddRow(int64(12), "processing", int64(1), int64(3)).
			AddRow(int64(13), "processing", int64(3), int64(3)))
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='failed', processing_started_at=NULL").
		WithArgs(int64(11), stagingBefore).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='retry', next_attempt_at=CURRENT_TIMESTAMP").
		WithArgs(int64(12), processingBefore).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='failed', processing_started_at=NULL").
		WithArgs(int64(13), processingBefore).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	reclaimed, err := repo.ReclaimStale(ctx, stagingBefore, processingBefore, 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), reclaimed)

	mock.ExpectQuery("SELECT status, COUNT\\(\\*\\) FROM prompt_audit_jobs GROUP BY status").WillReturnRows(
		sqlmock.NewRows([]string{"status", "count"}).
			AddRow("staging", int64(1)).
			AddRow("queued", int64(2)).
			AddRow("processing", int64(3)).
			AddRow("retry", int64(4)).
			AddRow("done", int64(5)).
			AddRow("failed", int64(6)).
			AddRow("unknown", int64(7)))
	stats, err := repo.QueueStats(ctx)
	require.NoError(t, err)
	require.Equal(t, QueueStats{Staging: 1, Queued: 2, Processing: 3, Retry: 4, Done: 5, Failed: 6, Active: 10}, stats)
}

func TestPromptAuditPostgreSQLRepositorySkipsPassEventWhenDisabled(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT next_id FROM prompt_audit_sequences").WithArgs("job").WillReturnRows(sqlmock.NewRows([]string{"next_id"}).AddRow(11))
	mock.ExpectExec("UPDATE prompt_audit_sequences SET next_id=\\$1").WithArgs(int64(12), "job", int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO prompt_audit_jobs").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT prompt_audit_jobs.id").WithArgs(int64(11)).WillReturnRows(promptJobRows("done"))
	mock.ExpectCommit()

	event, err := repo.RecordBlocking(context.Background(), storageSnapshot(), 7, &NormalizedResult{Decision: EventPass}, false, false)
	require.NoError(t, err)
	require.Nil(t, event)
}

func TestPromptAuditRepositoryAdmissionAndClaimContention(t *testing.T) {
	db, mock := newPromptStorageSQLMock(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	postgres := NewPostgreSQLRepository(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WithArgs(promptAuditAdmissionLockKey).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	mock.ExpectRollback()
	_, err := postgres.CreateStagingWithCapacity(ctx, storageSnapshot(), 7, 3, 10)
	require.ErrorIs(t, err, ErrQueueAdmissionBusy)

	mock.ExpectQuery("WITH candidate AS").WithArgs(now).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	job, claimed, err := postgres.ClaimNextJob(ctx, now)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, job)

	portable := &PostgreSQLRepository{db: db, dialect: promptAuditQuestionMark}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT prompt_audit_jobs.id").WithArgs(now).WillReturnRows(promptJobRows("queued"))
	mock.ExpectExec("UPDATE prompt_audit_jobs[[:space:]]+SET status='processing'").
		WithArgs(now, now, int64(11), int64(5), now).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT prompt_audit_jobs.id").WithArgs(now).WillReturnRows(promptJobRows("queued"))
	mock.ExpectExec("UPDATE prompt_audit_jobs[[:space:]]+SET status='processing'").
		WithArgs(now, now, int64(11), int64(5), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	job, claimed, err = portable.ClaimNextJob(ctx, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, "processing", job.Status)
	require.Equal(t, int64(6), job.ClaimVersion)
}

func TestPromptAuditRepositoryValidationAndPassCompletion(t *testing.T) {
	ctx := context.Background()
	var unavailable *PostgreSQLRepository
	_, err := unavailable.CreateStagingWithCapacity(ctx, storageSnapshot(), 7, 1, 1)
	require.Error(t, err)
	_, _, err = unavailable.ClaimNextJob(ctx, time.Now())
	require.Error(t, err)
	_, err = unavailable.Complete(ctx, nil, nil, false, false)
	require.Error(t, err)
	_, err = unavailable.ReclaimStale(ctx, time.Now(), time.Now(), 1)
	require.Error(t, err)
	_, err = unavailable.RecordBlocking(ctx, storageSnapshot(), 7, nil, false, false)
	require.Error(t, err)

	db, mock := newPromptStorageSQLMock(t)
	repo := NewPostgreSQLRepository(db)
	job := &Job{ID: 11, ClaimVersion: 5, Snapshot: storageSnapshot(), ConfigVersion: 7}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='done'").WithArgs(int64(11), int64(5)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	event, err := repo.Complete(ctx, job, &NormalizedResult{Decision: EventPass}, false, false)
	require.NoError(t, err)
	require.Nil(t, event)

	mock.ExpectExec("UPDATE prompt_audit_jobs SET status='queued'").WithArgs(int64(11)).WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, repo.PublishQueued(ctx, 11), ErrLeaseLost)
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
	require.NotContains(t, eventColumns("e"), "full_prompt")
	require.Contains(t, eventDetailColumns("e"), "e.full_prompt")

	require.Equal(t, "redacted_error", stableErrorCode("raw response; secret"))
	require.Equal(t, "unknown_error", stableErrorCode(""))
	code, message := sanitizeStoredError("queue_full")
	require.Equal(t, "queue_full", code)
	require.Equal(t, "Prompt Audit queue is unavailable", message)
	for _, tc := range []struct {
		code, message string
	}{
		{ErrorCodeBlocked, "Prompt Guard blocked the request"},
		{ErrorCodeUnavailable, "Prompt Audit dependency is unavailable"},
		{"payload_missing", "Prompt Audit dependency is unavailable"},
		{ErrorCodeInvalidResponse, "Prompt Guard returned an invalid response"},
		{"unexpected", "Prompt Audit operation failed"},
	} {
		require.Equal(t, tc.message, stableErrorMessage(tc.code))
	}
	require.False(t, realClock{}.Now().IsZero())

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
	require.Error(t, store.Set(context.Background(), 0, "text", time.Second))
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
