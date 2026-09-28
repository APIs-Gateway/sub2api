package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TryBlockDowngradedModel serializes the pool-wide cap and the scoped write.
// It never replaces an active rate limit from another source.
func (r *accountRepository) TryBlockDowngradedModel(ctx context.Context, id int64, model string, until time.Time, maxRatio float64) (bool, error) {
	beginner, ok := r.sql.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return false, errors.New("model downgrade guard requires a transactional SQL executor")
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockHash("model_downgrade_guard:ratio_cap")); err != nil {
		return false, err
	}

	var extra []byte
	err = tx.QueryRowContext(ctx, `
		SELECT extra FROM accounts
		WHERE id = $1 AND platform = $2 AND status = 'active' AND schedulable = TRUE AND deleted_at IS NULL
		FOR UPDATE
	`, id, service.PlatformOpenAI).Scan(&extra)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var state struct {
		ModelRateLimits map[string]struct {
			Reason           string `json:"reason"`
			RateLimitResetAt string `json:"rate_limit_reset_at"`
		} `json:"model_rate_limits"`
	}
	if len(extra) > 0 {
		if err := json.Unmarshal(extra, &state); err != nil {
			return false, err
		}
	}
	now := time.Now().UTC()
	alreadyCounted := false
	for key, limit := range state.ModelRateLimits {
		resetAt, err := time.Parse(time.RFC3339, limit.RateLimitResetAt)
		if err != nil {
			if key == model {
				// Unknown state is safer than overwriting another source's limit.
				return false, nil
			}
			continue
		}
		if key == model && now.Before(resetAt) {
			return false, nil
		}
		if limit.Reason == service.ModelDowngradeGuardReason && now.Before(resetAt) {
			alreadyCounted = true
		}
	}

	var blocked, total int64
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE EXISTS (
			SELECT 1 FROM jsonb_each(
				CASE WHEN jsonb_typeof(extra -> 'model_rate_limits') = 'object'
				THEN extra -> 'model_rate_limits' ELSE '{}'::jsonb END
			) AS limits(model, payload)
			WHERE payload ->> 'reason' = $1
				AND (payload ->> 'rate_limit_reset_at')::timestamptz > $2
		)), COUNT(*)
		FROM accounts
		WHERE platform = $3 AND status = 'active' AND schedulable = TRUE AND deleted_at IS NULL
	`, service.ModelDowngradeGuardReason, now, service.PlatformOpenAI).Scan(&blocked, &total)
	if err != nil {
		return false, err
	}
	if total == 0 || (!alreadyCounted && float64(blocked+1)/float64(total) > maxRatio) {
		return false, nil
	}
	payload, err := json.Marshal(map[string]string{
		"rate_limited_at":     now.Format(time.RFC3339),
		"rate_limit_reset_at": until.UTC().Format(time.RFC3339),
		"reason":              service.ModelDowngradeGuardReason,
	})
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE accounts SET extra = jsonb_set(
			jsonb_set(COALESCE(extra, '{}'::jsonb), '{model_rate_limits}',
				CASE WHEN jsonb_typeof(extra -> 'model_rate_limits') = 'object'
				THEN extra -> 'model_rate_limits' ELSE '{}'::jsonb END, TRUE),
			ARRAY['model_rate_limits', $1]::text[], $2::jsonb, TRUE),
			updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL
	`, model, payload, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, service.ErrAccountNotFound
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return true, nil
}
