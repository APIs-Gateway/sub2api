package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// W6 PR7b：阶段切换的存储原语（service.PricingStageOps）与回放证据的写入。手写 database/sql，与同目录其余 W6 仓储同风格。
// 改库的方法只在 PriceWriteStore.WithTx 给出的事务里调用；读方法事务与普通连接都可以用。

// pricingStageSwitchStore 审批与事务沿用 PriceWriteStore 的实现，阶段原语在此补上。
type pricingStageSwitchStore struct {
	service.PriceWriteStore
	db *sql.DB
}

// NewPricingStageSwitchStore 创建阶段切换存储。
func NewPricingStageSwitchStore(db *sql.DB) service.PricingStageSwitchStore {
	return &pricingStageSwitchStore{PriceWriteStore: NewPricingWriteStore(db), db: db}
}

// NewPricingStageFingerprinter 渠道配置摘要的读口（与回放同一份实现）。
func NewPricingStageFingerprinter(db *sql.DB) service.PricingStageFingerprinter {
	return &pricingReplayRepository{db: db}
}

// stageExec 把 MatrixExecutor 还原成仓储内部用的 dbExec（*sql.Tx 与 *sql.DB 都满足）。
func stageExec(exec service.MatrixExecutor) (dbExec, error) {
	d, ok := exec.(dbExec)
	if !ok {
		return nil, errors.New("pricing stage: the executor cannot run single-row queries")
	}
	return d, nil
}

func (s *pricingStageSwitchStore) LockConfig(ctx context.Context, tx service.MatrixExecutor, groupID int64) (*service.StoredGroupConfig, error) {
	exec, err := stageExec(tx)
	if err != nil {
		return nil, err
	}
	var id int64
	err = exec.QueryRowContext(ctx,
		`SELECT c.group_id FROM group_model_config c JOIN groups g ON g.id = c.group_id
		 WHERE c.group_id = $1 AND g.deleted_at IS NULL FOR UPDATE OF c`, groupID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, classifyMissingStageRow(ctx, exec, groupID)
	}
	if err != nil {
		return nil, fmt.Errorf("lock group_model_config: %w", err)
	}
	cfgs, err := loadMatrixConfigs(ctx, exec, []int64{groupID})
	if err != nil {
		return nil, err
	}
	cfg, ok := cfgs[groupID]
	if !ok {
		return nil, service.ErrPricingStageNotDerived
	}
	return &cfg, nil
}

