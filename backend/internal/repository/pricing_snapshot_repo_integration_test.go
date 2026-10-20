//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// W6 PR9 价格快照表的集成测试（真实 PostgreSQL，迁移 216 已执行）。表没有其他使用方，测试前后整表清空。
func resetPricingSnapshots(t *testing.T) {
	t.Helper()
	clean := func() { _, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM pricing_snapshots`) }
	clean()
	t.Cleanup(clean)
}

func snapshotInput(label, sha string, payload string) service.NewPricingSnapshot {
	return service.NewPricingSnapshot{Label: label, Source: service.PricingSnapshotSourceBootstrap,
		ContentSHA256: sha, ModelCount: 1, Payload: []byte(payload)}
}

func TestPricingSnapshotRepo_ActivateSwitchesActiveRowInOneTransaction(t *testing.T) {
	ctx := context.Background()
	resetPricingSnapshots(t)
	repo := NewPricingSnapshotRepository(integrationDB)

	_, err := repo.GetActiveMeta(ctx)
	require.ErrorIs(t, err, service.ErrPricingSnapshotNotFound)

	shaA, shaB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	a, err := repo.ActivateNew(ctx, snapshotInput("a", shaA, `{"gpt-5.1":{"input_cost_per_token":1e-6}}`))
	require.NoError(t, err)
	require.Equal(t, service.PricingSnapshotStatusActive, a.Status)
	require.NotNil(t, a.ApprovedAt)

	b, err := repo.ActivateNew(ctx, snapshotInput("b", shaB, `{"gpt-5.1":{"input_cost_per_token":2e-6}}`))
	require.NoError(t, err)

	active, err := repo.GetActiveMeta(ctx)
	require.NoError(t, err)
	require.Equal(t, b.ID, active.ID)

	var statusA string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM pricing_snapshots WHERE id = $1`, a.ID).Scan(&statusA))
	require.Equal(t, service.PricingSnapshotStatusSuperseded, statusA)
	var actives int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pricing_snapshots WHERE status = 'active'`).Scan(&actives))
	require.Equal(t, 1, actives)

	payload, err := repo.GetPayload(ctx, b.ID)
	require.NoError(t, err)
	require.JSONEq(t, `{"gpt-5.1":{"input_cost_per_token":2e-6}}`, string(payload))
	_, err = repo.GetPayload(ctx, b.ID+1000)
	require.ErrorIs(t, err, service.ErrPricingSnapshotNotFound)
}

func TestPricingSnapshotRepo_UniqueIndexesGuardActiveAndCandidates(t *testing.T) {
	ctx := context.Background()
	resetPricingSnapshots(t)
	sha := strings.Repeat("c", 64)
	insert := func(status string) error {
		_, err := integrationDB.ExecContext(ctx,
			`INSERT INTO pricing_snapshots (label, source, content_sha256, model_count, payload_gz, status)
			 VALUES ('x', 'remote', $1, 1, '\x00', $2)`, sha, status)
		return err
	}

	require.NoError(t, insert("active"))
	requireMatrixPGCode(t, insert("active"), "23505") // 同时只能有一个生效快照

	require.NoError(t, insert("candidate"))
	requireMatrixPGCode(t, insert("candidate"), "23505") // 同一内容的候选只保留一份
	require.NoError(t, insert("superseded"))             // 其他状态不受去重限制
	require.NoError(t, insert("rejected"))
}

func TestPricingSnapshotRepo_CandidatesApprovalAndCleanup(t *testing.T) {
	ctx := context.Background()
	resetPricingSnapshots(t)
	repo := NewPricingSnapshotRepository(integrationDB)

	base, err := repo.ActivateNew(ctx, snapshotInput("base", strings.Repeat("a", 64), `{"m":{"input_cost_per_token":1e-6}}`))
	require.NoError(t, err)

	// 候选按内容哈希去重。
	candIn := snapshotInput("cand", strings.Repeat("b", 64), `{"m":{"input_cost_per_token":2e-6}}`)
	candIn.Source = service.PricingSnapshotSourceRemote
	cand, created, err := repo.InsertCandidate(ctx, candIn)
	require.NoError(t, err)
	require.True(t, created)
	again, created, err := repo.InsertCandidate(ctx, candIn)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, cand.ID, again.ID)

	got, err := repo.GetMeta(ctx, cand.ID)
	require.NoError(t, err)
	require.Equal(t, service.PricingSnapshotStatusCandidate, got.Status)
	cands, err := repo.List(ctx, []string{service.PricingSnapshotStatusCandidate}, 10)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	all, err := repo.List(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, all, 2)

	// 批准：基线对得上才成功；Check 失败整体回滚（生效快照、候选状态与差异行都不变）。
	merged := snapshotInput("merged", strings.Repeat("c", 64), `{"m":{"input_cost_per_token":2e-6}}`)
	merged.Source = service.PricingSnapshotSourceMerged
	merged.ParentSnapshotID, merged.CandidateSnapshotID = &base.ID, &cand.ID
	req := service.ApplyMergedSnapshot{
		New: merged, ExpectedActiveID: base.ID, ExpectedActiveSHA: base.ContentSHA256, ConsumeCandidate: true,
		Diffs: []service.PricingSnapshotDiffRecord{{ModelKey: "m", ChangeType: service.PricingDiffChanged,
			OldPrice: []byte(`{"input_cost_per_token":1e-6}`), NewPrice: []byte(`{"input_cost_per_token":2e-6}`),
			ChangedFields: []string{"input_cost_per_token"}, Decision: service.PricingDiffDecisionApprove}},
	}

	stale := req
	stale.ExpectedActiveSHA = strings.Repeat("0", 64)
	_, err = repo.ApplyMerged(ctx, stale)
	require.ErrorIs(t, err, service.ErrPricingSnapshotBaselineChanged)

	rejected := req
	rejected.Check = func(_ context.Context, exec service.MatrixExecutor) error {
		// 校验看得到事务里刚写入的新生效行。
		rows, qerr := exec.QueryContext(ctx, `SELECT count(*) FROM pricing_snapshots WHERE status = 'active' AND label = 'merged'`)
		require.NoError(t, qerr)
		defer func() { _ = rows.Close() }()
		require.True(t, rows.Next())
		var n int
		require.NoError(t, rows.Scan(&n))
		require.Equal(t, 1, n)
		return context.DeadlineExceeded
	}
	_, err = repo.ApplyMerged(ctx, rejected)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	active, err := repo.GetActiveMeta(ctx)
	require.NoError(t, err)
	require.Equal(t, base.ID, active.ID, "Check 失败后旧快照仍是生效快照")
	var diffRows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM pricing_snapshot_diffs`).Scan(&diffRows))
	require.Zero(t, diffRows)

	approved, err := repo.ApplyMerged(ctx, req)
	require.NoError(t, err)
	require.Equal(t, service.PricingSnapshotSourceMerged, approved.Source)
	active, err = repo.GetActiveMeta(ctx)
	require.NoError(t, err)
	require.Equal(t, approved.ID, active.ID)
	baseAfter, err := repo.GetMeta(ctx, base.ID)
	require.NoError(t, err)
	require.Equal(t, service.PricingSnapshotStatusSuperseded, baseAfter.Status)
	candAfter, err := repo.GetMeta(ctx, cand.ID)
	require.NoError(t, err)
	require.Equal(t, service.PricingSnapshotStatusSuperseded, candAfter.Status, "没有搁置时候选被消耗")

	var decision string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT decision FROM pricing_snapshot_diffs WHERE snapshot_id = $1 AND model_key = 'm'`, cand.ID).Scan(&decision))
	require.Equal(t, "approve", decision)

	// 再次批准同一候选的同一模型：差异行 upsert，不报主键冲突。
	merged2 := snapshotInput("merged2", strings.Repeat("d", 64), `{"m":{"input_cost_per_token":3e-6}}`)
	merged2.Source = service.PricingSnapshotSourceMerged
	merged2.CandidateSnapshotID = &cand.ID
	req2 := service.ApplyMergedSnapshot{New: merged2, ExpectedActiveID: approved.ID, ExpectedActiveSHA: approved.ContentSHA256,
		Diffs: []service.PricingSnapshotDiffRecord{{ModelKey: "m", ChangeType: service.PricingDiffChanged, Decision: service.PricingDiffDecisionHold}}}
	_, err = repo.ApplyMerged(ctx, req2)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT decision FROM pricing_snapshot_diffs WHERE snapshot_id = $1 AND model_key = 'm'`, cand.ID).Scan(&decision))
	require.Equal(t, "hold", decision)

	// 拒绝与清理：只清理未批准的候选与已拒绝快照，生效与被替换的快照不动；差异行随候选级联删除。
	c2In := snapshotInput("c2", strings.Repeat("e", 64), `{"m":{}}`)
	c2In.Source = service.PricingSnapshotSourceRemote
	c2, _, err := repo.InsertCandidate(ctx, c2In)
	require.NoError(t, err)
	require.NoError(t, repo.RejectCandidate(ctx, c2.ID))
	require.ErrorIs(t, repo.RejectCandidate(ctx, c2.ID), service.ErrPricingSnapshotNotCandidate)
	_, err = integrationDB.ExecContext(ctx, `UPDATE pricing_snapshots SET fetched_at = NOW() - INTERVAL '40 days'`)
	require.NoError(t, err)
	deleted, err := repo.DeleteExpiredCandidates(ctx, time.Now().Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted, "只有已拒绝的 c2；被消耗的候选已是 superseded，不在清理范围")
	_, err = repo.GetMeta(ctx, c2.ID)
	require.ErrorIs(t, err, service.ErrPricingSnapshotNotFound)
}

func TestPricingSnapshotRepo_RecentBillingModels_Integration(t *testing.T) {
	ctx := context.Background()
	repo := NewPricingSnapshotRepository(integrationDB)
	models, err := repo.RecentBillingModels(ctx, 7)
	require.NoError(t, err)
	require.LessOrEqual(t, len(models), 5000)
}
