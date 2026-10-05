package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// pricingMatrixRepository 手写 database/sql 实现 W6 矩阵表（group_model_config、model_group_prices、
// cost_accounting_rules / _rule_prices）的访问。不走 ent，与 api_key_group_routes 同风格。
// 三张表都不建外键（groups 是软删除），所以读取分组元数据时单独带上软删除标记。
type pricingMatrixRepository struct {
	db *sql.DB
}

// NewPricingMatrixRepository 创建矩阵表仓储。
func NewPricingMatrixRepository(db *sql.DB) service.PricingMatrixRepository {
	return &pricingMatrixRepository{db: db}
}

// pricingMatrixLockTimeout 钩子事务等待行锁的上限：与阶段切换互斥时不无限等待。
const pricingMatrixLockTimeout = "5s"

func (r *pricingMatrixRepository) GetGroupMeta(ctx context.Context, groupIDs []int64) (map[int64]service.DeriveGroup, error) {
	out := make(map[int64]service.DeriveGroup, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, platform, deleted_at IS NOT NULL FROM groups WHERE id = ANY($1)`, pq.Array(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("query group meta: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var g service.DeriveGroup
		if err := rows.Scan(&g.ID, &g.Platform, &g.Deleted); err != nil {
			return nil, fmt.Errorf("scan group meta: %w", err)
		}
		out[g.ID] = g
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group meta: %w", err)
	}
	return out, nil
}

func (r *pricingMatrixRepository) ListDerivedRuleGroupIDs(ctx context.Context, channelID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT scope_group_id FROM cost_accounting_rules
		 WHERE source = 'legacy_derived' AND source_channel_id = $1 ORDER BY scope_group_id`, channelID)
	if err != nil {
		return nil, fmt.Errorf("query derived rule groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan derived rule group: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate derived rule groups: %w", err)
	}
	return out, nil
}

func (r *pricingMatrixRepository) LoadGroupSnapshots(ctx context.Context, groupIDs []int64) (map[int64]service.GroupStateSnapshot, error) {
	return loadMatrixSnapshots(ctx, r.db, groupIDs)
}

