package repository

import (
	"context"
	"encoding/json"
	"fmt"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// pricingCostRuleWriter 是成本核算规则的 tx-aware 写入器（service.CostRuleWriter）。
// 与单元格写入器取同一把锁（group_model_config 行 FOR UPDATE）、同一个基线（分组配置 revision），
// 写完把 revision 加一，并在同一个事务里追加 cost_accounting_rule_history（操作人、动作、改前改后的完整内容）。
// manual 规则由这里写；legacy_derived 行由渠道派生，只读。
type pricingCostRuleWriter struct{}

// NewPricingCostRuleWriter 创建成本核算规则写入器。
func NewPricingCostRuleWriter() service.CostRuleWriter { return pricingCostRuleWriter{} }

// lockCostRuleGroup 锁分组配置行并校验：分组存在、在 v2 阶段、基线 revision 一致。
func lockCostRuleGroup(ctx context.Context, tx service.MatrixTx, groupID, baseline int64) (int64, error) {
	states, err := loadCellGroupStates(ctx, tx, []int64{groupID}, true)
	if err != nil {
		return 0, err
	}
	st, ok := states[groupID]
	if !ok {
		return 0, infraerrors.NotFound(service.ReasonCostRuleNotFound, "the group has no pricing configuration")
	}
	if st.Stage != service.PricingStageV2 {
		return 0, infraerrors.Conflict(service.ReasonGroupConfigNotV2, "cost rules are only editable for groups on the v2 pricing stage")
	}
	if st.Revision != baseline {
		return 0, infraerrors.Conflict(service.ReasonPriceBaselineChanged, "the group configuration changed since it was loaded").
			WithMetadata(map[string]string{"group_id": fmt.Sprint(groupID), "revision": fmt.Sprint(st.Revision)})
	}
	return st.Revision, nil
}

// bumpCostRuleRevision 把分组配置 revision 加一并返回新值。
func bumpCostRuleRevision(ctx context.Context, tx service.MatrixTx, groupID int64) (int64, error) {
	rows, err := tx.QueryContext(ctx,
		`UPDATE group_model_config SET revision = revision + 1, updated_at = NOW() WHERE group_id = $1 RETURNING revision`, groupID)
	if err != nil {
		return 0, fmt.Errorf("bump group_model_config revision: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var rev int64
	if rows.Next() {
		if err := rows.Scan(&rev); err != nil {
			return 0, fmt.Errorf("scan revision: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate revision: %w", err)
	}
	return rev, nil
}

// loadCostRuleState 读一条规则（规则行加价格行）的完整内容，序列化成 JSON，给历史行当改前、改后快照；规则不存在返回 nil。
func loadCostRuleState(ctx context.Context, tx service.MatrixTx, ruleID int64) ([]byte, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT jsonb_build_object(
		     'name', r.name, 'source', r.source, 'group_ids', r.group_ids, 'account_ids', r.account_ids,
		     'sort_order', r.sort_order, 'enabled', r.enabled,
		     'prices', COALESCE((SELECT jsonb_agg(jsonb_build_object('platform', p.platform, 'models', p.models, 'price', p.price) ORDER BY p.id)
		                         FROM cost_accounting_rule_prices p WHERE p.rule_id = r.id), '[]'::jsonb))::text
		 FROM cost_accounting_rules r WHERE r.id = $1`, ruleID)
	if err != nil {
		return nil, fmt.Errorf("load cost rule state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var raw string
	if err := rows.Scan(&raw); err != nil {
		return nil, fmt.Errorf("scan cost rule state: %w", err)
	}
	return []byte(raw), nil
}

// appendCostRuleHistory 追加一行历史；before、after 为 nil 时存 NULL。
func appendCostRuleHistory(ctx context.Context, tx service.MatrixTx, operatorID, groupID, ruleID int64, action string, revision int64, before, after []byte) error {
	var b, a any
	if before != nil {
		b = string(before)
	}
	if after != nil {
		a = string(after)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO cost_accounting_rule_history (rule_id, scope_group_id, action, operator_id, group_revision, before_state, after_state)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb)`,
		ruleID, groupID, action, operatorID, revision, b, a); err != nil {
		return fmt.Errorf("append cost rule history: %w", err)
	}
	return nil
}

func insertCostRulePrices(ctx context.Context, tx service.MatrixTx, ruleID int64, prices []service.MatrixCostRulePrice) error {
	for _, p := range prices {
		models, err := json.Marshal(p.Models)
		if err != nil {
			return fmt.Errorf("marshal rule models: %w", err)
		}
		price, err := json.Marshal(p.Price)
		if err != nil {
			return fmt.Errorf("marshal rule price: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO cost_accounting_rule_prices (rule_id, platform, models, price) VALUES ($1, $2, $3::jsonb, $4::jsonb)`,
			ruleID, p.Platform, string(models), string(price)); err != nil {
			return fmt.Errorf("insert cost rule price: %w", err)
		}
	}
	return nil
}

// editableCostRule 锁住一条规则并确认它属于该分组、不是渠道派生的。
func editableCostRule(ctx context.Context, tx service.MatrixTx, groupID, ruleID int64) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT source FROM cost_accounting_rules WHERE id = $1 AND scope_group_id = $2 FOR UPDATE`, ruleID, groupID)
	if err != nil {
		return fmt.Errorf("query cost rule: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate cost rule: %w", err)
		}
		return infraerrors.NotFound(service.ReasonCostRuleNotFound, "cost rule not found")
	}
	var source string
	if err := rows.Scan(&source); err != nil {
		return fmt.Errorf("scan cost rule: %w", err)
	}
	if service.MatrixSource(source) == service.MatrixSourceLegacyDerived {
		return infraerrors.Conflict(service.ReasonCostRuleReadonly, "this rule is derived from a channel and cannot be edited here")
	}
	return nil
}

func (pricingCostRuleWriter) CreateTx(ctx context.Context, tx service.MatrixTx, operatorID, groupID, baseline int64, spec service.CostRuleSpec) (*service.CostRuleWriteResult, error) {
	if _, err := lockCostRuleGroup(ctx, tx, groupID, baseline); err != nil {
		return nil, err
	}
	var ruleID int64
	rows, err := tx.QueryContext(ctx,
		`INSERT INTO cost_accounting_rules (name, scope_group_id, source, group_ids, account_ids, sort_order, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		spec.Name, groupID, string(service.MatrixSourceManual), pq.Array(spec.GroupIDs), pq.Array(spec.AccountIDs), spec.SortOrder, spec.Enabled)
	if err != nil {
		return nil, fmt.Errorf("insert cost rule: %w", err)
	}
	if rows.Next() {
		if err := rows.Scan(&ruleID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan cost rule id: %w", err)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, fmt.Errorf("iterate cost rule id: %w", err)
	}
	if err := insertCostRulePrices(ctx, tx, ruleID, spec.Prices); err != nil {
		return nil, err
	}
	rev, err := bumpCostRuleRevision(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	after, err := loadCostRuleState(ctx, tx, ruleID)
	if err != nil {
		return nil, err
	}
	if err := appendCostRuleHistory(ctx, tx, operatorID, groupID, ruleID, "create", rev, nil, after); err != nil {
		return nil, err
	}
	return &service.CostRuleWriteResult{GroupID: groupID, RuleID: ruleID, Revision: rev}, nil
}

func (pricingCostRuleWriter) UpdateTx(ctx context.Context, tx service.MatrixTx, operatorID, groupID, baseline, ruleID int64, spec service.CostRuleSpec) (*service.CostRuleWriteResult, error) {
	if _, err := lockCostRuleGroup(ctx, tx, groupID, baseline); err != nil {
		return nil, err
	}
	if err := editableCostRule(ctx, tx, groupID, ruleID); err != nil {
		return nil, err
	}
	before, err := loadCostRuleState(ctx, tx, ruleID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE cost_accounting_rules
		 SET name = $2, group_ids = $3, account_ids = $4, sort_order = $5, enabled = $6, updated_at = NOW()
		 WHERE id = $1`,
		ruleID, spec.Name, pq.Array(spec.GroupIDs), pq.Array(spec.AccountIDs), spec.SortOrder, spec.Enabled); err != nil {
		return nil, fmt.Errorf("update cost rule: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cost_accounting_rule_prices WHERE rule_id = $1`, ruleID); err != nil {
		return nil, fmt.Errorf("delete cost rule prices: %w", err)
	}
	if err := insertCostRulePrices(ctx, tx, ruleID, spec.Prices); err != nil {
		return nil, err
	}
	rev, err := bumpCostRuleRevision(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	after, err := loadCostRuleState(ctx, tx, ruleID)
	if err != nil {
		return nil, err
	}
	if err := appendCostRuleHistory(ctx, tx, operatorID, groupID, ruleID, "update", rev, before, after); err != nil {
		return nil, err
	}
	return &service.CostRuleWriteResult{GroupID: groupID, RuleID: ruleID, Revision: rev}, nil
}

func (pricingCostRuleWriter) DeleteTx(ctx context.Context, tx service.MatrixTx, operatorID, groupID, baseline, ruleID int64) (*service.CostRuleWriteResult, error) {
	if _, err := lockCostRuleGroup(ctx, tx, groupID, baseline); err != nil {
		return nil, err
	}
	if err := editableCostRule(ctx, tx, groupID, ruleID); err != nil {
		return nil, err
	}
	before, err := loadCostRuleState(ctx, tx, ruleID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cost_accounting_rules WHERE id = $1`, ruleID); err != nil {
		return nil, fmt.Errorf("delete cost rule: %w", err)
	}
	rev, err := bumpCostRuleRevision(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	if err := appendCostRuleHistory(ctx, tx, operatorID, groupID, ruleID, "delete", rev, before, nil); err != nil {
		return nil, err
	}
	return &service.CostRuleWriteResult{GroupID: groupID, RuleID: ruleID, Revision: rev}, nil
}
