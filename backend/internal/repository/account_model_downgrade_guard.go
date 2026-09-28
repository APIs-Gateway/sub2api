package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TryBlockDowngradedModel serializes the pool-wide cap and the scoped write.
// It never replaces an active rate limit from another source.
func (r *accountRepository) TryBlockDowngradedModel(ctx context.Context, id int64, requestedModel, model string, until time.Time, maxRatio float64, simpleMode bool, candidateFilter service.ModelDowngradeCandidateFilter) (bool, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || candidateFilter == nil {
		return false, nil
	}
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
	// Stabilize the global denominator through commit. An administrator can
	// otherwise disable unrelated accounts after the ratio check.
	if err := lockModelDowngradePool(ctx, tx, `
		SELECT a.id FROM accounts a
		WHERE a.platform = $1 AND a.status = 'active' AND a.schedulable = TRUE AND a.deleted_at IS NULL
		ORDER BY a.id FOR UPDATE OF a
	`, service.PlatformOpenAI); err != nil {
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

	// Decode timestamps in Go: a malformed value written by another limit
	// source must not turn the whole quarantine decision into a SQL error.
	rows, err := tx.QueryContext(ctx, `
		SELECT extra FROM accounts
		WHERE platform = $1 AND status = 'active' AND schedulable = TRUE AND deleted_at IS NULL
	`, service.PlatformOpenAI)
	if err != nil {
		return false, err
	}
	var blocked, total int64
	for rows.Next() {
		var candidateExtra []byte
		if err := rows.Scan(&candidateExtra); err != nil {
			_ = rows.Close()
			return false, err
		}
		total++
		var candidateState struct {
			ModelRateLimits map[string]json.RawMessage `json:"model_rate_limits"`
		}
		if err := json.Unmarshal(candidateExtra, &candidateState); err != nil {
			// Database JSONB is valid JSON, but a non-object legacy shape must
			// not make unrelated accounts block this transaction.
			continue
		}
		for _, rawLimit := range candidateState.ModelRateLimits {
			var limit struct {
				Reason           string `json:"reason"`
				RateLimitResetAt string `json:"rate_limit_reset_at"`
			}
			if json.Unmarshal(rawLimit, &limit) != nil || limit.Reason != service.ModelDowngradeGuardReason {
				continue
			}
			resetAt, parseErr := time.Parse(time.RFC3339, limit.RateLimitResetAt)
			if parseErr != nil || now.Before(resetAt) {
				blocked++
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if total == 0 || (!alreadyCounted && float64(blocked+1)/float64(total) > maxRatio) {
		return false, nil
	}
	// One account may serve several groups. Lock the candidate rows and their
	// group bindings before reading the repository projection on another DB
	// connection, so concurrent admin edits cannot remove the last candidate
	// between this check and the block commit.
	if simpleMode {
		candidates, err := r.ListSchedulableByPlatform(ctx, service.PlatformOpenAI)
		if err != nil {
			return false, err
		}
		if !hasAlternativeModelDowngradeCandidate(ctx, candidates, id, candidateFilter, nil) {
			return false, nil
		}
	} else if ok, err := r.hasAlternativeModelDowngradeCandidateInGroups(ctx, tx, id, candidateFilter); err != nil {
		return false, err
	} else if !ok {
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

func (r *accountRepository) hasAlternativeModelDowngradeCandidateInGroups(ctx context.Context, tx *sql.Tx, id int64, candidateFilter service.ModelDowngradeCandidateFilter) (bool, error) {
	groupRows, err := tx.QueryContext(ctx, "SELECT group_id FROM account_groups WHERE account_id = $1 ORDER BY group_id FOR UPDATE", id)
	if err != nil {
		return false, err
	}
	var groupIDs []int64
	for groupRows.Next() {
		var groupID int64
		if err := groupRows.Scan(&groupID); err != nil {
			_ = groupRows.Close()
			return false, err
		}
		groupIDs = append(groupIDs, groupID)
	}
	if err := groupRows.Err(); err != nil {
		_ = groupRows.Close()
		return false, err
	}
	if err := groupRows.Close(); err != nil {
		return false, err
	}
	// A shared account can serve a different channel alias in each group.
	// This request proves availability only for its own alias, so do not
	// quarantine an account shared by several groups.
	if len(groupIDs) > 1 {
		return false, nil
	}
	if len(groupIDs) == 0 {
		if err := lockModelDowngradePool(ctx, tx, `
			SELECT a.id FROM accounts a
			WHERE a.platform = $1 AND a.deleted_at IS NULL
				AND NOT EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = a.id)
			ORDER BY a.id FOR UPDATE OF a
		`, service.PlatformOpenAI); err != nil {
			return false, err
		}
		candidates, err := r.ListSchedulableUngroupedByPlatform(ctx, service.PlatformOpenAI)
		if err != nil {
			return false, err
		}
		if !hasAlternativeModelDowngradeCandidate(ctx, candidates, id, candidateFilter, nil) {
			return false, nil
		}
	}
	for _, groupID := range groupIDs {
		if err := lockModelDowngradePool(ctx, tx, `
			SELECT a.id FROM accounts a
			JOIN account_groups ag ON ag.account_id = a.id
			WHERE ag.group_id = $1 AND a.platform = $2 AND a.deleted_at IS NULL
			ORDER BY a.id FOR UPDATE OF a, ag
		`, groupID, service.PlatformOpenAI); err != nil {
			return false, err
		}
		candidates, err := r.ListSchedulableByGroupIDAndPlatform(ctx, groupID, service.PlatformOpenAI)
		if err != nil {
			return false, err
		}
		if !hasAlternativeModelDowngradeCandidate(ctx, candidates, id, candidateFilter, &groupID) {
			return false, nil
		}
	}
	return true, nil
}

func lockModelDowngradePool(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func hasAlternativeModelDowngradeCandidate(ctx context.Context, candidates []service.Account, blockedID int64, candidateFilter service.ModelDowngradeCandidateFilter, groupID *int64) bool {
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.ID != blockedID && candidateFilter(ctx, candidate, groupID) {
			return true
		}
	}
	return false
}

// ClearModelRateLimitsExceptDowngrade is used by automatic account recovery.
// A successful health test of another model must not release this guard early;
// explicit administrator ClearModelRateLimits still removes every entry.
func (r *accountRepository) ClearModelRateLimitsExceptDowngrade(ctx context.Context, id int64) error {
	result, err := r.sql.ExecContext(ctx, `
		UPDATE accounts SET extra = jsonb_set(
			COALESCE(extra, '{}'::jsonb), '{model_rate_limits}',
			COALESCE((
				SELECT jsonb_object_agg(model, payload)
				FROM jsonb_each(
					CASE WHEN jsonb_typeof(extra -> 'model_rate_limits') = 'object'
					THEN extra -> 'model_rate_limits' ELSE '{}'::jsonb END
				) AS limits(model, payload)
				WHERE payload ->> 'reason' = $2
					AND payload ->> 'rate_limit_reset_at' > $3
			), '{}'::jsonb), TRUE), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`, id, service.ModelDowngradeGuardReason, time.Now().UTC().Format(time.RFC3339))
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
	if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		logger.LegacyPrintf("repository.account", "[SchedulerOutbox] enqueue selective model rate limit clear failed: account=%d err=%v", id, err)
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return nil
}
