package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// pricingCellWriter 手写 database/sql 实现单元格写入（service.CellWriter）。
// 它没有状态：连接和事务由调用方传入的 service.MatrixExecutor 决定（*sql.Tx 或 *ent.Tx）。
type pricingCellWriter struct{}

// NewPricingCellWriter 创建单元格写入器。
func NewPricingCellWriter() service.CellWriter { return pricingCellWriter{} }

func (pricingCellWriter) PlanTx(ctx context.Context, exec service.MatrixExecutor, req service.CellWriteRequest) ([]service.PlannedCellWrite, error) {
	_, planned, err := prepareCellWrite(ctx, exec, req, false)
	return planned, err
}

func (pricingCellWriter) ApplyTx(ctx context.Context, tx service.MatrixTx, req service.CellWriteRequest) (*service.CellWriteResult, error) {
	norm, planned, err := prepareCellWrite(ctx, tx, req, true)
	if err != nil {
		return nil, err
	}
	res := &service.CellWriteResult{Planned: planned, TouchesPrice: service.PlannedTouchesPrice(planned)}
	changed := make(map[int64]struct{})
	for _, p := range planned {
		if p.Action == service.CellWriteNoop {
			continue
		}
		if err := writeMatrixCell(ctx, tx, p); err != nil {
			return nil, err
		}
		if err := insertMatrixCellHistory(ctx, tx, p, norm); err != nil {
			return nil, err
		}
		if _, seen := changed[p.Op.GroupID]; !seen {
			changed[p.Op.GroupID] = struct{}{}
			res.ChangedGroupIDs = append(res.ChangedGroupIDs, p.Op.GroupID)
		}
	}
	if len(res.ChangedGroupIDs) > 0 {
		// 分组配置 revision 是 HasPrice 缓存键的一部分：单元格变了，它也要变。
		if _, err := tx.ExecContext(ctx,
			`UPDATE group_model_config SET revision = revision + 1, updated_at = NOW() WHERE group_id = ANY($1)`,
			pq.Array(res.ChangedGroupIDs)); err != nil {
			return nil, fmt.Errorf("bump group_model_config revision: %w", err)
		}
	}
	return res, nil
}

// prepareCellWrite 规范请求、（lock 时加锁并）核对分组、读取现状并规划。
func prepareCellWrite(ctx context.Context, exec service.MatrixExecutor, req service.CellWriteRequest, lock bool) (service.CellWriteRequest, []service.PlannedCellWrite, error) {
	norm, err := service.NormalizeCellWriteRequest(req)
	if err != nil {
		return norm, nil, err
	}
	groupIDs := service.CellWriteGroupIDs(norm.Ops)
	states, err := loadCellGroupStates(ctx, exec, groupIDs, lock)
	if err != nil {
		return norm, nil, err
	}
	if err := service.CheckWritableGroups(groupIDs, states, norm.GroupRevisions); err != nil {
		return norm, nil, err
	}
	existing, err := loadCellsForOps(ctx, exec, norm.Ops)
	if err != nil {
		return norm, nil, err
	}
	planned, err := service.PlanCellWrites(norm.Ops, existing)
	return norm, planned, err
}