func (s *pricingStageSwitchStore) LoadGateFacts(ctx context.Context, e service.MatrixExecutor, groupID int64, now time.Time) (*service.StageGateFacts, error) {
	exec, err := stageExec(e)
	if err != nil {
		return nil, err
	}
	cfgs, err := loadMatrixConfigs(ctx, exec, []int64{groupID})
	if err != nil {
		return nil, err
	}
	cfg, ok := cfgs[groupID]
	if !ok {
		return nil, classifyMissingStageRow(ctx, exec, groupID)
	}
	facts := &service.StageGateFacts{Config: service.StageGateConfig{
		GroupID: groupID, Stage: cfg.PricingStage, Revision: cfg.Revision,
		StageChangedAt: cfg.StageChangedAt, UpdatedAt: cfg.UpdatedAt,
	}}

	var ownerUpdated sql.NullTime
	if err := exec.QueryRowContext(ctx,
		`SELECT MAX(c.updated_at) FROM channels c JOIN channel_groups cg ON cg.channel_id = c.id WHERE cg.group_id = $1`,
		groupID).Scan(&ownerUpdated); err != nil {
		return nil, fmt.Errorf("query owner channel: %w", err)
	}
	if ownerUpdated.Valid {
		t := ownerUpdated.Time
		facts.OwnerChannelUpdatedAt = &t
	}

	// 影子样本：只数观察期起点与样本保留期里较晚的那个之后的。
	since := service.StageObservedSince(facts.Config, facts.OwnerChannelUpdatedAt)
	if floor := now.Add(-service.PricingGateShadowRetention); floor.After(since) {
		since = floor
	}
	trafficSince := now.Add(-service.PricingGateExpectedTrafficWindow)
	if since.After(trafficSince) {
		trafficSince = since
	}
	facts.Shadow.WindowFrom = since
	facts.Shadow.ExpectedModels = []string{}
	if err := exec.QueryRowContext(ctx,
		`SELECT COUNT(*) FILTER (WHERE class = 'translation'), COUNT(*) FILTER (WHERE class = 'expected')
		 FROM pricing_shadow_diffs WHERE group_id = $1 AND created_at >= $2`,
		groupID, since).Scan(&facts.Shadow.TranslationDiffs, &facts.Shadow.ExpectedDiffs); err != nil {
		return nil, fmt.Errorf("count pricing_shadow_diffs: %w", err)
	}
	rows, err := exec.QueryContext(ctx,
		`SELECT DISTINCT lower(model) FROM pricing_shadow_diffs
		 WHERE group_id = $1 AND class = 'expected' AND created_at >= $2 ORDER BY 1`, groupID, trafficSince)
	if err != nil {
		return nil, fmt.Errorf("query expected models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("scan expected model: %w", err)
		}
		facts.Shadow.ExpectedModels = append(facts.Shadow.ExpectedModels, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expected models: %w", err)
	}
	_ = rows.Close()

	ev, err := latestReplayEvidence(ctx, exec, groupID)
	if err != nil {
		return nil, err
	}
	facts.Replay = ev
	return facts, nil
}

func latestReplayEvidence(ctx context.Context, exec dbExec, groupID int64) (*service.ReplayEvidence, error) {
	var (
		ev    service.ReplayEvidence
		diffs []byte
	)
	err := exec.QueryRowContext(ctx,
		`SELECT id, group_id, recorded_at, window_from, window_to, matrix_source, passed, binding_stable,
		        rows_in_window, rows_replayed, rows_errored, translation_diffs, expected_diffs,
		        channel_config_hash, derive_revision, matrix_hash, pricing_data_sha256, tool_version, diffs
		 FROM pricing_replay_evidence WHERE group_id = $1 ORDER BY recorded_at DESC, id DESC LIMIT 1`, groupID).Scan(
		&ev.ID, &ev.GroupID, &ev.RecordedAt, &ev.WindowFrom, &ev.WindowTo, &ev.MatrixSource, &ev.Passed, &ev.BindingStable,
		&ev.RowsInWindow, &ev.RowsReplayed, &ev.RowsErrored, &ev.TranslationDiffs, &ev.ExpectedDiffs,
		&ev.ChannelConfigHash, &ev.DeriveRevision, &ev.MatrixHash, &ev.PricingDataSHA256, &ev.ToolVersion, &diffs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query pricing_replay_evidence: %w", err)
	}
	if err := json.Unmarshal(diffs, &ev.Diffs); err != nil {
		return nil, fmt.Errorf("decode replay diffs: %w", err)
	}
	if ev.Diffs == nil {
		ev.Diffs = []service.PricingReplayDiffCount{}
	}
	return &ev, nil
}

func (s *pricingStageSwitchStore) LoadSnapshot(ctx context.Context, e service.MatrixExecutor, groupID int64) (service.GroupStateSnapshot, error) {
	exec, err := stageExec(e)
	if err != nil {
		return service.GroupStateSnapshot{}, err
	}
	snaps, err := loadMatrixSnapshots(ctx, exec, []int64{groupID})
	if err != nil {
		return service.GroupStateSnapshot{}, err
	}
	return snaps[groupID], nil
}

func (s *pricingStageSwitchStore) ApplyPlan(ctx context.Context, tx service.MatrixExecutor, plan service.GroupApplyPlan) error {
	exec, err := stageExec(tx)
	if err != nil {
		return err
	}
	if plan.Skipped || plan.Empty() {
		return nil
	}
	return applyMatrixGroupPlan(ctx, exec, plan)
}

func (s *pricingStageSwitchStore) FreezeDerived(ctx context.Context, tx service.MatrixExecutor, groupID int64) (cells, rules int64, err error) {
	res, err := tx.ExecContext(ctx,
		`UPDATE model_group_prices SET source = 'legacy_frozen' WHERE group_id = $1 AND source = 'legacy_derived'`, groupID)
	if err != nil {
		return 0, 0, fmt.Errorf("freeze model_group_prices: %w", err)
	}
	if cells, err = res.RowsAffected(); err != nil {
		return 0, 0, fmt.Errorf("rows affected: %w", err)
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE cost_accounting_rules SET source = 'legacy_frozen' WHERE scope_group_id = $1 AND source = 'legacy_derived'`, groupID)
	if err != nil {
		return 0, 0, fmt.Errorf("freeze cost_accounting_rules: %w", err)
	}
	if rules, err = res.RowsAffected(); err != nil {
		return 0, 0, fmt.Errorf("rows affected: %w", err)
	}
	return cells, rules, nil
}

func (s *pricingStageSwitchStore) ArchiveNonDerived(ctx context.Context, tx service.MatrixExecutor, groupID, operatorID, approvalID int64) (service.StageArchive, error) {
	var out service.StageArchive
	exec, err := stageExec(tx)
	if err != nil {
		return out, err
	}
	cells, err := loadMatrixCells(ctx, exec, []int64{groupID})
	if err != nil {
		return out, err
	}
	for _, c := range cells[groupID] {
		if c.Source == service.MatrixSourceLegacyDerived {
			continue
		}
		before, err := matrixCellStateParam(&c.MatrixCell)
		if err != nil {
			return out, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO model_group_price_history
			   (group_id, model_key, is_pattern, action, before_state, after_state, operator_id, approval_id)
			 VALUES ($1, $2, $3, 'archive', $4::jsonb, NULL, $5, $6)`,
			groupID, c.ModelKey, c.IsPattern, before, nullableID(operatorID), nullableID(approvalID)); err != nil {
			return out, fmt.Errorf("archive cell history: %w", err)
		}
		out.Cells++
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM model_group_prices WHERE group_id = $1 AND source <> 'legacy_derived'`, groupID); err != nil {
		return out, fmt.Errorf("delete non-derived cells: %w", err)
	}

	rules, err := loadMatrixCostRules(ctx, exec, []int64{groupID})
	if err != nil {
		return out, err
	}
	archived := []service.StoredMatrixCostRule{}
	for _, r := range rules[groupID] {
		if r.Source != service.MatrixSourceLegacyDerived {
			archived = append(archived, r)
		}
	}
	raw, err := json.Marshal(archived)
	if err != nil {
		return out, fmt.Errorf("marshal archived rules: %w", err)
	}
	out.Rules, out.RulesJSON = int64(len(archived)), raw
	if len(archived) > 0 {
		// 价格行由外键 ON DELETE CASCADE 一并删除。
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM cost_accounting_rules WHERE scope_group_id = $1 AND source <> 'legacy_derived'`, groupID); err != nil {
			return out, fmt.Errorf("delete non-derived cost rules: %w", err)
		}
	}
	return out, nil
}

