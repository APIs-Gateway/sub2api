package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ExtendModelRateLimit writes a new model cooldown only if the existing reset
// is earlier. Holding the account row lock across the read and write prevents
// an in-flight image balance response from shortening a later 429 reset.
func (r *accountRepository) ExtendModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	if scope == "" {
		return nil
	}
	// This method owns its SQL transaction. Do not silently run it outside an
	// Ent transaction supplied by the caller, or through an executor without
	// transaction support; the image failover path will log a failed cooldown.
	if dbent.TxFromContext(ctx) != nil {
		return errors.New("extend model rate limit cannot run inside an Ent transaction")
	}
	db, ok := r.sql.(*sql.DB)
	if !ok || db == nil {
		return errors.New("extend model rate limit requires a SQL database")
	}
	resetAt = resetAt.UTC().Truncate(time.Second)
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()

	var extra []byte
	if err := transaction.QueryRowContext(ctx,
		`SELECT COALESCE(extra, '{}'::jsonb) FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id,
	).Scan(&extra); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrAccountNotFound
		}
		return err
	}
	if current := modelRateLimitResetFromExtra(extra, scope); current != nil && !current.Before(resetAt) {
		return transaction.Commit()
	}

	payload := map[string]string{
		"rate_limited_at":     time.Now().UTC().Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.Format(time.RFC3339),
	}
	if len(reason) > 0 {
		if value := strings.TrimSpace(reason[0]); value != "" {
			payload["reason"] = value
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx,
		`UPDATE accounts SET
			extra = jsonb_set(
				jsonb_set(COALESCE(extra, '{}'::jsonb), '{model_rate_limits}'::text[], COALESCE(extra->'model_rate_limits', '{}'::jsonb), true),
				ARRAY['model_rate_limits', $1]::text[],
				$2::jsonb,
				true
			),
			updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL`,
		scope, raw, id,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAccountNotFound
	}
	if err := transaction.Commit(); err != nil {
		return err
	}

	if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		logger.LegacyPrintf("repository.account", "[SchedulerOutbox] enqueue extended model rate limit failed: account=%d err=%v", id, err)
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return nil
}

func modelRateLimitResetFromExtra(extra []byte, scope string) *time.Time {
	var record map[string]json.RawMessage
	if json.Unmarshal(extra, &record) != nil {
		return nil
	}
	var limits map[string]json.RawMessage
	if json.Unmarshal(record["model_rate_limits"], &limits) != nil {
		return nil
	}
	var limit map[string]json.RawMessage
	if json.Unmarshal(limits[scope], &limit) != nil {
		return nil
	}
	var reset string
	if json.Unmarshal(limit["rate_limit_reset_at"], &reset) != nil {
		return nil
	}
	reset = strings.TrimSpace(reset)
	if reset == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, reset)
	if err != nil {
		return nil
	}
	return &parsed
}
