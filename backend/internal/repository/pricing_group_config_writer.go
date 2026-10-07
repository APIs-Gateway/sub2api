package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// pricingGroupConfigWriter 手写 database/sql 实现分组配置写入（service.GroupConfigWriter）。
// 没有状态：事务由调用方传入（service.MatrixTx）。
type pricingGroupConfigWriter struct{}

// NewPricingGroupConfigWriter 创建分组配置写入器。
func NewPricingGroupConfigWriter() service.GroupConfigWriter { return pricingGroupConfigWriter{} }

func (pricingGroupConfigWriter) PlanTx(ctx context.Context, exec service.MatrixExecutor, req service.GroupConfigWriteRequest) (*service.GroupConfigWriteResult, error) {
	norm, err := service.NormalizeGroupConfigWrite(req)
	if err != nil {
		return nil, err
	}
	cur, found, err := loadGroupConfig(ctx, exec, norm.GroupID, false)
	if err != nil {
		return nil, err
	}
	_, res, err := planGroupConfigWrite(cur, found, norm)
	return res, err
}

func (pricingGroupConfigWriter) ApplyTx(ctx context.Context, tx service.MatrixTx, req service.GroupConfigWriteRequest) (*service.GroupConfigWriteResult, error) {
	norm, err := service.NormalizeGroupConfigWrite(req)
	if err != nil {
		return nil, err
	}
	cur, found, err := loadGroupConfig(ctx, tx, norm.GroupID, true)
	if err != nil {
		return nil, err
	}
	target, res, err := planGroupConfigWrite(cur, found, norm)
	if err != nil {
		return nil, err
	}
	if !res.Changed {
		return res, nil
	}

	mapping, err := json.Marshal(target.ModelMapping)
	if err != nil {
		return nil, fmt.Errorf("marshal model_mapping: %w", err)
	}
	features, err := json.Marshal(target.Features)
	if err != nil {
		return nil, fmt.Errorf("marshal features: %w", err)
	}
	var bms any
	if target.BillingModelSource != nil {
		bms = *target.BillingModelSource
	}
	rows, err := tx.QueryContext(ctx,
		`UPDATE group_model_config
		 SET access_mode = $2, billing_model_source = $3, model_mapping = $4::jsonb, features = $5::jsonb,
		     cost_mode = $6, revision = revision + 1, updated_at = NOW()
		 WHERE group_id = $1
		 RETURNING revision, updated_at`,
		norm.GroupID, string(target.AccessMode), bms, string(mapping), string(features), string(target.CostMode))
	if err != nil {
		return nil, fmt.Errorf("update group_model_config: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("update group_model_config: %w", err)
		}
		return nil, fmt.Errorf("update group_model_config: no row returned for group %d", norm.GroupID)
	}
	after := cur
	after.MatrixGroupConfig = target
	if err := rows.Scan(&after.Revision, &after.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan updated group_model_config: %w", err)
	}
	res.After = after
	return res, nil
}

// planGroupConfigWrite 核对分组是 v2、基线未变，套用补丁得到目标配置与结果（还没写入）。
// PlanTx 与 ApplyTx 共用它，所以预览与提交的校验完全一致。
func planGroupConfigWrite(cur service.StoredGroupConfig, found bool, norm service.GroupConfigWriteRequest) (service.MatrixGroupConfig, *service.GroupConfigWriteResult, error) {
	md := map[string]string{"group_id": strconv.FormatInt(norm.GroupID, 10)}
	if !found || cur.PricingStage != service.PricingStageV2 {
		return service.MatrixGroupConfig{}, nil, infraerrors.Conflict(service.ReasonGroupConfigNotV2,
			"group configuration is only editable for groups on the v2 pricing stage").WithMetadata(md)
	}
	if cur.Revision != norm.BaselineRevision {
		return service.MatrixGroupConfig{}, nil, infraerrors.Conflict(service.ReasonPriceBaselineChanged,
			"the group configuration changed since it was read").WithMetadata(md)
	}
	target := service.ApplyGroupConfigPatch(cur.MatrixGroupConfig, norm)
	changed, exposure := service.GroupConfigChange(cur.MatrixGroupConfig, target)
	res := &service.GroupConfigWriteResult{Before: cur, After: cur, Changed: changed, ExposureRelevant: exposure}
	if changed {
		// 预览看到的是改后的内容（revision 与 updated_at 要写入之后才有）。
		res.After.MatrixGroupConfig = target
	}
	return target, res, nil
}

