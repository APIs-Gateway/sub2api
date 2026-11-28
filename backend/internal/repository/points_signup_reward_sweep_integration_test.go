//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 「被邀请人消费达标后发注册奖励」的仓储层集成测试（真实 PG）。
//
// 重点验两件只有真实数据库才说得清的事：
//  1. 候选名单与发放用同一个口径——已经发过奖励的被邀请人不会再出现；
//  2. 累计实际消费就是 usage_logs.actual_cost 的求和，只含传进来的用户。

func bindInviterForSweepTest(t *testing.T, inviteeID, inviterID int64) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(),
		`INSERT INTO user_affiliates (user_id, aff_code, inviter_id) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO UPDATE SET inviter_id = EXCLUDED.inviter_id`,
		inviteeID, fmt.Sprintf("SW%s", pointsUniq()), inviterID)
	require.NoError(t, err)
}

func insertUsageCostForSweepTest(t *testing.T, userID int64, costUnit *int16, costs ...float64) {
	t.Helper()
	account := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "sweep-usage-account-" + pointsUniq()})
	apiKey := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: userID})
	for _, c := range costs {
		_, err := integrationDB.ExecContext(context.Background(),
			`INSERT INTO usage_logs (user_id, api_key_id, account_id, model, input_tokens, output_tokens, total_cost, actual_cost, cost_unit, created_at)
			 VALUES ($1, $2, $3, 'gpt-test', 1, 1, $4, $4, $5, $6)`,
			userID, apiKey.ID, account.ID, c, costUnit, time.Now().UTC())
		require.NoError(t, err)
	}
}

func setUserStatusForSweepTest(t *testing.T, userID int64, status string) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE users SET status = $2 WHERE id = $1`, userID, status)
	require.NoError(t, err)
}

func TestPointsRepo_SumUsageActualCost_SplitsByCostUnit(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	a := mustCreatePointsUser(t, "user")
	b := mustCreatePointsUser(t, "user")
	idle := mustCreatePointsUser(t, "user")

	cny := service.UsageLogCostUnitCNY
	insertUsageCostForSweepTest(t, a.ID, nil, 0.25, 0.5, 0.3) // 额度行（cost_unit NULL）
	insertUsageCostForSweepTest(t, a.ID, &cny, 2)             // 人民币行
	insertUsageCostForSweepTest(t, b.ID, nil, 2)

	got, err := repo.SumUsageActualCost(ctx, []int64{a.ID, b.ID, idle.ID})
	require.NoError(t, err)

	byKey := map[string]float64{}
	for _, row := range got {
		byKey[fmt.Sprintf("%d/%t", row.UserID, row.CostUnitCNY)] = row.ActualCost
	}
	require.InDelta(t, 1.05, byKey[fmt.Sprintf("%d/false", a.ID)], 1e-6, "额度行和人民币行必须分开求和，换算交给 service 层")
	require.InDelta(t, 2.0, byKey[fmt.Sprintf("%d/true", a.ID)], 1e-6)
	require.InDelta(t, 2.0, byKey[fmt.Sprintf("%d/false", b.ID)], 1e-6)
	for key := range byKey {
		require.NotContains(t, key, fmt.Sprintf("%d/", idle.ID), "没有用量的用户不出现，调用方按 0 处理")
	}

	empty, err := repo.SumUsageActualCost(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestPointsRepo_ListSignupRewardCandidates_Filters(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	bannedInviter := mustCreatePointsUser(t, "user")
	pending := mustCreatePointsUser(t, "user")
	rewarded := mustCreatePointsUser(t, "user")
	unbound := mustCreatePointsUser(t, "user")
	underBanned := mustCreatePointsUser(t, "user")
	old := mustCreatePointsUser(t, "user")

	bindInviterForSweepTest(t, pending.ID, inviter.ID)
	bindInviterForSweepTest(t, rewarded.ID, inviter.ID)
	bindInviterForSweepTest(t, underBanned.ID, bannedInviter.ID)
	bindInviterForSweepTest(t, old.ID, inviter.ID)
	_, err := integrationDB.ExecContext(ctx,
		`INSERT INTO user_affiliates (user_id, aff_code) VALUES ($1, $2) ON CONFLICT (user_id) DO NOTHING`, unbound.ID, fmt.Sprintf("SW%s", pointsUniq()))
	require.NoError(t, err)

	setUserStatusForSweepTest(t, bannedInviter.ID, "disabled")

	// since 之前注册的被邀请人：把 created_at 改到很久以前
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET created_at = $2 WHERE id = $1`, old.ID, time.Now().UTC().Add(-48*time.Hour))
	require.NoError(t, err)

	applied, err := repo.GrantSignupReward(ctx, service.SignupRewardInput{
		InviterID: inviter.ID, SourceUserID: rewarded.ID, Points: 5, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)
	require.True(t, applied)

	since := time.Now().UTC().Add(-24 * time.Hour)
	got, err := repo.ListSignupRewardCandidates(ctx, since, 0, 10000)
	require.NoError(t, err)

	var ids []int64
	for _, c := range got {
		ids = append(ids, c.InviteeID)
		if c.InviteeID == pending.ID {
			require.Equal(t, inviter.ID, c.InviterID)
		}
	}
	require.Contains(t, ids, pending.ID)
	require.NotContains(t, ids, rewarded.ID, "已经发过奖励的不能再出现，否则会白白重复扫描")
	require.NotContains(t, ids, unbound.ID, "没有邀请人的不是候选")
	require.NotContains(t, ids, underBanned.ID, "邀请人被封禁的不发")
	require.NotContains(t, ids, old.ID, "since 之前注册的被邀请人不碰")

	// since 为零值：不返回任何东西（扫描必须有明确的生效起点）
	none, err := repo.ListSignupRewardCandidates(ctx, time.Time{}, 0, 100)
	require.NoError(t, err)
	require.Empty(t, none)

	// limit 生效
	one, err := repo.ListSignupRewardCandidates(ctx, since, 0, 1)
	require.NoError(t, err)
	require.LessOrEqual(t, len(one), 1)
}

// 邀请人被封禁或已删除时，发放本身也不入账（注册即发与达标后发两条路径共用这一步）。
func TestPointsRepo_GrantSignupReward_SkipsInactiveInviter(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()

	banned := mustCreatePointsUser(t, "user")
	inviteeA := mustCreatePointsUser(t, "user")
	setUserStatusForSweepTest(t, banned.ID, "disabled")
	applied, err := repo.GrantSignupReward(ctx, service.SignupRewardInput{InviterID: banned.ID, SourceUserID: inviteeA.ID, Points: 5, PegAt: pointsTestPeg})
	require.NoError(t, err)
	require.False(t, applied)
	require.Zero(t, pointsLedgerCount(t, banned.ID, service.PointsKindSignupReward))

	deleted := mustCreatePointsUser(t, "user")
	inviteeB := mustCreatePointsUser(t, "user")
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET deleted_at = NOW() WHERE id = $1`, deleted.ID)
	require.NoError(t, err)
	applied, err = repo.GrantSignupReward(ctx, service.SignupRewardInput{InviterID: deleted.ID, SourceUserID: inviteeB.ID, Points: 5, PegAt: pointsTestPeg})
	require.NoError(t, err)
	require.False(t, applied)
	require.Zero(t, pointsLedgerCount(t, deleted.ID, service.PointsKindSignupReward))
}
