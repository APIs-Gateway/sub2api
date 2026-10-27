package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

const (
	pricingSnapshotListMaxLimit = 200
	// pricingSnapshotRecentModelsLimit 封顶，防止异常数据让差异页的逐名报价失控。
	pricingSnapshotRecentModelsLimit   = 5000
	pricingSnapshotRecentModelsTimeout = "15s"
)

func (r *pricingSnapshotRepository) GetMeta(ctx context.Context, id int64) (*service.PricingSnapshotMeta, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+pricingSnapshotMetaColumns+` FROM pricing_snapshots WHERE id = $1`, id)
	m, err := scanPricingSnapshotMeta(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrPricingSnapshotNotFound
		}
		return nil, fmt.Errorf("query pricing snapshot: %w", err)
	}
	return m, nil
}

func (r *pricingSnapshotRepository) List(ctx context.Context, statuses []string, limit int) ([]service.PricingSnapshotMeta, error) {
	if limit <= 0 || limit > pricingSnapshotListMaxLimit {
		limit = pricingSnapshotListMaxLimit
	}
	if statuses == nil {
		statuses = []string{}
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+pricingSnapshotMetaColumns+` FROM pricing_snapshots
		 WHERE (cardinality($1::text[]) = 0 OR status = ANY($1::text[]))
		 ORDER BY fetched_at DESC, id DESC LIMIT $2`, pq.Array(statuses), limit)
	if err != nil {
		return nil, fmt.Errorf("list pricing snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []service.PricingSnapshotMeta{}
	for rows.Next() {
		m, err := scanPricingSnapshotMeta(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pricing snapshot: %w", err)
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pricing snapshots: %w", err)
	}
	return out, nil
}

func (r *pricingSnapshotRepository) InsertCandidate(ctx context.Context, in service.NewPricingSnapshot) (*service.PricingSnapshotMeta, bool, error) {
	if len(in.ContentSHA256) != 64 {
		return nil, false, fmt.Errorf("pricing snapshot content_sha256 must be 64 hex characters, got %d", len(in.ContentSHA256))
	}
	gz, err := gzipPricingPayload(in.Payload)
	if err != nil {
		return nil, false, fmt.Errorf("encode pricing snapshot payload: %w", err)
	}
	// 同一内容的候选只保留一份（部分唯一索引）：冲突时什么也不写，改读已有的那份。
	row := r.db.QueryRowContext(ctx,
		`INSERT INTO pricing_snapshots
		   (label, source, source_url, content_sha256, model_count, payload_gz,
		    parent_snapshot_id, candidate_snapshot_id, status, fetched_by, note)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'candidate', $9, $10)
		 ON CONFLICT (content_sha256) WHERE status = 'candidate' DO NOTHING
		 RETURNING `+pricingSnapshotMetaColumns,
		in.Label, in.Source, in.SourceURL, in.ContentSHA256, in.ModelCount, gz,
		in.ParentSnapshotID, in.CandidateSnapshotID, in.FetchedBy, in.Note)
	meta, err := scanPricingSnapshotMeta(row)
	if err == nil {
		return meta, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("insert pricing snapshot candidate: %w", err)
	}
	existing, err := scanPricingSnapshotMeta(r.db.QueryRowContext(ctx,
		`SELECT `+pricingSnapshotMetaColumns+` FROM pricing_snapshots WHERE content_sha256 = $1 AND status = 'candidate'`, in.ContentSHA256))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 冲突的那份在两条语句之间被清理或批准了：交给调用方重试。
			return nil, false, service.ErrPricingSnapshotConflict
		}
		return nil, false, fmt.Errorf("query existing pricing snapshot candidate: %w", err)
	}
	return existing, false, nil
}

func (r *pricingSnapshotRepository) RejectCandidate(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE pricing_snapshots SET status = 'rejected' WHERE id = $1 AND status = 'candidate'`, id)
	if err != nil {
		return fmt.Errorf("reject pricing snapshot candidate: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reject pricing snapshot candidate: %w", err)
	}
	if n == 0 {
		return service.ErrPricingSnapshotNotCandidate
	}
	return nil
}

func (r *pricingSnapshotRepository) DeleteExpiredCandidates(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM pricing_snapshots WHERE status IN ('candidate', 'rejected') AND fetched_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete expired pricing snapshot candidates: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired pricing snapshot candidates: %w", err)
	}
	return n, nil
}

func (r *pricingSnapshotRepository) ApplyMerged(ctx context.Context, req service.ApplyMergedSnapshot) (*service.PricingSnapshotMeta, error) {
	in := req.New
	if len(in.ContentSHA256) != 64 {
		return nil, fmt.Errorf("pricing snapshot content_sha256 must be 64 hex characters, got %d", len(in.ContentSHA256))
	}
	gz, err := gzipPricingPayload(in.Payload)
	if err != nil {
		return nil, fmt.Errorf("encode pricing snapshot payload: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin pricing snapshot approval: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return nil, fmt.Errorf("set lock_timeout: %w", err)
	}
	// 锁住生效行并核对基线：预览之后若有人切换了生效快照，整个批准失败。
	var activeID int64
	var activeSHA string
	if err := tx.QueryRowContext(ctx, `SELECT id, content_sha256 FROM pricing_snapshots WHERE status = 'active' FOR UPDATE`).Scan(&activeID, &activeSHA); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrPricingSnapshotBaselineChanged
		}
		return nil, fmt.Errorf("lock active pricing snapshot: %w", err)
	}
	if activeID != req.ExpectedActiveID || !strings.EqualFold(activeSHA, req.ExpectedActiveSHA) {
		return nil, service.ErrPricingSnapshotBaselineChanged
	}
	// 同一个事务里先把旧行置 superseded，再把新行置 active。
	if _, err := tx.ExecContext(ctx, `UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`); err != nil {
		return nil, fmt.Errorf("supersede active pricing snapshot: %w", err)
	}
	meta, err := insertActivePricingSnapshot(ctx, tx, in, gz)
	if err != nil {
		return nil, err
	}

	if in.CandidateSnapshotID != nil {
		for _, d := range req.Diffs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO pricing_snapshot_diffs
				   (snapshot_id, base_snapshot_id, model_key, change_type, old_price, new_price, changed_fields, decision)
				 VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8)
				 ON CONFLICT (snapshot_id, model_key) DO UPDATE SET
				   base_snapshot_id = EXCLUDED.base_snapshot_id, change_type = EXCLUDED.change_type,
				   old_price = EXCLUDED.old_price, new_price = EXCLUDED.new_price,
				   changed_fields = EXCLUDED.changed_fields, decision = EXCLUDED.decision`,
				*in.CandidateSnapshotID, req.ExpectedActiveID, d.ModelKey, d.ChangeType,
				nullableJSONText(d.OldPrice), nullableJSONText(d.NewPrice), pq.Array(nonNilStrings(d.ChangedFields)), d.Decision); err != nil {
				return nil, fmt.Errorf("write pricing snapshot diff %q: %w", d.ModelKey, err)
			}
		}
		if req.ConsumeCandidate {
			if _, err := tx.ExecContext(ctx,
				`UPDATE pricing_snapshots SET status = 'superseded' WHERE id = $1 AND status = 'candidate'`, *in.CandidateSnapshotID); err != nil {
				return nil, fmt.Errorf("consume pricing snapshot candidate: %w", err)
			}
		}
	}

	// 保存时校验：与上面的写入在同一个事务连接里，错误原样返回（调用方按类型判断），整体回滚。
	if req.Check != nil {
		if err := req.Check(ctx, tx); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			return nil, service.ErrPricingSnapshotConflict
		}
		return nil, fmt.Errorf("commit pricing snapshot approval: %w", err)
	}
	return meta, nil
}

func (r *pricingSnapshotRepository) RecentBillingModels(ctx context.Context, days int) ([]string, error) {
	if days <= 0 {
		days = 7
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin recent models read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '"+pricingSnapshotRecentModelsTimeout+"'"); err != nil {
		return nil, fmt.Errorf("set statement_timeout: %w", err)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT model FROM usage_logs
		 WHERE created_at >= NOW() - ($1::int * INTERVAL '1 day') AND model <> ''
		 LIMIT $2`, days, pricingSnapshotRecentModelsLimit)
	if err != nil {
		return nil, fmt.Errorf("query recent billing models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("scan recent billing model: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent billing models: %w", err)
	}
	return out, nil
}

// nullableJSONText 把 JSON 原文交给 $n::jsonb；空表示 SQL NULL。
func nullableJSONText(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
