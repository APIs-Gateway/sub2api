package repository

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// apiKeyGroupRouteExtras 是回退链表在 PR1 仓储之外的两个操作：按分组反查与删除 Key 时清理。
// 单独成类型，避免改动 PR1 已提交的仓储接口。
type apiKeyGroupRouteExtras struct {
	db *sql.DB
}

// NewAPIKeyGroupRouteExtras 创建回退链附加仓储。
func NewAPIKeyGroupRouteExtras(db *sql.DB) service.APIKeyGroupRouteExtras {
	return &apiKeyGroupRouteExtras{db: db}
}

// ListByGroup 返回把某分组放进链里的条目。JOIN api_keys 过滤已软删除的 Key。
func (r *apiKeyGroupRouteExtras) ListByGroup(ctx context.Context, groupID int64, limit int) ([]service.GroupRouteRef, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.api_key_id, k.user_id, r.source, r.placement
		FROM api_key_group_routes r
		JOIN api_keys k ON k.id = r.api_key_id AND k.deleted_at IS NULL
		WHERE r.group_id = $1
		ORDER BY r.api_key_id, r.source, r.placement, r.position
		LIMIT $2`, groupID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []service.GroupRouteRef
	for rows.Next() {
		var ref service.GroupRouteRef
		if err := rows.Scan(&ref.KeyID, &ref.UserID, &ref.Source, &ref.Placement); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// DeleteByKey 删除某把 Key 的全部链项。
func (r *apiKeyGroupRouteExtras) DeleteByKey(ctx context.Context, keyID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM api_key_group_routes WHERE api_key_id = $1`, keyID)
	return err
}
