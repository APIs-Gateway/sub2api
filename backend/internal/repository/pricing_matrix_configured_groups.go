package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.ConfiguredGroupLister = (*pricingMatrixRepository)(nil)

// ListConfiguredGroupIDs 列出有 group_model_config 行、且分组未软删除的分组（W6 PR7a 启动预加载用）。
// 没有配置行的分组一律是 legacy，不需要预加载。实现 service.ConfiguredGroupLister。
func (r *pricingMatrixRepository) ListConfiguredGroupIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT c.group_id FROM group_model_config c
		 JOIN groups g ON g.id = c.group_id AND g.deleted_at IS NULL
		 ORDER BY c.group_id`)
	if err != nil {
		return nil, fmt.Errorf("query configured groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan configured group: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate configured groups: %w", err)
	}
	return out, nil
}
