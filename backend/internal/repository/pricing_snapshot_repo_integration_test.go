//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"

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
