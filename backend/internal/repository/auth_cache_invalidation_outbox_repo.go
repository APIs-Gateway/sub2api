package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type authCacheInvalidationOutboxRepository struct {
	db      *sql.DB
	dialect authCacheInvalidationOutboxDialect
}

type authCacheInvalidationOutboxDialect uint8

const (
	authCacheInvalidationOutboxPostgres authCacheInvalidationOutboxDialect = iota
	authCacheInvalidationOutboxMySQL
	authCacheInvalidationOutboxSQLite
)

func NewAuthCacheInvalidationOutboxRepository(db *sql.DB) service.AuthCacheInvalidationOutboxRepository {
	return &authCacheInvalidationOutboxRepository{
		db:      db,
		dialect: authCacheInvalidationOutboxDialectForDB(db),
	}
}

func authCacheInvalidationOutboxDialectForDB(db *sql.DB) authCacheInvalidationOutboxDialect {
	if db == nil || isPostgresDriver(db) {
		return authCacheInvalidationOutboxPostgres
	}
	driverName := strings.ToLower(fmt.Sprintf("%T", db.Driver()))
	if strings.Contains(driverName, "mysql") {
		return authCacheInvalidationOutboxMySQL
	}
	if strings.Contains(driverName, "sqlite") {
		return authCacheInvalidationOutboxSQLite
	}
	// sqlmock and unknown drivers use the PostgreSQL branch so existing tests
	// continue to exercise the production default.
	return authCacheInvalidationOutboxPostgres
}

func authCacheInvalidationOutboxPlaceholder(dialect authCacheInvalidationOutboxDialect, position int) string {
	if dialect != authCacheInvalidationOutboxPostgres {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}

func (r *authCacheInvalidationOutboxRepository) Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]service.AuthCacheInvalidationEvent, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil auth cache invalidation outbox database")
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, errors.New("empty auth cache invalidation worker id")
	}
	if limit <= 0 {
		limit = 100
	}
	if r.dialect != authCacheInvalidationOutboxPostgres {
		return r.claimWithoutReturning(ctx, workerID, limit, lease)
	}
	return r.claimPostgres(ctx, workerID, limit, lease)
}

