package repository

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// pricingSnapshotMaxPayloadBytes 是解压后价格 JSON 的上限（LiteLLM 全量约 1.5 MB），防止损坏的行吃光内存。
var pricingSnapshotMaxPayloadBytes = 64 << 20

const pricingSnapshotMetaColumns = `id, label, source, source_url, content_sha256, model_count,
	parent_snapshot_id, candidate_snapshot_id, status, fetched_by, fetched_at, approved_by, approved_at, change_set_id, note`

// pricingSnapshotRepository 手写 database/sql 实现价格快照（pricing_snapshots）的访问。
type pricingSnapshotRepository struct {
	db *sql.DB
}

// NewPricingSnapshotRepository 创建价格快照仓储。
func NewPricingSnapshotRepository(db *sql.DB) service.PricingSnapshotRepository {
	return &pricingSnapshotRepository{db: db}
}

type pricingSnapshotRowScanner interface {
	Scan(dest ...any) error
}

func scanPricingSnapshotMeta(row pricingSnapshotRowScanner) (*service.PricingSnapshotMeta, error) {
	var m service.PricingSnapshotMeta
	var sha string
	var parent, candidate, fetchedBy, approvedBy, changeSetID sql.NullInt64
	var approvedAt sql.NullTime
	if err := row.Scan(&m.ID, &m.Label, &m.Source, &m.SourceURL, &sha, &m.ModelCount,
		&parent, &candidate, &m.Status, &fetchedBy, &m.FetchedAt, &approvedBy, &approvedAt, &changeSetID, &m.Note); err != nil {
		return nil, err
	}
	m.ContentSHA256 = sha
	m.ParentSnapshotID = nullInt64Ptr(parent)
	m.CandidateSnapshotID = nullInt64Ptr(candidate)
	m.FetchedBy = nullInt64Ptr(fetchedBy)
	m.ApprovedBy = nullInt64Ptr(approvedBy)
	m.ChangeSetID = nullInt64Ptr(changeSetID)
	if approvedAt.Valid {
		t := approvedAt.Time
		m.ApprovedAt = &t
	}
	return &m, nil
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func (r *pricingSnapshotRepository) GetActiveMeta(ctx context.Context) (*service.PricingSnapshotMeta, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+pricingSnapshotMetaColumns+` FROM pricing_snapshots WHERE status = 'active'`)
	m, err := scanPricingSnapshotMeta(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrPricingSnapshotNotFound
		}
		return nil, fmt.Errorf("query active pricing snapshot: %w", err)
	}
	return m, nil
}

func (r *pricingSnapshotRepository) GetPayload(ctx context.Context, id int64) ([]byte, error) {
	var gz []byte
	err := r.db.QueryRowContext(ctx, `SELECT payload_gz FROM pricing_snapshots WHERE id = $1`, id).Scan(&gz)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrPricingSnapshotNotFound
		}
		return nil, fmt.Errorf("query pricing snapshot payload: %w", err)
	}
	payload, err := gunzipPricingPayload(gz)
	if err != nil {
		return nil, fmt.Errorf("decode pricing snapshot %d payload: %w", id, err)
	}
	return payload, nil
}

func (r *pricingSnapshotRepository) ActivateNew(ctx context.Context, in service.NewPricingSnapshot) (*service.PricingSnapshotMeta, error) {
	if len(in.ContentSHA256) != 64 {
		return nil, fmt.Errorf("pricing snapshot content_sha256 must be 64 hex characters, got %d", len(in.ContentSHA256))
	}
	gz, err := gzipPricingPayload(in.Payload)
	if err != nil {
		return nil, fmt.Errorf("encode pricing snapshot payload: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin pricing snapshot activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return nil, fmt.Errorf("set lock_timeout: %w", err)
	}
	// 同一个事务里先把旧的生效行置 superseded，再把新行置 active；唯一索引兜住并发切换。
	if _, err := tx.ExecContext(ctx, `UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`); err != nil {
		return nil, fmt.Errorf("supersede active pricing snapshot: %w", err)
	}
	meta, err := insertActivePricingSnapshot(ctx, tx, in, gz)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			return nil, service.ErrPricingSnapshotConflict
		}
		return nil, fmt.Errorf("commit pricing snapshot activation: %w", err)
	}
	return meta, nil
}

// insertActivePricingSnapshot 插入一行并直接置 active（调用方已在同一事务里把旧的生效行置 superseded）。
func insertActivePricingSnapshot(ctx context.Context, tx *sql.Tx, in service.NewPricingSnapshot, gz []byte) (*service.PricingSnapshotMeta, error) {
	row := tx.QueryRowContext(ctx,
		`INSERT INTO pricing_snapshots
		   (label, source, source_url, content_sha256, model_count, payload_gz,
		    parent_snapshot_id, candidate_snapshot_id, status, fetched_by, approved_by, approved_at, note)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', $9, $10, NOW(), $11)
		 RETURNING `+pricingSnapshotMetaColumns,
		in.Label, in.Source, in.SourceURL, in.ContentSHA256, in.ModelCount, gz,
		in.ParentSnapshotID, in.CandidateSnapshotID, in.FetchedBy, in.ApprovedBy, in.Note)
	meta, err := scanPricingSnapshotMeta(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, service.ErrPricingSnapshotConflict
		}
		return nil, fmt.Errorf("insert active pricing snapshot: %w", err)
	}
	return meta, nil
}

func gzipPricingPayload(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipPricingPayload(gz []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(io.LimitReader(zr, int64(pricingSnapshotMaxPayloadBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(out) > pricingSnapshotMaxPayloadBytes {
		return nil, fmt.Errorf("payload exceeds %d bytes", pricingSnapshotMaxPayloadBytes)
	}
	return out, nil
}
