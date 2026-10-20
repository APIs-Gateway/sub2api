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

// AllowlistGroupIDsTx 返回所有未删除、且在 v2 阶段的白名单分组 ID，在调用方的事务里读。
// 只看 v2：矩阵对 legacy、shadow 分组不生效（运行时按渠道计价），它们的 open 单元格有无价格不影响任何请求；
// 已知免费名单的写入（AllowlistOpenCellsTx）同样只看 v2 分组，两处范围保持一致。
func (pricingSnapshotExposureSource) AllowlistGroupIDsTx(ctx context.Context, exec service.MatrixExecutor) ([]int64, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT c.group_id
		 FROM group_model_config c
		 JOIN groups g ON g.id = c.group_id
		 WHERE c.access_mode = $1 AND c.pricing_stage = $2 AND g.deleted_at IS NULL
		 ORDER BY c.group_id`, string(service.MatrixAccessAllowlist), string(service.PricingStageV2))
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