func (s *pricingStageSwitchStore) SetStage(ctx context.Context, tx service.MatrixExecutor, groupID int64, to service.PricingStage, operatorID int64, now time.Time) (int64, error) {
	exec, err := stageExec(tx)
	if err != nil {
		return 0, err
	}
	return setGroupStage(ctx, exec, groupID, to, operatorID, now)
}

func (s *pricingStageSwitchStore) InsertAudit(ctx context.Context, tx service.MatrixExecutor, rec service.StageAuditRecord) (int64, error) {
	exec, err := stageExec(tx)
	if err != nil {
		return 0, err
	}
	evidence := rec.Evidence
	if len(evidence) == 0 {
		evidence = []byte("{}")
	}
	var id int64
	err = exec.QueryRowContext(ctx,
		`INSERT INTO pricing_stage_audit
		   (group_id, from_stage, to_stage, kind, operator_id, interactive, approval_id, price_delta,
		    config_revision_before, config_revision_after, evidence)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb) RETURNING id`,
		rec.GroupID, string(rec.From), string(rec.To), rec.Kind, rec.Actor.ID, rec.Actor.Interactive,
		nullableID(rec.ApprovalID), string(rec.PriceDelta), rec.RevisionBefore, rec.RevisionAfter, string(evidence)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert pricing_stage_audit: %w", err)
	}
	return id, nil
}

