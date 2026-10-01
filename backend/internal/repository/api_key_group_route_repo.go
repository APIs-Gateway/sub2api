package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// apiKeyGroupRouteRepository 手写 database/sql 实现 Key 级分组回退链表（api_key_group_routes）。
// 不走 ent：与 legacy_invite_claims、checkin_records 同风格。
type apiKeyGroupRouteRepository struct {
	db *sql.DB
}

// NewAPIKeyGroupRouteRepository 创建回退链仓储。
func NewAPIKeyGroupRouteRepository(db *sql.DB) service.APIKeyGroupRouteRepository {
	return &apiKeyGroupRouteRepository{db: db}
}

// 读取时 JOIN api_keys / groups 过滤软删除：ON DELETE CASCADE 只对物理删除生效。
const apiKeyGroupRouteSelect = `
	SELECT r.id, r.api_key_id, r.group_id, r.platform, r.source, r.placement,
	       r.position, r.note, r.created_by, r.created_at, r.updated_at
	FROM api_key_group_routes r
	JOIN api_keys k ON k.id = r.api_key_id AND k.deleted_at IS NULL
	JOIN groups g ON g.id = r.group_id AND g.deleted_at IS NULL
	WHERE r.api_key_id = $1`

func (r *apiKeyGroupRouteRepository) ListByKey(ctx context.Context, keyID int64, source string) ([]service.RouteItem, error) {
	query := apiKeyGroupRouteSelect
	args := []any{keyID}
	if source != "" {
		query += " AND r.source = $2"
		args = append(args, source)
	}
	query += " ORDER BY r.source, r.placement, r.position, r.id"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []service.RouteItem
	for rows.Next() {
		item, err := scanAPIKeyGroupRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanAPIKeyGroupRoute(rows *sql.Rows) (service.RouteItem, error) {
	var (
		it        service.RouteItem
		note      sql.NullString
		createdBy sql.NullInt64
	)
	if err := rows.Scan(&it.ID, &it.APIKeyID, &it.GroupID, &it.Platform, &it.Source, &it.Placement,
		&it.Position, &note, &createdBy, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return service.RouteItem{}, err
	}
	if note.Valid {
		it.Note = note.String
	}
	if createdBy.Valid {
		v := createdBy.Int64
		it.CreatedBy = &v
	}
	return it, nil
}

// lockAPIKeyRow 锁住 api_keys 行并返回当前主分组；Key 不存在或已软删除返回 ErrAPIKeyNotFound。
func lockAPIKeyRow(ctx context.Context, tx *sql.Tx, keyID int64) (sql.NullInt64, error) {
	var groupID sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT group_id FROM api_keys WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, keyID,
	).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return groupID, service.ErrAPIKeyNotFound
	}
	return groupID, err
}

func insertAPIKeyGroupRoute(ctx context.Context, tx *sql.Tx, keyID int64, it service.RouteItem) error {
	var note sql.NullString
	if it.Note != "" {
		note = sql.NullString{String: it.Note, Valid: true}
	}
	var createdBy sql.NullInt64
	if it.CreatedBy != nil {
		createdBy = sql.NullInt64{Int64: *it.CreatedBy, Valid: true}
	}
	if !it.CreatedAt.IsZero() {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO api_key_group_routes
				(api_key_id, group_id, platform, source, placement, position, note, created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			keyID, it.GroupID, it.Platform, it.Source, it.Placement, it.Position, note, createdBy, it.CreatedAt)
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO api_key_group_routes
			(api_key_id, group_id, platform, source, placement, position, note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		keyID, it.GroupID, it.Platform, it.Source, it.Placement, it.Position, note, createdBy)
	return err
}

func (r *apiKeyGroupRouteRepository) ReplaceChain(ctx context.Context, p service.ReplaceRoutesParams) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	currentGroupID, err := lockAPIKeyRow(ctx, tx, p.APIKeyID)
	if err != nil {
		return err
	}
	if p.ExpectedPrimaryGroupID != 0 && (!currentGroupID.Valid || currentGroupID.Int64 != p.ExpectedPrimaryGroupID) {
		return service.ErrFallbackKeyChanged
	}

	if _, err = tx.ExecContext(ctx,
		`DELETE FROM api_key_group_routes WHERE api_key_id = $1 AND source = $2`,
		p.APIKeyID, p.Source); err != nil {
		return err
	}
	for _, it := range p.Items {
		it.Source = p.Source
		if err = insertAPIKeyGroupRoute(ctx, tx, p.APIKeyID, it); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *apiKeyGroupRouteRepository) ApplyPrimaryGroupChange(ctx context.Context, keyID, newGroupID int64, newPlatform string) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = lockAPIKeyRow(ctx, tx, keyID); err != nil {
		return err
	}

	// 这里读全部行（不 JOIN 软删除过滤）：压实 position 时要连带保留软删除分组的行，
	// 否则会被误删。
	rows, err := tx.QueryContext(ctx, `
		SELECT id, api_key_id, group_id, platform, source, placement, position, note, created_by, created_at, updated_at
		FROM api_key_group_routes WHERE api_key_id = $1 ORDER BY id`, keyID)
	if err != nil {
		return err
	}
	var current []service.RouteItem
	for rows.Next() {
		it, scanErr := scanAPIKeyGroupRoute(rows)
		if scanErr != nil {
			_ = rows.Close()
			err = scanErr
			return err
		}
		current = append(current, it)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	kept, changed := planPrimaryGroupChange(current, newGroupID, newPlatform)
	if !changed {
		return tx.Commit()
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM api_key_group_routes WHERE api_key_id = $1`, keyID); err != nil {
		return err
	}
	for _, it := range kept {
		if err = insertAPIKeyGroupRoute(ctx, tx, keyID, it); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// planPrimaryGroupChange 是 ApplyPrimaryGroupChange 的纯函数部分：
// 删除与新主分组相同的项；新平台与 user 项平台不一致时清掉这些 user 项；
// 剩余项按 (platform, source, placement) 分区把 position 压实为从 0 起连续。
// changed 为 false 表示没有任何项被删除，无需改库。
func planPrimaryGroupChange(current []service.RouteItem, newGroupID int64, newPlatform string) (kept []service.RouteItem, changed bool) {
	for _, it := range current {
		if it.GroupID == newGroupID {
			changed = true
			continue
		}
		if it.Source == service.RouteSourceUser && it.Platform != newPlatform {
			changed = true
			continue
		}
		kept = append(kept, it)
	}
	if !changed {
		return current, false
	}

	type partition struct{ platform, source, placement string }
	groups := make(map[partition][]int)
	for i, it := range kept {
		k := partition{it.Platform, it.Source, it.Placement}
		groups[k] = append(groups[k], i)
	}
	for _, idxs := range groups {
		sort.SliceStable(idxs, func(a, b int) bool {
			ia, ib := kept[idxs[a]], kept[idxs[b]]
			if ia.Position != ib.Position {
				return ia.Position < ib.Position
			}
			return ia.ID < ib.ID
		})
		for pos, idx := range idxs {
			kept[idx].Position = pos
		}
	}
	return kept, true
}
