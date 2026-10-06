//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// W6 PR7b 阶段切换的集成测试（真实 PostgreSQL，迁移 200 至 220 已执行）。
// 夹具按一个新建分组的 id 写入，结束时清理它在所有相关表里的行；不写 usage_logs，也不碰其他分组的数据。

type stageIntDeriver struct{ state service.DerivedGroupState }

func (d stageIntDeriver) DeriveGroupCurrent(context.Context, int64) (service.DerivedGroupState, string, error) {
	return d.state, service.PlatformOpenAI, nil
}

func stageIntGroup(t *testing.T) int64 {
	t.Helper()
	gid := mxIntGroup(t)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM pricing_stage_audit WHERE group_id = $1`, gid)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM pricing_replay_evidence WHERE group_id = $1`, gid)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM pricing_shadow_diffs WHERE group_id = $1`, gid)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM pricing_write_approvals WHERE $1 = ANY(group_ids)`, gid)
	})
	return gid
}

func stageIntScalar[T any](t *testing.T, q string, args ...any) T {
	t.Helper()
	var v T
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), q, args...).Scan(&v))
	return v
}

func TestPricingStageSwitch_EndToEnd(t *testing.T) {
	ctx := context.Background()
	gid := stageIntGroup(t)
	long := time.Now().Add(-100 * time.Hour)
	_, err := integrationDB.ExecContext(ctx,
		`INSERT INTO group_model_config (group_id, pricing_stage, stage_changed_at, updated_at) VALUES ($1, 'shadow', $2, $2)`, gid, long)
	require.NoError(t, err)
	for _, key := range []string{"gpt-5.4", "gpt-5.5"} {
		_, err = integrationDB.ExecContext(ctx,
			`INSERT INTO model_group_prices (group_id, model_key, source) VALUES ($1, $2, 'legacy_derived')`, gid, key)
		require.NoError(t, err)
	}

	store := NewPricingStageSwitchStore(integrationDB)
	fp := NewPricingStageFingerprinter(integrationDB)
	derived := service.DerivedGroupState{GroupID: gid, ChannelID: 1, Revision: "rev-int",
		Cells: []service.MatrixCell{
			{ModelKey: "gpt-5.4", Open: true, PriceMode: service.MatrixPriceInherit, Source: service.MatrixSourceLegacyDerived},
			{ModelKey: "gpt-5.5", Open: true, PriceMode: service.MatrixPriceInherit, Source: service.MatrixSourceLegacyDerived},
		}}
	cfgs, err := loadMatrixConfigs(ctx, integrationDB, []int64{gid})
	require.NoError(t, err)
	derived.Config = cfgs[gid].MatrixGroupConfig
	sw := service.NewPricingStageSwitcher(store, stageIntDeriver{derived}, fp, nil, nil, nil)

	// 闸门不满足：没有回放记录。
	p, err := sw.Preview(ctx, service.PricingStagePreviewRequest{GroupID: gid, To: service.PricingStageV2, OperatorID: 1})
	require.NoError(t, err)
	require.False(t, p.Executable)
	require.Zero(t, p.ApprovalID)
	require.Equal(t, service.ReasonPricingGateReplayMissing, p.Gate.Failures[0].Code)
	_, err = sw.Commit(ctx, service.PricingStageSwitchRequest{GroupID: gid, To: service.PricingStageV2, OperatorID: 1, Confirm: true, ApprovalID: 1, AuthMethod: service.AuditAuthMethodJWT})
	require.Error(t, err)
	require.Equal(t, "shadow", stageIntScalar[string](t, `SELECT pricing_stage FROM group_model_config WHERE group_id = $1`, gid))

	// 记一次通过的回放（绑定当前的渠道配置摘要与派生 revision）。
	chain, err := fp.Fingerprint(ctx, []int64{gid})
	require.NoError(t, err)
	now := time.Now().UTC()
	evidence := service.ReplayEvidence{GroupID: gid, RecordedAt: now, WindowFrom: now.Add(-31 * 24 * time.Hour), WindowTo: now.Add(-time.Hour),
		MatrixSource: "derived", Passed: true, BindingStable: true, RowsInWindow: 5, RowsReplayed: 5,
		ChannelConfigHash: chain.ChannelConfigHash, DeriveRevision: "rev-int",
		Diffs: []service.PricingReplayDiffCount{{Kind: "cost", Class: "expected", Reason: service.PricingReplayReasonUnpricedZero, Count: 2}}}
	require.NoError(t, NewPricingReplayEvidenceStore(integrationDB).RecordReplayEvidence(ctx, []service.ReplayEvidence{evidence}))

	// 预览：闸门通过、登记凭证。
	p, err = sw.Preview(ctx, service.PricingStagePreviewRequest{GroupID: gid, To: service.PricingStageV2, OperatorID: 1})
	require.NoError(t, err)
	require.True(t, p.Executable, "%+v", p.Gate)
	require.NotZero(t, p.ApprovalID)
	require.Equal(t, "previewed", stageIntScalar[string](t, `SELECT status FROM pricing_write_approvals WHERE id = $1`, p.ApprovalID))

	// 机器令牌不能确认涉价变更；失败的提交什么都不留下，凭证还能用。
	_, err = sw.Commit(ctx, service.PricingStageSwitchRequest{GroupID: gid, To: service.PricingStageV2, OperatorID: 1, Confirm: true, ApprovalID: p.ApprovalID, AuthMethod: "admin_token"})
	require.Error(t, err)
	require.Equal(t, "shadow", stageIntScalar[string](t, `SELECT pricing_stage FROM group_model_config WHERE group_id = $1`, gid))
	require.Equal(t, "previewed", stageIntScalar[string](t, `SELECT status FROM pricing_write_approvals WHERE id = $1`, p.ApprovalID))
	require.Zero(t, stageIntScalar[int](t, `SELECT COUNT(*) FROM pricing_stage_audit WHERE group_id = $1`, gid))

	// 提交。
	res, err := sw.Commit(ctx, service.PricingStageSwitchRequest{GroupID: gid, To: service.PricingStageV2, OperatorID: 1, Confirm: true, ApprovalID: p.ApprovalID, AuthMethod: service.AuditAuthMethodJWT})
	require.NoError(t, err)
	require.Equal(t, service.StageKindAdvance, res.Kind)
	require.Equal(t, "v2", stageIntScalar[string](t, `SELECT pricing_stage FROM group_model_config WHERE group_id = $1`, gid))
	require.Equal(t, 2, stageIntScalar[int](t, `SELECT COUNT(*) FROM model_group_prices WHERE group_id = $1 AND source = 'legacy_frozen'`, gid))
	require.Equal(t, "consumed", stageIntScalar[string](t, `SELECT status FROM pricing_write_approvals WHERE id = $1`, p.ApprovalID))
	require.Equal(t, 1, stageIntScalar[int](t, `SELECT COUNT(*) FROM pricing_stage_audit WHERE group_id = $1 AND kind = 'advance' AND from_stage = 'shadow' AND to_stage = 'v2' AND interactive AND approval_id = $2`, gid, p.ApprovalID))

	// 分组已经是 v2：用同一个凭证再提交是幂等的空操作（成功返回，不写任何东西，也不再碰凭证）。
	// 凭证一次性的语义在真实库上由 pricing_write_integration_test.go 覆盖。
	again, err := sw.Commit(ctx, service.PricingStageSwitchRequest{GroupID: gid, To: service.PricingStageV2, OperatorID: 1, Confirm: true, ApprovalID: p.ApprovalID, AuthMethod: service.AuditAuthMethodJWT})
	require.NoError(t, err)
	require.False(t, again.Changed)
	require.Equal(t, "noop", again.Kind)
	require.Equal(t, 1, stageIntScalar[int](t, `SELECT COUNT(*) FROM pricing_stage_audit WHERE group_id = $1`, gid), "a noop leaves no audit row")
	require.Equal(t, "consumed", stageIntScalar[string](t, `SELECT status FROM pricing_write_approvals WHERE id = $1`, p.ApprovalID))

	// v2 分组上有人手工加了一行、改了一行：回拨时归档并删除，再按渠道当前配置重新派生。
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO model_group_prices (group_id, model_key, source) VALUES ($1, 'edited', 'manual')`, gid)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM model_group_prices WHERE group_id = $1 AND model_key = 'gpt-5.5'`, gid)
	require.NoError(t, err)

	back, err := sw.Commit(ctx, service.PricingStageSwitchRequest{GroupID: gid, To: service.PricingStageShadow, OperatorID: 1, Confirm: true, AuthMethod: service.AuditAuthMethodJWT})
	require.NoError(t, err)
	require.Equal(t, service.StageKindRollback, back.Kind)
	require.EqualValues(t, 2, back.Archived.Cells, "the frozen row and the manual row")
	require.Equal(t, "shadow", stageIntScalar[string](t, `SELECT pricing_stage FROM group_model_config WHERE group_id = $1`, gid))
	require.Equal(t, 2, stageIntScalar[int](t, `SELECT COUNT(*) FROM model_group_prices WHERE group_id = $1 AND source = 'legacy_derived'`, gid), "derived again from the channel")
	require.Zero(t, stageIntScalar[int](t, `SELECT COUNT(*) FROM model_group_prices WHERE group_id = $1 AND source <> 'legacy_derived'`, gid))
	require.Equal(t, 2, stageIntScalar[int](t, `SELECT COUNT(*) FROM model_group_price_history WHERE group_id = $1 AND action = 'archive'`, gid))
	require.Equal(t, 1, stageIntScalar[int](t, `SELECT COUNT(*) FROM pricing_stage_audit WHERE group_id = $1 AND kind = 'rollback' AND from_stage = 'v2' AND to_stage = 'shadow'`, gid))

	items, err := sw.Audit(ctx, gid, 10)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, service.StageKindRollback, items[0].Kind)
}