func (r *authCacheInvalidationOutboxRepository) claimPostgres(ctx context.Context, workerID string, limit int, lease time.Duration) ([]service.AuthCacheInvalidationEvent, error) {
	leaseSeconds := int64(lease / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 30
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM auth_cache_invalidation_outbox
			WHERE available_at <= NOW()
			  AND (claimed_at IS NULL OR claimed_at < NOW() - ($3 * INTERVAL '1 second'))
			ORDER BY id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE auth_cache_invalidation_outbox AS o
		SET claimed_at = NOW(), claimed_by = $1
		FROM candidates AS c
		WHERE o.id = c.id
		RETURNING o.id, o.cache_key, o.attempts, o.delivery_stage, o.created_at
	`, workerID, limit, leaseSeconds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	events := make([]service.AuthCacheInvalidationEvent, 0, limit)
	for rows.Next() {
		var event service.AuthCacheInvalidationEvent
		if err := rows.Scan(&event.ID, &event.CacheKey, &event.Attempts, &event.Stage, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.CacheKey = strings.TrimSpace(event.CacheKey)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// claimWithoutReturning is the portable claim path for MySQL 5.7 and SQLite.
// Neither dialect can use the PostgreSQL CTE/UPDATE RETURNING form; MySQL 5.7
// also has no SKIP LOCKED. The transaction locks candidates where supported,
// then the conditional updates make the SQLite path safe against a competing
// worker as well.
func (r *authCacheInvalidationOutboxRepository) claimWithoutReturning(ctx context.Context, workerID string, limit int, lease time.Duration) ([]service.AuthCacheInvalidationEvent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	leaseCutoff := time.Now().UTC().Add(-lease)
	if lease <= 0 {
		leaseCutoff = time.Now().UTC().Add(-30 * time.Second)
	}
	leasePlaceholder := authCacheInvalidationOutboxPlaceholder(r.dialect, 1)
	limitPlaceholder := authCacheInvalidationOutboxPlaceholder(r.dialect, 2)
	lockClause := ""
	if r.dialect == authCacheInvalidationOutboxMySQL {
		lockClause = " FOR UPDATE"
	}
	query := fmt.Sprintf(`
		SELECT id, cache_key, attempts, delivery_stage, created_at
		FROM auth_cache_invalidation_outbox
		WHERE available_at <= CURRENT_TIMESTAMP
		  AND (claimed_at IS NULL OR claimed_at < %s)
		ORDER BY id ASC
		LIMIT %s%s
	`, leasePlaceholder, limitPlaceholder, lockClause)
	rows, err := tx.QueryContext(ctx, query, leaseCutoff, limit)
	if err != nil {
		return nil, err
	}

	candidates := make([]service.AuthCacheInvalidationEvent, 0, limit)
	for rows.Next() {
		var event service.AuthCacheInvalidationEvent
		if err := rows.Scan(&event.ID, &event.CacheKey, &event.Attempts, &event.Stage, &event.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		event.CacheKey = strings.TrimSpace(event.CacheKey)
		candidates = append(candidates, event)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	claimed := make([]service.AuthCacheInvalidationEvent, 0, len(candidates))
	updateQuery := fmt.Sprintf(`
		UPDATE auth_cache_invalidation_outbox
		SET claimed_at = CURRENT_TIMESTAMP, claimed_by = %s
		WHERE id = %s
		  AND (claimed_at IS NULL OR claimed_at < %s)
	`,
		authCacheInvalidationOutboxPlaceholder(r.dialect, 1),
		authCacheInvalidationOutboxPlaceholder(r.dialect, 2),
		authCacheInvalidationOutboxPlaceholder(r.dialect, 3),
	)
	for _, event := range candidates {
		result, err := tx.ExecContext(ctx, updateQuery, workerID, event.ID, leaseCutoff)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 1 {
			claimed = append(claimed, event)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (r *authCacheInvalidationOutboxRepository) ScheduleSecondPass(ctx context.Context, id int64, workerID string, availableAt time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("nil auth cache invalidation outbox database")
	}
	query := fmt.Sprintf(`
		UPDATE auth_cache_invalidation_outbox
		SET delivery_stage = 1,
			available_at = %s,
			last_error = NULL,
			claimed_at = NULL,
			claimed_by = NULL
		WHERE id = %s AND claimed_by = %s AND delivery_stage = 0
	`,
		authCacheInvalidationOutboxPlaceholder(r.dialect, 3),
		authCacheInvalidationOutboxPlaceholder(r.dialect, 1),
		authCacheInvalidationOutboxPlaceholder(r.dialect, 2),
	)
	args := []any{id, workerID, availableAt}
	if r.dialect != authCacheInvalidationOutboxPostgres {
		args = []any{availableAt, id, workerID}
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("auth cache invalidation claim %d cannot schedule second pass", id)
	}
	return nil
}

func (r *authCacheInvalidationOutboxRepository) DeleteClaimed(ctx context.Context, id int64, workerID string) error {
	if r == nil || r.db == nil {
		return errors.New("nil auth cache invalidation outbox database")
	}
	result, err := r.db.ExecContext(ctx, fmt.Sprintf(`
		DELETE FROM auth_cache_invalidation_outbox
		WHERE id = %s AND claimed_by = %s
	`,
		authCacheInvalidationOutboxPlaceholder(r.dialect, 1),
		authCacheInvalidationOutboxPlaceholder(r.dialect, 2),
	), id, workerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("auth cache invalidation claim %d is no longer owned by %s", id, workerID)
	}
	return nil
}

func (r *authCacheInvalidationOutboxRepository) RetryClaimed(ctx context.Context, id int64, workerID string, availableAt time.Time, lastError string) error {
	if r == nil || r.db == nil {
		return errors.New("nil auth cache invalidation outbox database")
	}
	query := fmt.Sprintf(`
		UPDATE auth_cache_invalidation_outbox
		SET attempts = attempts + 1,
			available_at = %s,
			last_error = %s,
			claimed_at = NULL,
			claimed_by = NULL
		WHERE id = %s AND claimed_by = %s
	`,
		authCacheInvalidationOutboxPlaceholder(r.dialect, 3),
		authCacheInvalidationOutboxPlaceholder(r.dialect, 4),
		authCacheInvalidationOutboxPlaceholder(r.dialect, 1),
		authCacheInvalidationOutboxPlaceholder(r.dialect, 2),
	)
	args := []any{id, workerID, availableAt, lastError}
	if r.dialect != authCacheInvalidationOutboxPostgres {
		args = []any{availableAt, lastError, id, workerID}
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("auth cache invalidation claim %d is no longer owned by %s", id, workerID)
	}
	return nil
}

func (r *authCacheInvalidationOutboxRepository) Stats(ctx context.Context) (service.AuthCacheInvalidationOutboxStats, error) {
	if r == nil || r.db == nil {
		return service.AuthCacheInvalidationOutboxStats{}, errors.New("nil auth cache invalidation outbox database")
	}
	var (
		stats     service.AuthCacheInvalidationOutboxStats
		oldest    sql.NullTime
		lastError sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(created_at), COALESCE(MAX(attempts), 0),
			(SELECT last_error
			 FROM auth_cache_invalidation_outbox
			 WHERE last_error IS NOT NULL
			 ORDER BY available_at DESC, id DESC
			 LIMIT 1)
		FROM auth_cache_invalidation_outbox
	`).Scan(&stats.Pending, &oldest, &stats.MaxAttempts, &lastError)
	if err != nil {
		return stats, err
	}
	if oldest.Valid {
		value := oldest.Time
		stats.OldestCreatedAt = &value
	}
	if lastError.Valid {
		stats.LastError = lastError.String
	}
	return stats, nil
}