// loadCellGroupStates 读取分组的阶段与配置 revision。lock 为真时按 group_id 升序 FOR UPDATE（只锁配置行），
// 与派生钩子、阶段切换取同一把锁。已软删除的分组不返回。
func loadCellGroupStates(ctx context.Context, exec service.MatrixExecutor, groupIDs []int64, lock bool) (map[int64]service.CellGroupState, error) {
	q := `SELECT c.group_id, c.pricing_stage, c.revision
	      FROM group_model_config c
	      JOIN groups g ON g.id = c.group_id AND g.deleted_at IS NULL
	      WHERE c.group_id = ANY($1) ORDER BY c.group_id`
	if lock {
		q += ` FOR UPDATE OF c`
	}
	rows, err := exec.QueryContext(ctx, q, pq.Array(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("query group_model_config states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]service.CellGroupState, len(groupIDs))
	for rows.Next() {
		var (
			id    int64
			stage string
			st    service.CellGroupState
		)
		if err := rows.Scan(&id, &stage, &st.Revision); err != nil {
			return nil, fmt.Errorf("scan group_model_config state: %w", err)
		}
		st.Stage = service.PricingStage(stage)
		out[id] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group_model_config states: %w", err)
	}
	return out, nil
}

// loadCellsForOps 读取操作涉及的精确模型名单元格（不含通配符单元格）。
func loadCellsForOps(ctx context.Context, exec service.MatrixExecutor, ops []service.CellOp) (map[int64][]service.StoredMatrixCell, error) {
	groups := make([]int64, len(ops))
	keys := make([]string, len(ops))
	for i, op := range ops {
		groups[i], keys[i] = op.GroupID, op.ModelKey
	}
	rows, err := exec.QueryContext(ctx,
		`SELECT `+matrixCellColumns+`
		 FROM model_group_prices
		 WHERE is_pattern = FALSE AND (group_id, model_key) IN
		   (SELECT t.group_id, t.model_key FROM unnest($1::bigint[], $2::text[]) AS t(group_id, model_key))`,
		pq.Array(groups), pq.Array(keys))
	if err != nil {
		return nil, fmt.Errorf("query model_group_prices for write: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64][]service.StoredMatrixCell)
	for rows.Next() {
		c, err := scanMatrixCell(rows)
		if err != nil {
			return nil, fmt.Errorf("scan model_group_prices: %w", err)
		}
		out[c.GroupID] = append(out[c.GroupID], c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model_group_prices: %w", err)
	}
	return out, nil
}

func writeMatrixCell(ctx context.Context, exec service.MatrixExecutor, p service.PlannedCellWrite) error {
	switch p.Action {
	case service.CellWriteDelete:
		if _, err := exec.ExecContext(ctx, `DELETE FROM model_group_prices WHERE id = $1`, p.Before.ID); err != nil {
			return fmt.Errorf("delete cell %d: %w", p.Before.ID, err)
		}
		return nil
	case service.CellWriteUpdate:
		custom, err := matrixCustomPriceParam(p.After.CustomPrice)
		if err != nil {
			return err
		}
		// 生效时间窗保持不动：带时间窗的单元格在规划阶段已被拒绝。
		if _, err := exec.ExecContext(ctx,
			`UPDATE model_group_prices
			 SET open = $2, price_mode = $3, extra_multiplier = $4, custom_price = $5::jsonb, source = $6,
			     revision = revision + 1, updated_at = NOW()
			 WHERE id = $1`,
			p.Before.ID, p.After.Open, string(p.After.PriceMode), p.After.ExtraMultiplier, custom, string(p.After.Source)); err != nil {
			return fmt.Errorf("update cell %d: %w", p.Before.ID, err)
		}
		return nil
	default:
		custom, err := matrixCustomPriceParam(p.After.CustomPrice)
		if err != nil {
			return err
		}
		if _, err := exec.ExecContext(ctx,
			`INSERT INTO model_group_prices
			   (group_id, model_key, is_pattern, pattern_order, open, price_mode, extra_multiplier, custom_price, source)
			 VALUES ($1, $2, FALSE, 0, $3, $4, $5, $6::jsonb, $7)`,
			p.Op.GroupID, p.Op.ModelKey, p.After.Open, string(p.After.PriceMode), p.After.ExtraMultiplier, custom,
			string(p.After.Source)); err != nil {
			return fmt.Errorf("insert cell %q: %w", p.Op.ModelKey, err)
		}
		return nil
	}
}

// insertMatrixCellHistory 追加一行 model_group_price_history（与单元格写入同一事务）。
func insertMatrixCellHistory(ctx context.Context, exec service.MatrixExecutor, p service.PlannedCellWrite, req service.CellWriteRequest) error {
	var beforeCell *service.MatrixCell
	if p.Before != nil {
		c := p.Before.MatrixCell
		beforeCell = &c
	}
	before, err := matrixCellStateParam(beforeCell)
	if err != nil {
		return err
	}
	after, err := matrixCellStateParam(p.After)
	if err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx,
		`INSERT INTO model_group_price_history
		   (group_id, model_key, is_pattern, action, before_state, after_state, operator_id, change_set_id, approval_id)
		 VALUES ($1, $2, FALSE, $3, $4::jsonb, $5::jsonb, $6, $7, $8)`,
		p.Op.GroupID, p.Op.ModelKey, string(p.Action), before, after,
		nullableID(req.OperatorID), nullableID(req.ChangeSetID), nullableID(req.ApprovalID)); err != nil {
		return fmt.Errorf("insert cell history: %w", err)
	}
	return nil
}

// matrixCellStateParam 历史行的 before_state / after_state（jsonb）；nil 写 NULL。
func matrixCellStateParam(c *service.MatrixCell) (any, error) {
	if c == nil {
		return nil, nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal cell state: %w", err)
	}
	return string(b), nil
}

// nullableID 0 写成 NULL。
func nullableID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}