func TestPricingStageSwitch_GateFactsFromTheDatabase(t *testing.T) {
	ctx := context.Background()
	gid := stageIntGroup(t)
	long := time.Now().Add(-100 * time.Hour)
	_, err := integrationDB.ExecContext(ctx,
		`INSERT INTO group_model_config (group_id, pricing_stage, stage_changed_at, updated_at) VALUES ($1, 'shadow', $2, $2)`, gid, long)
	require.NoError(t, err)
	for _, class := range []string{"translation", "expected", "expected"} {
		_, err = integrationDB.ExecContext(ctx,
			`INSERT INTO pricing_shadow_diffs (group_id, model, kind, class, legacy_view, v2_view) VALUES ($1, 'GPT-5.4', 'cost', $2, '{}', '{}')`, gid, class)
		require.NoError(t, err)
	}
	// 窗口之前的样本不计。
	_, err = integrationDB.ExecContext(ctx,
		`INSERT INTO pricing_shadow_diffs (created_at, group_id, model, kind, class, legacy_view, v2_view) VALUES ($1, $2, 'old', 'cost', 'translation', '{}', '{}')`,
		time.Now().Add(-20*24*time.Hour), gid)
	require.NoError(t, err)

	store := NewPricingStageSwitchStore(integrationDB)
	facts, err := store.LoadGateFacts(ctx, integrationDB, gid, time.Now())
	require.NoError(t, err)
	require.Equal(t, service.PricingStageShadow, facts.Config.Stage)
	require.EqualValues(t, 1, facts.Shadow.TranslationDiffs)
	require.EqualValues(t, 2, facts.Shadow.ExpectedDiffs)
	require.Equal(t, []string{"gpt-5.4"}, facts.Shadow.ExpectedModels)
	require.Nil(t, facts.Replay)
	require.Nil(t, facts.OwnerChannelUpdatedAt)
}
