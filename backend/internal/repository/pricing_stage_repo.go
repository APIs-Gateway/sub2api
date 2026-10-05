package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// W6 PR5：阶段切换（group_model_config.pricing_stage）与影子差异样本（pricing_shadow_diffs）的存储。
// 手写 database/sql，与 pricing_matrix_repo.go 同风格。

type pricingStageStore struct {
	db *sql.DB
}

// NewPricingStageStore 创建阶段存储。
func NewPricingStageStore(db *sql.DB) service.PricingStageStore {
	return &pricingStageStore{db: db}
}

func (s *pricingStageStore) SwitchStage(ctx context.Context, groupID int64, to service.PricingStage, operatorID int64, now time.Time) (change *service.PricingStageChange, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	// 与派生钩子争同一把行锁时不无限等待。
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+pricingMatrixLockTimeout+"'"); err != nil {
		return nil, fmt.Errorf("set lock_timeout: %w", err)
	}

	var (
		from     string
		revision int64
	)
	err = tx.QueryRowContext(ctx,
		`SELECT c.pricing_stage, c.revision
		 FROM group_model_config c JOIN groups g ON g.id = c.group_id
		 WHERE c.group_id = $1 AND g.deleted_at IS NULL
		 FOR UPDATE OF c`, groupID).Scan(&from, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = classifyMissingStageRow(ctx, tx, groupID)
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("lock group_model_config: %w", err)
	}

	change = &service.PricingStageChange{
		GroupID: groupID, From: service.PricingStage(from), To: to, Revision: revision, ChangedAt: now,
	}
	if change.From != to {
		if _, err = tx.ExecContext(ctx,
			`UPDATE group_model_config
			 SET pricing_stage = $2, stage_changed_at = $3, stage_changed_by = $4, revision = revision + 1, updated_at = $3
			 WHERE group_id = $1`, groupID, string(to), now, operatorID); err != nil {
			return nil, fmt.Errorf("update pricing_stage: %w", err)
		}
		change.Changed = true
		change.Revision = revision + 1
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return change, nil
}

// classifyMissingStageRow 区分「分组不存在或已软删除」和「分组存在但还没有配置行」。
func classifyMissingStageRow(ctx context.Context, tx *sql.Tx, groupID int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM groups WHERE id = $1 AND deleted_at IS NULL)`, groupID).Scan(&exists); err != nil {
		return fmt.Errorf("check group: %w", err)
	}
	if !exists {
		return service.ErrGroupNotFound
	}
	return service.ErrPricingStageNotDerived
}

type pricingShadowStore struct {
	db *sql.DB
}

// NewPricingShadowStore 创建影子差异样本存储。
func NewPricingShadowStore(db *sql.DB) service.PricingShadowStore {
	return &pricingShadowStore{db: db}
}

func (s *pricingShadowStore) InsertDiffs(ctx context.Context, samples []service.PricingShadowSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO pricing_shadow_diffs (created_at, group_id, model, kind, class, usage_ref, legacy_view, v2_view)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7::jsonb, $8::jsonb)`)
	if err != nil {
		return fmt.Errorf("prepare insert pricing_shadow_diffs: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, sample := range samples {
		if _, err := stmt.ExecContext(ctx, sample.CreatedAt, sample.GroupID, sample.Model, sample.Kind, sample.Class,
			sample.UsageRef, string(sample.LegacyView), string(sample.V2View)); err != nil {
			return fmt.Errorf("insert pricing_shadow_diffs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *pricingShadowStore) PurgeDiffsBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pricing_shadow_diffs WHERE created_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("purge pricing_shadow_diffs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (s *pricingShadowStore) ListDiffs(ctx context.Context, groupID int64, limit int) ([]service.PricingShadowSample, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT created_at, group_id, model, kind, class, COALESCE(usage_ref, ''), legacy_view, v2_view
		 FROM pricing_shadow_diffs
		 WHERE ($1::bigint = 0 OR group_id = $1)
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2`, groupID, limit)
	if err != nil {
		return nil, fmt.Errorf("query pricing_shadow_diffs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []service.PricingShadowSample{}
	for rows.Next() {
		var sample service.PricingShadowSample
		var legacy, v2 []byte
		if err := rows.Scan(&sample.CreatedAt, &sample.GroupID, &sample.Model, &sample.Kind, &sample.Class,
			&sample.UsageRef, &legacy, &v2); err != nil {
			return nil, fmt.Errorf("scan pricing_shadow_diffs: %w", err)
		}
		sample.LegacyView, sample.V2View = legacy, v2
		out = append(out, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pricing_shadow_diffs: %w", err)
	}
	return out, nil
}