func (r *pricingMatrixRepository) ApplyPlans(
	ctx context.Context,
	groupIDs []int64,
	plan func(snaps map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error),
) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+pricingMatrixLockTimeout+"'"); err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}
	// 与阶段切换互斥：两边都先按 group_id 升序 SELECT ... FOR UPDATE 取 group_model_config 行，再判断阶段。
	if err = lockGroupConfigRows(ctx, tx, groupIDs); err != nil {
		return err
	}
	snaps, err := loadMatrixSnapshots(ctx, tx, groupIDs)
	if err != nil {
		return err
	}
	plans, err := plan(snaps)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if p.Skipped || p.Empty() {
			continue
		}
		if err = applyMatrixGroupPlan(ctx, tx, p); err != nil {
			return fmt.Errorf("apply plan of group %d: %w", p.GroupID, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func lockGroupConfigRows(ctx context.Context, exec dbExec, groupIDs []int64) error {
	rows, err := exec.QueryContext(ctx,
		`SELECT group_id FROM group_model_config WHERE group_id = ANY($1) ORDER BY group_id FOR UPDATE`, pq.Array(groupIDs))
	if err != nil {
		return fmt.Errorf("lock group_model_config rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		// 只为取得行锁，不需要列值。
	}
	return rows.Err()
}

// ---- 读取 ----

func loadMatrixSnapshots(ctx context.Context, exec dbExec, groupIDs []int64) (map[int64]service.GroupStateSnapshot, error) {
	snaps := make(map[int64]service.GroupStateSnapshot, len(groupIDs))
	if len(groupIDs) == 0 {
		return snaps, nil
	}
	configs, err := loadMatrixConfigs(ctx, exec, groupIDs)
	if err != nil {
		return nil, err
	}
	cells, err := loadMatrixCells(ctx, exec, groupIDs)
	if err != nil {
		return nil, err
	}
	rules, err := loadMatrixCostRules(ctx, exec, groupIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range groupIDs {
		snap := service.GroupStateSnapshot{Cells: cells[id], Rules: rules[id]}
		if cfg, ok := configs[id]; ok {
			c := cfg
			snap.Config = &c
		}
		snaps[id] = snap
	}
	return snaps, nil
}

func loadMatrixConfigs(ctx context.Context, exec dbExec, groupIDs []int64) (map[int64]service.StoredGroupConfig, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT group_id, access_mode, billing_model_source, model_mapping, features, cost_mode,
		        pricing_stage, stage_changed_at, stage_changed_by, revision, updated_at
		 FROM group_model_config WHERE group_id = ANY($1)`, pq.Array(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("query group_model_config: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64]service.StoredGroupConfig)
	for rows.Next() {
		var (
			c                   service.StoredGroupConfig
			access, cost, stage string
			bms                 sql.NullString
			mapping, features   []byte
			changedAt           sql.NullTime
			changedBy           sql.NullInt64
		)
		if err := rows.Scan(&c.GroupID, &access, &bms, &mapping, &features, &cost,
			&stage, &changedAt, &changedBy, &c.Revision, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan group_model_config: %w", err)
		}
		c.AccessMode = service.MatrixAccessMode(access)
		c.CostMode = service.MatrixCostMode(cost)
		c.PricingStage = service.PricingStage(stage)
		if bms.Valid {
			v := bms.String
			c.BillingModelSource = &v
		}
		if err := json.Unmarshal(mapping, &c.ModelMapping); err != nil {
			return nil, fmt.Errorf("decode model_mapping of group %d: %w", c.GroupID, err)
		}
		if c.ModelMapping == nil {
			c.ModelMapping = []service.MatrixMappingEntry{}
		}
		if err := json.Unmarshal(features, &c.Features); err != nil {
			return nil, fmt.Errorf("decode features of group %d: %w", c.GroupID, err)
		}
		if c.Features == nil {
			c.Features = map[string]any{}
		}
		if changedAt.Valid {
			t := changedAt.Time
			c.StageChangedAt = &t
		}
		if changedBy.Valid {
			v := changedBy.Int64
			c.StageChangedBy = &v
		}
		out[c.GroupID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group_model_config: %w", err)
	}
	return out, nil
}

// matrixCellColumns model_group_prices 的读取列，顺序与 scanMatrixCell 一致。
const matrixCellColumns = `id, group_id, model_key, is_pattern, pattern_order, open, price_mode, extra_multiplier,
	        custom_price, effective_from, effective_to, source, revision, updated_at`

func loadMatrixCells(ctx context.Context, exec dbExec, groupIDs []int64) (map[int64][]service.StoredMatrixCell, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT `+matrixCellColumns+`
		 FROM model_group_prices WHERE group_id = ANY($1)
		 ORDER BY group_id, is_pattern, pattern_order, model_key`, pq.Array(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("query model_group_prices: %w", err)
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

// scanMatrixCell 按 matrixCellColumns 的顺序读一行单元格。
func scanMatrixCell(rows *sql.Rows) (service.StoredMatrixCell, error) {
	var (
		c            service.StoredMatrixCell
		mode, source string
		extra        sql.NullFloat64
		custom       []byte
		from, to     sql.NullTime
	)
	if err := rows.Scan(&c.ID, &c.GroupID, &c.ModelKey, &c.IsPattern, &c.PatternOrder, &c.Open, &mode, &extra,
		&custom, &from, &to, &source, &c.Revision, &c.UpdatedAt); err != nil {
		return c, err
	}
	c.PriceMode = service.MatrixPriceMode(mode)
	c.Source = service.MatrixSource(source)
	if extra.Valid {
		v := extra.Float64
		c.ExtraMultiplier = &v
	}
	if custom != nil {
		var price service.MatrixCustomPrice
		if err := json.Unmarshal(custom, &price); err != nil {
			return c, fmt.Errorf("decode custom_price of cell %d: %w", c.ID, err)
		}
		c.CustomPrice = &price
	}
	if from.Valid {
		t := from.Time
		c.EffectiveFrom = &t
	}
	if to.Valid {
		t := to.Time
		c.EffectiveTo = &t
	}
	return c, nil
}

func loadMatrixCostRules(ctx context.Context, exec dbExec, groupIDs []int64) (map[int64][]service.StoredMatrixCostRule, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT id, scope_group_id, source, source_channel_id, source_ordinal, name, group_ids, account_ids, sort_order, enabled
		 FROM cost_accounting_rules WHERE scope_group_id = ANY($1)
		 ORDER BY scope_group_id, sort_order, source_ordinal NULLS LAST, id`, pq.Array(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("query cost_accounting_rules: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		rules   []service.StoredMatrixCostRule
		ruleIDs []int64
	)
	for rows.Next() {
		var (
			r        service.StoredMatrixCostRule
			source   string
			channel  sql.NullInt64
			ordinal  sql.NullInt64
			gids     pq.Int64Array
			accounts pq.Int64Array
		)
		if err := rows.Scan(&r.ID, &r.ScopeGroupID, &source, &channel, &ordinal, &r.Name, &gids, &accounts, &r.SortOrder, &r.Enabled); err != nil {
			return nil, fmt.Errorf("scan cost_accounting_rules: %w", err)
		}
		r.Source = service.MatrixSource(source)
		r.SourceChannelID = channel.Int64
		r.SourceOrdinal = int(ordinal.Int64)
		r.GroupIDs = append([]int64{}, gids...)
		r.AccountIDs = append([]int64{}, accounts...)
		r.Prices = []service.MatrixCostRulePrice{}
		rules = append(rules, r)
		ruleIDs = append(ruleIDs, r.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cost_accounting_rules: %w", err)
	}
	_ = rows.Close()

	out := make(map[int64][]service.StoredMatrixCostRule)
	if len(rules) == 0 {
		return out, nil
	}
	prices, err := loadMatrixCostRulePrices(ctx, exec, ruleIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if p, ok := prices[r.ID]; ok {
			r.Prices = p
		}
		out[r.ScopeGroupID] = append(out[r.ScopeGroupID], r)
	}
	return out, nil
}

func loadMatrixCostRulePrices(ctx context.Context, exec dbExec, ruleIDs []int64) (map[int64][]service.MatrixCostRulePrice, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT rule_id, platform, models, price FROM cost_accounting_rule_prices
		 WHERE rule_id = ANY($1) ORDER BY rule_id, id`, pq.Array(ruleIDs))
	if err != nil {
		return nil, fmt.Errorf("query cost_accounting_rule_prices: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64][]service.MatrixCostRulePrice)
	for rows.Next() {
		var (
			ruleID        int64
			p             service.MatrixCostRulePrice
			models, price []byte
		)
		if err := rows.Scan(&ruleID, &p.Platform, &models, &price); err != nil {
			return nil, fmt.Errorf("scan cost_accounting_rule_prices: %w", err)
		}
		if err := json.Unmarshal(models, &p.Models); err != nil {
			return nil, fmt.Errorf("decode models of rule %d: %w", ruleID, err)
		}
		if p.Models == nil {
			p.Models = []string{}
		}
		if err := json.Unmarshal(price, &p.Price); err != nil {
			return nil, fmt.Errorf("decode price of rule %d: %w", ruleID, err)
		}
		out[ruleID] = append(out[ruleID], p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cost_accounting_rule_prices: %w", err)
	}
	return out, nil
}

// ---- 写入 ----

// applyMatrixGroupPlan 执行一个分组的落库计划。调用方已经持有 group_model_config 行锁并确认阶段不是 v2。
func applyMatrixGroupPlan(ctx context.Context, exec dbExec, p service.GroupApplyPlan) error {
	if p.ConfigWrite != nil {
		written, err := upsertMatrixGroupConfig(ctx, exec, p.GroupID, *p.ConfigWrite)
		if err != nil {
			return err
		}
		if !written {
			// 行已经是 v2（并发切换）：整体跳过，单元格与成本核算行也不碰。
			return nil
		}
	}
	if len(p.CellDeletes) > 0 {
		if _, err := exec.ExecContext(ctx,
			`DELETE FROM model_group_prices WHERE id = ANY($1) AND source = 'legacy_derived'`, pq.Array(p.CellDeletes)); err != nil {
			return fmt.Errorf("delete derived cells: %w", err)
		}
	}
	for _, c := range p.CellUpdates {
		custom, err := matrixCustomPriceParam(c.CustomPrice)
		if err != nil {
			return err
		}
		if _, err := exec.ExecContext(ctx,
			`UPDATE model_group_prices
			 SET pattern_order = $2, open = $3, price_mode = $4, extra_multiplier = $5, custom_price = $6::jsonb,
			     revision = revision + 1, updated_at = NOW()
			 WHERE id = $1 AND source = 'legacy_derived'`,
			c.ID, c.PatternOrder, c.Open, string(c.PriceMode), c.ExtraMultiplier, custom); err != nil {
			return fmt.Errorf("update derived cell %d: %w", c.ID, err)
		}
	}
	for _, c := range p.CellInserts {
		custom, err := matrixCustomPriceParam(c.CustomPrice)
		if err != nil {
			return err
		}
		if _, err := exec.ExecContext(ctx,
			`INSERT INTO model_group_prices
			   (group_id, model_key, is_pattern, pattern_order, open, price_mode, extra_multiplier, custom_price, source)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
			 ON CONFLICT (group_id, model_key, is_pattern) DO NOTHING`,
			p.GroupID, c.ModelKey, c.IsPattern, c.PatternOrder, c.Open, string(c.PriceMode), c.ExtraMultiplier, custom,
			string(service.MatrixSourceLegacyDerived)); err != nil {
			return fmt.Errorf("insert derived cell %q: %w", c.ModelKey, err)
		}
	}
	if len(p.RuleDeletes) > 0 {
		// 价格行随规则行级联删除。
		if _, err := exec.ExecContext(ctx,
			`DELETE FROM cost_accounting_rules WHERE id = ANY($1) AND source = 'legacy_derived'`, pq.Array(p.RuleDeletes)); err != nil {
			return fmt.Errorf("delete derived cost rules: %w", err)
		}
	}
	for _, r := range p.RuleInserts {
		if err := insertMatrixCostRule(ctx, exec, p.GroupID, r); err != nil {
			return err
		}
	}
	return nil
}

// upsertMatrixGroupConfig 写 group_model_config：保留阶段字段，内容变了才把 revision 加一；
// 已经是 v2 的行不覆盖（返回 false）。
func upsertMatrixGroupConfig(ctx context.Context, exec dbExec, groupID int64, cfg service.MatrixGroupConfig) (bool, error) {
	mapping, err := json.Marshal(cfg.ModelMapping)
	if err != nil {
		return false, fmt.Errorf("marshal model_mapping: %w", err)
	}
	features, err := json.Marshal(cfg.Features)
	if err != nil {
		return false, fmt.Errorf("marshal features: %w", err)
	}
	var bms any
	if cfg.BillingModelSource != nil {
		bms = *cfg.BillingModelSource
	}
	res, err := exec.ExecContext(ctx,
		`INSERT INTO group_model_config (group_id, access_mode, billing_model_source, model_mapping, features, cost_mode)
		 VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6)
		 ON CONFLICT (group_id) DO UPDATE SET
		   access_mode = EXCLUDED.access_mode,
		   billing_model_source = EXCLUDED.billing_model_source,
		   model_mapping = EXCLUDED.model_mapping,
		   features = EXCLUDED.features,
		   cost_mode = EXCLUDED.cost_mode,
		   revision = group_model_config.revision + 1,
		   updated_at = NOW()
		 WHERE group_model_config.pricing_stage <> 'v2'`,
		groupID, string(cfg.AccessMode), bms, string(mapping), string(features), string(cfg.CostMode))
	if err != nil {
		return false, fmt.Errorf("upsert group_model_config: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n > 0, nil
}

func insertMatrixCostRule(ctx context.Context, exec dbExec, groupID int64, r service.MatrixCostRule) error {
	var channelID, ordinal any
	if r.SourceChannelID != 0 {
		channelID = r.SourceChannelID
	}
	if r.SourceOrdinal != 0 {
		ordinal = r.SourceOrdinal
	}
	var ruleID int64
	err := exec.QueryRowContext(ctx,
		`INSERT INTO cost_accounting_rules
		   (name, scope_group_id, source, source_channel_id, source_ordinal, group_ids, account_ids, sort_order, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		r.Name, groupID, string(service.MatrixSourceLegacyDerived), channelID, ordinal,
		pq.Array(r.GroupIDs), pq.Array(r.AccountIDs), r.SortOrder, r.Enabled).Scan(&ruleID)
	if err != nil {
		return fmt.Errorf("insert derived cost rule %q: %w", r.Name, err)
	}
	for _, p := range r.Prices {
		models, err := json.Marshal(p.Models)
		if err != nil {
			return fmt.Errorf("marshal rule models: %w", err)
		}
		price, err := json.Marshal(p.Price)
		if err != nil {
			return fmt.Errorf("marshal rule price: %w", err)
		}
		if _, err := exec.ExecContext(ctx,
			`INSERT INTO cost_accounting_rule_prices (rule_id, platform, models, price) VALUES ($1, $2, $3::jsonb, $4::jsonb)`,
			ruleID, p.Platform, string(models), string(price)); err != nil {
			return fmt.Errorf("insert cost rule price: %w", err)
		}
	}
	return nil
}

// matrixCustomPriceParam 把 custom_price 转成 jsonb 参数；nil 写 NULL。
func matrixCustomPriceParam(price *service.MatrixCustomPrice) (any, error) {
	if price == nil {
		return nil, nil
	}
	b, err := json.Marshal(price)
	if err != nil {
		return nil, fmt.Errorf("marshal custom_price: %w", err)
	}
	return string(b), nil
}
