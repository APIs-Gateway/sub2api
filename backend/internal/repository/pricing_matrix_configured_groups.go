package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.ConfiguredGroupLister = (*pricingMatrixRepository)(nil)

// ListConfiguredGroups 列出有 group_model_config 行、且分组未软删除的分组及其阶段（W6 PR7a 启动预加载用）。
// 没有配置行的分组一律是 legacy，不需要预加载；阶段用来区分「可能是 v2」的分组（W6 PR7b-1 审查 B1）。
// 实现 service.ConfiguredGroupLister。
func (r *pricingMatrixRepository) ListConfiguredGroups(ctx context.Context) ([]service.ConfiguredGroup, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT c.group_id, c.pricing_stage FROM group_model_config c
		 JOIN groups g ON g.id = c.group_id AND g.deleted_at IS NULL
		 ORDER BY c.group_id`)
	if err != nil {
		return nil, fmt.Errorf("query configured groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []service.ConfiguredGroup
	for rows.Next() {
		var g service.ConfiguredGroup
		var stage string
		if err := rows.Scan(&g.ID, &stage); err != nil {
			return nil, fmt.Errorf("scan configured group: %w", err)
		}
		g.Stage = service.PricingStage(stage)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate configured groups: %w", err)
	}
	return out, nil
}
