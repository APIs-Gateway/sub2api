package securityaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const promptAuditAdmissionLockKey int64 = 579147893221901921

var (
	ErrQueueFull          = errors.New("prompt audit queue full")
	ErrQueueAdmissionBusy = errors.New("prompt audit queue admission busy")
	ErrLeaseLost          = errors.New("prompt audit worker lease lost")
	ErrEventNotFound      = errors.New("prompt audit event not found")
)

type Job struct {
	ID                  int64
	Snapshot            PromptSnapshot
	ExecutionMode       Mode
	ConfigVersion       int64
	Status              string
	Attempts            int
	MaxAttempts         int
	ClaimVersion        int64
	NextAttemptAt       time.Time
	ProcessingStartedAt *time.Time
	ProcessedAt         *time.Time
	LastErrorCode       string
	LastErrorMessage    string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Event struct {
	ID              int64              `json:"id"`
	JobID           int64              `json:"job_id"`
	Snapshot        PromptSnapshot     `json:"snapshot"`
	Decision        EventDecision      `json:"decision"`
	RiskLevel       RiskLevel          `json:"risk_level"`
	Action          Action             `json:"action"`
	Categories      []string           `json:"categories"`
	MatchedScanners []string           `json:"matched_scanners"`
	ScannerScores   map[string]float64 `json:"scanner_scores"`
	ScannerEvidence map[string]string  `json:"scanner_evidence"`
	ScannerBackend  string             `json:"scanner_backend"`
	ScannerVersion  string             `json:"scanner_version"`
	GuardEndpointID string             `json:"guard_endpoint_id"`
	PolicyID        string             `json:"policy_id"`
	PolicyVersion   int                `json:"policy_version"`
	ConfigVersion   int64              `json:"config_version"`
	ChunkTotal      int                `json:"chunk_total"`
	LatencyMS       int                `json:"latency_ms"`
	IssueSummaries  []IssueSummary     `json:"issue_summaries"`
	CreatedAt       time.Time          `json:"created_at"`
}

type JobRepository interface {
	CreateStagingWithCapacity(ctx context.Context, snapshot PromptSnapshot, configVersion int64, maxAttempts, capacity int) (*Job, error)
	PublishQueued(ctx context.Context, jobID int64) error
	MarkStagingFailed(ctx context.Context, jobID int64, code, message string) error
	ClaimNextJob(ctx context.Context, now time.Time) (*Job, bool, error)
	RefreshLease(ctx context.Context, jobID, claimVersion int64, now time.Time) error
	Complete(ctx context.Context, job *Job, result *NormalizedResult, storePass bool) (*Event, error)
	Retry(ctx context.Context, jobID, claimVersion int64, next time.Time, code, message string) error
	Fail(ctx context.Context, jobID, claimVersion int64, code, message string) error
	ReclaimStale(ctx context.Context, stagingBefore, processingBefore time.Time, limit int) (int64, error)
	QueueStats(ctx context.Context) (QueueStats, error)
	RecordBlocking(ctx context.Context, snapshot PromptSnapshot, configVersion int64, result *NormalizedResult, storePass bool) (*Event, error)
}

type promptAuditDialect uint8

const (
	promptAuditPostgreSQL promptAuditDialect = iota
	promptAuditQuestionMark
)

// PostgreSQLRepository is kept as the public compatibility name used by the
// follow-up Prompt Audit phases. Its SQL is selected from the database driver,
// so the same repository is safe to use with PostgreSQL, SQLite, and MySQL.
type PostgreSQLRepository struct {
	db      *sql.DB
	clock   Clock
	dialect promptAuditDialect
}

type SQLRepository = PostgreSQLRepository

func NewPostgreSQLRepository(db *sql.DB) *PostgreSQLRepository {
	return NewSQLRepository(db)
}

func NewSQLRepository(db *sql.DB) *SQLRepository {
	return &SQLRepository{db: db, clock: realClock{}, dialect: promptAuditSQLDialect(db)}
}

func promptAuditSQLDialect(db *sql.DB) promptAuditDialect {
	if db == nil {
		return promptAuditPostgreSQL
	}
	driverName := strings.ToLower(fmt.Sprintf("%T", db.Driver()))
	if strings.Contains(driverName, "sqlite") || strings.Contains(driverName, "mysql") {
		return promptAuditQuestionMark
	}
	return promptAuditPostgreSQL
}

func (r *PostgreSQLRepository) placeholder(position int) string {
	if r != nil && r.dialect == promptAuditQuestionMark {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}

func (r *PostgreSQLRepository) CreateStagingWithCapacity(ctx context.Context, snapshot PromptSnapshot, configVersion int64, maxAttempts, capacity int) (*Job, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if r.dialect == promptAuditPostgreSQL {
		var locked bool
		if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, promptAuditAdmissionLockKey).Scan(&locked); err != nil {
			return nil, err
		}
		if !locked {
			return nil, ErrQueueAdmissionBusy
		}
	} else {
		// A no-op write on the sequence row serializes capacity admission on
		// SQLite and MySQL without relying on their incompatible lock syntax.
		_, err := tx.ExecContext(ctx,
			`UPDATE prompt_audit_sequences SET next_id=next_id WHERE name=`+r.placeholder(1), "job")
		if err != nil {
			return nil, fmt.Errorf("lock prompt audit admission: %w", err)
		}
	}

	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM prompt_audit_jobs
		WHERE status IN ('staging','queued','processing','retry')`).Scan(&active); err != nil {
		return nil, err
	}
	if capacity <= 0 || active >= capacity {
		return nil, ErrQueueFull
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	job, err := r.insertJob(ctx, tx, snapshot.Redacted(), ModeAsync, configVersion, "staging", maxAttempts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func (r *PostgreSQLRepository) PublishQueued(ctx context.Context, jobID int64) error {
	p := r.placeholder
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET status='queued', next_attempt_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE id=`+p(1)+` AND status='staging'`, jobID)
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) MarkStagingFailed(ctx context.Context, jobID int64, code, _ string) error {
	code, message := sanitizeStoredError(code)
	p := r.placeholder
	args := []any{jobID, code, message}
	if r.dialect == promptAuditQuestionMark {
		args = []any{code, message, jobID}
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs
		SET status='failed', processed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP, last_error_code=`+p(2)+`, last_error_message=`+p(3)+`
		WHERE id=`+p(1)+` AND status='staging'`, args...)
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) ClaimNextJob(ctx context.Context, now time.Time) (*Job, bool, error) {
	if r == nil || r.db == nil {
		return nil, false, errors.New("prompt audit database unavailable")
	}
	if r.dialect == promptAuditPostgreSQL {
		row := r.db.QueryRowContext(ctx, `
			WITH candidate AS (
				SELECT id FROM prompt_audit_jobs
				WHERE status IN ('queued','retry') AND next_attempt_at <= $1
				ORDER BY next_attempt_at, id
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE prompt_audit_jobs AS j
			SET status='processing', attempts=j.attempts+1, claim_version=j.claim_version+1,
				processing_started_at=$1, updated_at=$1
			FROM candidate
			WHERE j.id=candidate.id
			RETURNING `+jobColumns("j"), now.UTC())
		job, err := scanJob(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return job, err == nil, err
	}
	return r.claimNextPortable(ctx, now.UTC())
}

func (r *PostgreSQLRepository) claimNextPortable(ctx context.Context, now time.Time) (*Job, bool, error) {
	p := r.placeholder
	for attempts := 0; attempts < 4; attempts++ {
		tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return nil, false, err
		}
		job, err := r.selectJob(ctx, tx, `
			WHERE status IN ('queued','retry') AND next_attempt_at <= `+p(1)+`
			ORDER BY next_attempt_at, id LIMIT 1`, now)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return nil, false, nil
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		claimArgs := []any{now, job.ID, job.ClaimVersion, now}
		if r.dialect == promptAuditQuestionMark {
			claimArgs = []any{now, now, job.ID, job.ClaimVersion, now}
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE prompt_audit_jobs
			SET status='processing', attempts=attempts+1, claim_version=claim_version+1,
				processing_started_at=`+p(1)+`, updated_at=`+p(1)+`
			WHERE id=`+p(2)+` AND status IN ('queued','retry') AND claim_version=`+p(3)+` AND next_attempt_at <= `+p(4),
			claimArgs...)
		if err := requireOneRow(result, err, ErrLeaseLost); err != nil {
			_ = tx.Rollback()
			if errors.Is(err, ErrLeaseLost) {
				continue
			}
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		job.Status = "processing"
		job.Attempts++
		job.ClaimVersion++
		job.ProcessingStartedAt = &now
		job.UpdatedAt = now
		return job, true, nil
	}
	return nil, false, ErrQueueAdmissionBusy
}

func (r *PostgreSQLRepository) RefreshLease(ctx context.Context, jobID, claimVersion int64, now time.Time) error {
	p := r.placeholder
	args := []any{jobID, claimVersion, now.UTC()}
	if r.dialect == promptAuditQuestionMark {
		args = []any{now.UTC(), now.UTC(), jobID, claimVersion}
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET processing_started_at=`+p(3)+`, updated_at=`+p(3)+`
		WHERE id=`+p(1)+` AND status='processing' AND claim_version=`+p(2), args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	// MySQL reports changed rows by default. When a claim and its first
	// heartbeat land in the same timestamp precision bucket, the row matches
	// but reports zero affected rows. Recheck the lease predicate so that case
	// is not mistaken for a lost worker lease.
	var exists int
	err = r.db.QueryRowContext(ctx, `SELECT 1 FROM prompt_audit_jobs WHERE id=`+p(1)+` AND status='processing' AND claim_version=`+p(2), jobID, claimVersion).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

func (r *PostgreSQLRepository) Complete(ctx context.Context, job *Job, result *NormalizedResult, storePass bool) (*Event, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	if job == nil || result == nil {
		return nil, errors.New("prompt audit completion requires job and result")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	p := r.placeholder
	updateResult, err := tx.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET status='done', processed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP,
			last_error_code='', last_error_message=''
		WHERE id=`+p(1)+` AND status='processing' AND claim_version=`+p(2), job.ID, job.ClaimVersion)
	if err := requireOneRow(updateResult, err, ErrLeaseLost); err != nil {
		return nil, err
	}
	var event *Event
	if storePass || result.Decision != EventPass {
		event, err = r.insertEvent(ctx, tx, job.ID, job.Snapshot.Redacted(), job.ConfigVersion, result)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

func (r *PostgreSQLRepository) Retry(ctx context.Context, jobID, claimVersion int64, next time.Time, code, _ string) error {
	code, message := sanitizeStoredError(code)
	p := r.placeholder
	args := []any{jobID, claimVersion, next.UTC(), code, message}
	if r.dialect == promptAuditQuestionMark {
		args = []any{next.UTC(), code, message, jobID, claimVersion}
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET status='retry', next_attempt_at=`+p(3)+`, processing_started_at=NULL,
			updated_at=CURRENT_TIMESTAMP, last_error_code=`+p(4)+`, last_error_message=`+p(5)+`
		WHERE id=`+p(1)+` AND status='processing' AND claim_version=`+p(2),
		args...)
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) Fail(ctx context.Context, jobID, claimVersion int64, code, _ string) error {
	code, message := sanitizeStoredError(code)
	p := r.placeholder
	args := []any{jobID, claimVersion, code, message}
	if r.dialect == promptAuditQuestionMark {
		args = []any{code, message, jobID, claimVersion}
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET status='failed', processed_at=CURRENT_TIMESTAMP, processing_started_at=NULL,
			updated_at=CURRENT_TIMESTAMP, last_error_code=`+p(3)+`, last_error_message=`+p(4)+`
		WHERE id=`+p(1)+` AND status='processing' AND claim_version=`+p(2),
		args...)
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) ReclaimStale(ctx context.Context, stagingBefore, processingBefore time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("prompt audit database unavailable")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	p := r.placeholder
	rows, err := tx.QueryContext(ctx, `
		SELECT id,status,attempts,max_attempts FROM prompt_audit_jobs
		WHERE (status='staging' AND updated_at < `+p(1)+`)
		   OR (status='processing' AND processing_started_at < `+p(2)+`)
		ORDER BY updated_at, id LIMIT `+p(3), stagingBefore.UTC(), processingBefore.UTC(), limit)
	if err != nil {
		return 0, err
	}
	type staleJob struct {
		id, attempts, maxAttempts int64
		status                    string
	}
	stale := make([]staleJob, 0, limit)
	for rows.Next() {
		var entry staleJob
		if err := rows.Scan(&entry.id, &entry.status, &entry.attempts, &entry.maxAttempts); err != nil {
			_ = rows.Close()
			return 0, err
		}
		stale = append(stale, entry)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var reclaimed int64
	for _, entry := range stale {
		var update string
		var args []any
		if entry.status == "staging" {
			update = `UPDATE prompt_audit_jobs SET status='failed', processing_started_at=NULL,
				processed_at=CURRENT_TIMESTAMP, last_error_code='staging_timeout', last_error_message='', updated_at=CURRENT_TIMESTAMP
				WHERE id=` + p(1) + ` AND status='staging' AND updated_at < ` + p(2)
			args = []any{entry.id, stagingBefore.UTC()}
		} else if entry.attempts < entry.maxAttempts {
			update = `UPDATE prompt_audit_jobs SET status='retry', next_attempt_at=CURRENT_TIMESTAMP, processing_started_at=NULL,
				processed_at=NULL, last_error_code='processing_lease_expired', last_error_message='', updated_at=CURRENT_TIMESTAMP
				WHERE id=` + p(1) + ` AND status='processing' AND processing_started_at < ` + p(2)
			args = []any{entry.id, processingBefore.UTC()}
		} else {
			update = `UPDATE prompt_audit_jobs SET status='failed', processing_started_at=NULL,
				processed_at=CURRENT_TIMESTAMP, last_error_code='processing_lease_expired', last_error_message='', updated_at=CURRENT_TIMESTAMP
				WHERE id=` + p(1) + ` AND status='processing' AND processing_started_at < ` + p(2)
			args = []any{entry.id, processingBefore.UTC()}
		}
		result, err := tx.ExecContext(ctx, update, args...)
		if err != nil {
			return 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		reclaimed += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return reclaimed, nil
}

func (r *PostgreSQLRepository) QueueStats(ctx context.Context) (QueueStats, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM prompt_audit_jobs GROUP BY status`)
	if err != nil {
		return QueueStats{}, err
	}
	defer func() { _ = rows.Close() }()
	var stats QueueStats
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return QueueStats{}, err
		}
		switch status {
		case "staging":
			stats.Staging = count
		case "queued":
			stats.Queued = count
		case "processing":
			stats.Processing = count
		case "retry":
			stats.Retry = count
		case "done":
			stats.Done = count
		case "failed":
			stats.Failed = count
		}
	}
	stats.Active = stats.Staging + stats.Queued + stats.Processing + stats.Retry
	return stats, rows.Err()
}

func (r *PostgreSQLRepository) RecordBlocking(ctx context.Context, snapshot PromptSnapshot, configVersion int64, result *NormalizedResult, storePass bool) (*Event, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	if result == nil {
		return nil, errors.New("prompt guard result required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	job, err := r.insertJob(ctx, tx, snapshot.Redacted(), ModeBlocking, configVersion, "done", 1)
	if err != nil {
		return nil, err
	}
	var event *Event
	if storePass || result.Decision != EventPass {
		event, err = r.insertEvent(ctx, tx, job.ID, snapshot.Redacted(), configVersion, result)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

type sqlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type sqlTransaction interface {
	sqlQueryer
	sqlExecutor
}

func (r *PostgreSQLRepository) insertJob(ctx context.Context, tx sqlTransaction, snapshot PromptSnapshot, mode Mode, configVersion int64, status string, maxAttempts int) (*Job, error) {
	id, err := r.reserveID(ctx, tx, "job")
	if err != nil {
		return nil, err
	}
	processedExpr := "NULL"
	if status == "done" || status == "failed" {
		processedExpr = "CURRENT_TIMESTAMP"
	}
	p := r.placeholder
	_, err = tx.ExecContext(ctx, `
		INSERT INTO prompt_audit_jobs (
			id,request_id,user_id,username_snapshot,user_email_snapshot,api_key_id,api_key_name_snapshot,
			group_id,group_name,provider,endpoint,protocol,model,prompt_hash,redacted_preview,
			prompt_length,message_count,execution_mode,config_version,status,max_attempts,processed_at
		) VALUES (`+p(1)+`,`+p(2)+`,`+p(3)+`,`+p(4)+`,`+p(5)+`,`+p(6)+`,`+p(7)+`,`+p(8)+`,`+p(9)+`,`+p(10)+`,`+p(11)+`,`+p(12)+`,`+p(13)+`,`+p(14)+`,`+p(15)+`,`+p(16)+`,`+p(17)+`,`+p(18)+`,`+p(19)+`,`+p(20)+`,`+p(21)+`,`+processedExpr+`)`,
		id, snapshot.RequestID, nullableID(snapshot.UserID), snapshot.UsernameSnapshot, snapshot.UserEmailSnapshot,
		nullableID(snapshot.APIKeyID), snapshot.APIKeyNameSnapshot, snapshot.GroupID, snapshot.GroupName,
		snapshot.Provider, snapshot.Endpoint, snapshot.Protocol, snapshot.Model, snapshot.PromptHash,
		snapshot.RedactedPreview, snapshot.PromptLength, snapshot.MessageCount, string(mode), configVersion,
		status, maxAttempts)
	if err != nil {
		return nil, err
	}
	return r.selectJob(ctx, tx, " WHERE id="+p(1), id)
}

func (r *PostgreSQLRepository) insertEvent(ctx context.Context, tx sqlTransaction, jobID int64, snapshot PromptSnapshot, configVersion int64, result *NormalizedResult) (*Event, error) {
	id, err := r.reserveID(ctx, tx, "event")
	if err != nil {
		return nil, err
	}
	categories, _ := json.Marshal(result.Categories)
	matched, _ := json.Marshal(result.MatchedScanners)
	scores, _ := json.Marshal(result.ScannerScores)
	evidence := make(map[string]string, len(result.ScannerEvidence))
	for key, value := range result.ScannerEvidence {
		evidence[key] = RedactPreview(value, 160)
	}
	evidenceJSON, _ := json.Marshal(evidence)
	p := r.placeholder
	_, err = tx.ExecContext(ctx, `
		INSERT INTO prompt_audit_events (
			id,job_id,request_id,user_id,username_snapshot,user_email_snapshot,api_key_id,api_key_name_snapshot,
			group_id,group_name,provider,endpoint,protocol,model,prompt_hash,redacted_preview,
			decision,risk_level,action,categories,matched_scanners,scanner_scores,scanner_evidence,
			scanner_backend,scanner_version,guard_endpoint_id,policy_id,policy_version,config_version,chunk_total,latency_ms
		) VALUES (`+p(1)+`,`+p(2)+`,`+p(3)+`,`+p(4)+`,`+p(5)+`,`+p(6)+`,`+p(7)+`,`+p(8)+`,`+p(9)+`,`+p(10)+`,`+p(11)+`,`+p(12)+`,`+p(13)+`,`+p(14)+`,`+p(15)+`,`+p(16)+`,`+p(17)+`,`+p(18)+`,`+p(19)+`,`+p(20)+`,`+p(21)+`,`+p(22)+`,`+p(23)+`,`+p(24)+`,`+p(25)+`,`+p(26)+`,`+p(27)+`,`+p(28)+`,`+p(29)+`,`+p(30)+`,`+p(31)+`)`,
		id, jobID, snapshot.RequestID, nullableID(snapshot.UserID), snapshot.UsernameSnapshot, snapshot.UserEmailSnapshot,
		nullableID(snapshot.APIKeyID), snapshot.APIKeyNameSnapshot, snapshot.GroupID, snapshot.GroupName,
		snapshot.Provider, snapshot.Endpoint, snapshot.Protocol, snapshot.Model, snapshot.PromptHash,
		snapshot.RedactedPreview, string(result.Decision), string(result.RiskLevel), string(result.Action),
		string(categories), string(matched), string(scores), string(evidenceJSON), result.ScannerBackend, result.ScannerVersion,
		result.GuardEndpointID, result.PolicyID, result.PolicyVersion, configVersion, result.ChunkTotal, result.LatencyMS)
	if err != nil {
		return nil, err
	}
	return r.selectEvent(ctx, tx, " WHERE id="+p(1), id)
}

func (r *PostgreSQLRepository) reserveID(ctx context.Context, tx sqlTransaction, sequence string) (int64, error) {
	p := r.placeholder
	for attempts := 0; attempts < 8; attempts++ {
		var next int64
		if err := tx.QueryRowContext(ctx, `SELECT next_id FROM prompt_audit_sequences WHERE name=`+p(1), sequence).Scan(&next); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE prompt_audit_sequences SET next_id=`+p(1)+` WHERE name=`+p(2)+` AND next_id=`+p(3), next+1, sequence, next)
		if err != nil {
			return 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if affected == 1 {
			return next, nil
		}
	}
	return 0, ErrQueueAdmissionBusy
}

func (r *PostgreSQLRepository) selectJob(ctx context.Context, queryer sqlQueryer, suffix string, args ...any) (*Job, error) {
	return scanJob(queryer.QueryRowContext(ctx, `SELECT `+jobColumns("prompt_audit_jobs")+` FROM prompt_audit_jobs`+suffix, args...))
}

func (r *PostgreSQLRepository) selectEvent(ctx context.Context, queryer sqlQueryer, suffix string, args ...any) (*Event, error) {
	return scanEvent(queryer.QueryRowContext(ctx, `SELECT `+eventColumns("prompt_audit_events")+` FROM prompt_audit_events`+suffix, args...))
}

type rowScanner interface{ Scan(...any) error }

func scanJob(row rowScanner) (*Job, error) {
	job := &Job{}
	var userID, apiKeyID, groupID sql.NullInt64
	var processingStarted, processed sql.NullTime
	err := row.Scan(
		&job.ID, &job.Snapshot.RequestID, &userID, &job.Snapshot.UsernameSnapshot, &job.Snapshot.UserEmailSnapshot,
		&apiKeyID, &job.Snapshot.APIKeyNameSnapshot, &groupID, &job.Snapshot.GroupName, &job.Snapshot.Provider,
		&job.Snapshot.Endpoint, &job.Snapshot.Protocol, &job.Snapshot.Model, &job.Snapshot.PromptHash,
		&job.Snapshot.RedactedPreview, &job.Snapshot.PromptLength, &job.Snapshot.MessageCount, &job.ExecutionMode,
		&job.ConfigVersion, &job.Status, &job.Attempts, &job.MaxAttempts, &job.ClaimVersion,
		&job.NextAttemptAt, &processingStarted, &processed, &job.LastErrorCode, &job.LastErrorMessage,
		&job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	job.Snapshot.UserID = nullableInt64Value(userID)
	job.Snapshot.APIKeyID = nullableInt64Value(apiKeyID)
	job.Snapshot.GroupID = nullableInt64Ptr(groupID)
	if processingStarted.Valid {
		value := processingStarted.Time
		job.ProcessingStartedAt = &value
	}
	if processed.Valid {
		value := processed.Time
		job.ProcessedAt = &value
	}
	return job, nil
}

func jobColumns(alias string) string {
	return fmt.Sprintf(`%[1]s.id,%[1]s.request_id,%[1]s.user_id,%[1]s.username_snapshot,%[1]s.user_email_snapshot,
		%[1]s.api_key_id,%[1]s.api_key_name_snapshot,%[1]s.group_id,%[1]s.group_name,%[1]s.provider,
		%[1]s.endpoint,%[1]s.protocol,%[1]s.model,%[1]s.prompt_hash,%[1]s.redacted_preview,
		%[1]s.prompt_length,%[1]s.message_count,%[1]s.execution_mode,%[1]s.config_version,%[1]s.status,
		%[1]s.attempts,%[1]s.max_attempts,%[1]s.claim_version,%[1]s.next_attempt_at,
		%[1]s.processing_started_at,%[1]s.processed_at,%[1]s.last_error_code,%[1]s.last_error_message,
		%[1]s.created_at,%[1]s.updated_at`, alias)
}

func requireOneRow(result sql.Result, err error, missing error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return missing
	}
	return nil
}

func nullableID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableInt64Value(value sql.NullInt64) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}

func nullableInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}