// loadGroupConfig 读取分组的配置行；lock 为真时 FOR UPDATE OF c（只锁配置行，与单元格写入器、派生钩子、
// 阶段切换取同一把锁）。分组已软删除或没有配置行时 found 为 false。
func loadGroupConfig(ctx context.Context, exec service.MatrixExecutor, groupID int64, lock bool) (service.StoredGroupConfig, bool, error) {
	var c service.StoredGroupConfig
	lockClause := ""
	if lock {
		lockClause = "\n\t\t FOR UPDATE OF c"
	}
	rows, err := exec.QueryContext(ctx,
		`SELECT c.group_id, c.access_mode, c.billing_model_source, c.model_mapping, c.features, c.cost_mode,
		        c.pricing_stage, c.stage_changed_at, c.stage_changed_by, c.revision, c.updated_at
		 FROM group_model_config c
		 JOIN groups g ON g.id = c.group_id AND g.deleted_at IS NULL
		 WHERE c.group_id = $1`+lockClause, groupID)
	if err != nil {
		return c, false, fmt.Errorf("load group_model_config: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return c, false, rows.Err()
	}
	var (
		access, cost, stage string
		bms                 sql.NullString
		mapping, features   []byte
		changedAt           sql.NullTime
		changedBy           sql.NullInt64
	)
	if err := rows.Scan(&c.GroupID, &access, &bms, &mapping, &features, &cost,
		&stage, &changedAt, &changedBy, &c.Revision, &c.UpdatedAt); err != nil {
		return c, false, fmt.Errorf("scan group_model_config: %w", err)
	}
	c.AccessMode = service.MatrixAccessMode(access)
	c.CostMode = service.MatrixCostMode(cost)
	c.PricingStage = service.PricingStage(stage)
	if bms.Valid {
		v := bms.String
		c.BillingModelSource = &v
	}
	if err := json.Unmarshal(mapping, &c.ModelMapping); err != nil {
		return c, false, fmt.Errorf("decode model_mapping of group %d: %w", groupID, err)
	}
	if c.ModelMapping == nil {
		c.ModelMapping = []service.MatrixMappingEntry{}
	}
	if err := json.Unmarshal(features, &c.Features); err != nil {
		return c, false, fmt.Errorf("decode features of group %d: %w", groupID, err)
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
	return c, true, nil
}

// pricingExposureReader 保存时校验读状态用（service.ExposureReader）。
type pricingExposureReader struct{}

// NewPricingExposureReader 创建校验用的读取器。
func NewPricingExposureReader() service.ExposureReader { return pricingExposureReader{} }

func (pricingExposureReader) AccessModesTx(ctx context.Context, exec service.MatrixExecutor, groupIDs []int64) (map[int64]service.MatrixAccessMode, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT group_id, access_mode FROM group_model_config WHERE group_id = ANY($1)`, pq.Array(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("query access modes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]service.MatrixAccessMode, len(groupIDs))
	for rows.Next() {
		var (
			id   int64
			mode string
		)
		if err := rows.Scan(&id, &mode); err != nil {
			return nil, fmt.Errorf("scan access mode: %w", err)
		}
		out[id] = service.MatrixAccessMode(mode)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate access modes: %w", err)
	}
	return out, nil
}

func (pricingExposureReader) OpenCellsTx(ctx context.Context, exec service.MatrixExecutor, groupIDs []int64) ([]service.ExposureCell, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT `+matrixCellColumns+`
		 FROM model_group_prices
		 WHERE group_id = ANY($1) AND open = TRUE
		   AND effective_from IS NULL AND effective_to IS NULL
		 ORDER BY group_id, model_key`, pq.Array(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("query open cells: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []service.ExposureCell
	for rows.Next() {
		c, err := scanMatrixCell(rows)
		if err != nil {
			return nil, fmt.Errorf("scan open cell: %w", err)
		}
		out = append(out, service.ExposureCell{GroupID: c.GroupID, Cell: c.MatrixCell})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open cells: %w", err)
	}
	return out, nil
}