func (s *pricingStageSwitchStore) RecentUsageModels(ctx context.Context, exec service.MatrixExecutor, groupID int64, since time.Time) (map[string]int64, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT lower(btrim(COALESCE(NULLIF(requested_model, ''), model))) AS m, COUNT(*)
		 FROM usage_logs WHERE group_id = $1 AND created_at >= $2 GROUP BY 1`, groupID, since)
	if err != nil {
		return nil, fmt.Errorf("query recent usage models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var m string
		var n int64
		if err := rows.Scan(&m, &n); err != nil {
			return nil, fmt.Errorf("scan recent usage model: %w", err)
		}
		if m != "" {
			out[m] = n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent usage models: %w", err)
	}
	return out, nil
}

func (s *pricingStageSwitchStore) ListAudit(ctx context.Context, exec service.MatrixExecutor, groupID int64, limit int) ([]service.StageAuditEntry, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT id, created_at, group_id, from_stage, to_stage, kind, operator_id, interactive, COALESCE(approval_id, 0),
		        price_delta, config_revision_before, config_revision_after, evidence
		 FROM pricing_stage_audit WHERE group_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, groupID, limit)
	if err != nil {
		return nil, fmt.Errorf("query pricing_stage_audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []service.StageAuditEntry{}
	for rows.Next() {
		var (
			e        service.StageAuditEntry
			from, to string
			delta    string
			evidence []byte
		)
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.GroupID, &from, &to, &e.Kind, &e.OperatorID, &e.Interactive,
			&e.ApprovalID, &delta, &e.RevisionBefore, &e.RevisionAfter, &evidence); err != nil {
			return nil, fmt.Errorf("scan pricing_stage_audit: %w", err)
		}
		e.From, e.To, e.PriceDelta, e.Evidence = service.PricingStage(from), service.PricingStage(to), service.PriceDelta(delta), evidence
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pricing_stage_audit: %w", err)
	}
	return out, nil
}

// ---- 回放证据 ----

type pricingReplayEvidenceStore struct {
	db *sql.DB
}

// NewPricingReplayEvidenceStore 创建回放证据存储。db 必须是可写连接（回放本身用的是只读会话，不能复用）。
func NewPricingReplayEvidenceStore(db *sql.DB) service.PricingReplayEvidenceStore {
	return &pricingReplayEvidenceStore{db: db}
}

func (s *pricingReplayEvidenceStore) RecordReplayEvidence(ctx context.Context, rows []service.ReplayEvidence) (err error) {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, r := range rows {
		diffs, merr := json.Marshal(r.Diffs)
		if merr != nil {
			return fmt.Errorf("marshal replay diffs: %w", merr)
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO pricing_replay_evidence
			   (group_id, recorded_at, window_from, window_to, matrix_source, passed, binding_stable,
			    rows_in_window, rows_replayed, rows_errored, translation_diffs, expected_diffs,
			    channel_config_hash, derive_revision, matrix_hash, pricing_data_sha256, tool_version, diffs)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18::jsonb)`,
			r.GroupID, r.RecordedAt, r.WindowFrom, r.WindowTo, r.MatrixSource, r.Passed, r.BindingStable,
			r.RowsInWindow, r.RowsReplayed, r.RowsErrored, r.TranslationDiffs, r.ExpectedDiffs,
			r.ChannelConfigHash, r.DeriveRevision, r.MatrixHash, r.PricingDataSHA256, r.ToolVersion, string(diffs)); err != nil {
			return fmt.Errorf("insert pricing_replay_evidence: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
