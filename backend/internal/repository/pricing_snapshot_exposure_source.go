package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// pricingSnapshotExposureSource 是价格快照批准时保存时校验用的读取器：
// 复用写入路径的 ExposureReader（准入模式、open 单元格），再补一个「列出全部白名单分组」的查询。
type pricingSnapshotExposureSource struct {
	service.ExposureReader
}

// NewPricingSnapshotExposureSource 创建快照批准校验用的读取器。
func NewPricingSnapshotExposureSource() service.SnapshotExposureReader {
	return pricingSnapshotExposureSource{ExposureReader: NewPricingExposureReader()}
}

// AllowlistGroupIDsTx 返回所有未删除的白名单分组 ID，在调用方的事务里读。
func (pricingSnapshotExposureSource) AllowlistGroupIDsTx(ctx context.Context, exec service.MatrixExecutor) ([]int64, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT c.group_id
		 FROM group_model_config c
		 JOIN groups g ON g.id = c.group_id
		 WHERE c.access_mode = $1 AND g.deleted_at IS NULL
		 ORDER BY c.group_id`, string(service.MatrixAccessAllowlist))
	if err != nil {
		return nil, fmt.Errorf("query allowlist groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan allowlist group: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate allowlist groups: %w", err)
	}
	return ids, nil
}
