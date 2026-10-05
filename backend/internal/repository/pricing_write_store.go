package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// pricingWriteStore 手写 database/sql 实现过渡审批的存储（service.PriceWriteStore）：
// 预览/审批记录表 pricing_write_approvals，以及单元格写入用的事务。
type pricingWriteStore struct {
	db *sql.DB
}

// NewPricingWriteStore 创建过渡审批存储。
func NewPricingWriteStore(db *sql.DB) service.PriceWriteStore {
	return &pricingWriteStore{db: db}
}

func (s *pricingWriteStore) Reader() service.MatrixExecutor { return s.db }

func (s *pricingWriteStore) WithTx(ctx context.Context, fn func(ctx context.Context, tx service.MatrixExecutor) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
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
	// 与派生钩子、阶段切换争同一把行锁时不无限等待。
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+pricingMatrixLockTimeout+"'"); err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}
	if err = fn(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *pricingWriteStore) InsertApproval(ctx context.Context, a service.PriceWriteApproval) (int64, error) {
	summary := a.Summary
	if len(summary) == 0 {
		summary = []byte("{}")
	}
	var id int64
	if err := s.db.QueryRowContext(ctx,
		`INSERT INTO pricing_write_approvals
		   (kind, plan_hash, touches_price, price_delta, group_ids, summary, previewed_by, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9) RETURNING id`,
		a.Kind, a.PlanHash, a.TouchesPrice, string(a.Delta), pq.Array(a.GroupIDs), string(summary), a.PreviewedBy,
		a.CreatedAt, a.ExpiresAt).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert pricing_write_approvals: %w", err)
	}
	return id, nil
}

func (s *pricingWriteStore) ConsumeApproval(ctx context.Context, tx service.MatrixExecutor, id int64, planHash, kind string, approverID int64, now time.Time) (*service.PriceWriteApproval, error) {
	rows, err := tx.QueryContext(ctx,
		`UPDATE pricing_write_approvals
		 SET status = 'consumed', approved_by = $2, consumed_at = $3
		 WHERE id = $1 AND status = 'previewed' AND plan_hash = $4 AND kind = $5 AND expires_at > $3
		 RETURNING touches_price, price_delta, previewed_by`,
		id, approverID, now, planHash, kind)
	if err != nil {
		return nil, fmt.Errorf("consume pricing_write_approvals: %w", err)
	}
	a := &service.PriceWriteApproval{ID: id, Kind: kind, PlanHash: planHash, ApprovedBy: approverID}
	consumed := rows.Next()
	if consumed {
		var delta string
		if err := rows.Scan(&a.TouchesPrice, &delta, &a.PreviewedBy); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan consumed approval: %w", err)
		}
		a.Delta = service.PriceDelta(delta)
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		return nil, fmt.Errorf("consume pricing_write_approvals: %w", rowsErr)
	}
	if consumed {
		return a, nil
	}
	return nil, s.classifyRejectedApproval(ctx, tx, id, planHash, kind, now)
}

// classifyRejectedApproval 没有消耗成功时，读出行的现状给出具体原因。
func (s *pricingWriteStore) classifyRejectedApproval(ctx context.Context, tx service.MatrixExecutor, id int64, planHash, kind string, now time.Time) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT status, plan_hash, kind, expires_at FROM pricing_write_approvals WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("query pricing_write_approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var (
		found                  = rows.Next()
		status, gotHash, gotKd string
		expiresAt              time.Time
	)
	if found {
		if err := rows.Scan(&status, &gotHash, &gotKd, &expiresAt); err != nil {
			return fmt.Errorf("scan pricing_write_approvals: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("query pricing_write_approvals: %w", err)
	}
	return service.ClassifyApprovalRejection(found, status, gotHash, gotKd, expiresAt, planHash, kind, now)
}

func (s *pricingWriteStore) PurgeStale(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pricing_write_approvals WHERE status = 'previewed' AND created_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("purge pricing_write_approvals: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}
