package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// pricingKnownFreeStore 是「已知免费」名单的存取：名单存在 settings 表的 billing_known_free_list 键里。
// 写入走事务里的直接 SQL，不经 settingRepository（那里拒绝受保护的键，见 setting_repo.go）。
type pricingKnownFreeStore struct{}

// NewPricingKnownFreeStore 创建名单存储。
func NewPricingKnownFreeStore() service.KnownFreeListStore { return pricingKnownFreeStore{} }

// GetKnownFreeListTx 读名单原文；没有这一行返回空串。读的时候锁住这一行（没有这一行时没有可锁的，
// 由写入者的 upsert 在提交时竞争，最坏是后写覆盖先写，两者都已通过校验）。
func (pricingKnownFreeStore) GetKnownFreeListTx(ctx context.Context, exec service.MatrixExecutor) (string, error) {
	rows, err := exec.QueryContext(ctx, `SELECT value FROM settings WHERE key = $1 FOR UPDATE`, service.SettingKeyBillingKnownFreeList)
	if err != nil {
		return "", fmt.Errorf("query known free list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var raw string
	if rows.Next() {
		if err := rows.Scan(&raw); err != nil {
			return "", fmt.Errorf("scan known free list: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate known free list: %w", err)
	}
	return raw, nil
}

// SetKnownFreeListTx 写名单原文（upsert）。
func (pricingKnownFreeStore) SetKnownFreeListTx(ctx context.Context, tx service.MatrixTx, raw string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, NOW())
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		service.SettingKeyBillingKnownFreeList, raw)
	if err != nil {
		return fmt.Errorf("upsert known free list: %w", err)
	}
	return nil
}

// AllowlistOpenCellsTx 返回所有 v2 白名单分组里 open 的单元格；lock 为真时先按分组 id 升序锁住这些分组的配置行，
// 与单元格写入器、阶段切换互斥。
func (pricingKnownFreeStore) AllowlistOpenCellsTx(ctx context.Context, exec service.MatrixExecutor, lock bool) ([]service.ExposureCell, error) {
	q := `SELECT c.group_id
		 FROM group_model_config c
		 JOIN groups g ON g.id = c.group_id
		 WHERE c.access_mode = $1 AND c.pricing_stage = $2 AND g.deleted_at IS NULL
		 ORDER BY c.group_id`
	if lock {
		q += ` FOR UPDATE OF c`
	}
	rows, err := exec.QueryContext(ctx, q, string(service.MatrixAccessAllowlist), string(service.PricingStageV2))
	if err != nil {
		return nil, fmt.Errorf("query allowlist v2 groups: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan allowlist v2 group: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate allowlist v2 groups: %w", err)
	}
	_ = rows.Close()
	if len(ids) == 0 {
		return nil, nil
	}
	return pricingExposureReader{}.OpenCellsTx(ctx, exec, ids)
}

// pricingCatalogStatusStore 实现 service.ModelCatalogStatusStore：目录条目的读取、状态更新与 7 天用量统计。
type pricingCatalogStatusStore struct {
	db *sql.DB
}

// NewModelCatalogStatusStore 创建目录状态存储。
func NewModelCatalogStatusStore(db *sql.DB) service.ModelCatalogStatusStore {
	return &pricingCatalogStatusStore{db: db}
}

// GetByID 读一个目录条目；不存在返回 service.ErrModelCatalogNotFound。
func (s *pricingCatalogStatusStore) GetByID(ctx context.Context, id int64) (*service.ModelCatalogEntry, error) {
	var (
		e         service.ModelCatalogEntry
		aliases   pq.StringArray
		reference sql.NullString
		status    string
		createdBy sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, model_key, platform, display_name, aliases, reference_model, status, note, created_by, created_at, updated_at
		 FROM model_catalog WHERE id = $1`, id,
	).Scan(&e.ID, &e.ModelKey, &e.Platform, &e.DisplayName, &aliases, &reference, &status, &e.Note, &createdBy, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrModelCatalogNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get model_catalog: %w", err)
	}
	e.Aliases = []string(aliases)
	if reference.Valid {
		v := reference.String
		e.ReferenceModel = &v
	}
	e.Status = service.ModelCatalogStatus(status)
	if createdBy.Valid {
		v := createdBy.Int64
		e.CreatedBy = &v
	}
	return &e, nil
}

// UpdateStatus 把状态从 from 改成 to；条目的状态已经不是 from（被别人改过）时返回 false。
func (s *pricingCatalogStatusStore) UpdateStatus(ctx context.Context, id int64, from, to service.ModelCatalogStatus) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE model_catalog SET status = $3, updated_at = NOW() WHERE id = $1 AND status = $2`,
		id, string(from), string(to))
	if err != nil {
		return false, fmt.Errorf("update model_catalog status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n > 0, nil
}

// CountModelUsageSince 统计自 since 以来 models 里任一名字出现在 model 或 requested_model 上的用量。
// 两个列各有索引，分开查再合并，不用 OR，避免退化成全表扫描。
func (s *pricingCatalogStatusStore) CountModelUsageSince(ctx context.Context, models []string, since time.Time) (service.CatalogUsage, error) {
	out := service.CatalogUsage{}
	if len(models) == 0 {
		return out, nil
	}
	var count int64
	var last sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(n), 0), MAX(last_at) FROM (
		   SELECT COUNT(*) AS n, MAX(created_at) AS last_at FROM usage_logs
		    WHERE model = ANY($1) AND created_at >= $2
		   UNION ALL
		   SELECT COUNT(*) AS n, MAX(created_at) AS last_at FROM usage_logs
		    WHERE requested_model = ANY($1) AND created_at >= $2
		 ) u`, pq.Array(models), since,
	).Scan(&count, &last)
	if err != nil {
		return out, fmt.Errorf("count model usage: %w", err)
	}
	out.Requests = count
	if last.Valid {
		t := last.Time
		out.LastUsedAt = &t
	}
	return out, nil
}
