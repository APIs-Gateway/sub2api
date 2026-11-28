//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 「被邀请人消费达标后发注册奖励」的仓储层集成测试（真实 PG）。
//
// 重点验只有真实数据库才说得清的事：
//  1. 候选名单与发放用同一个口径——已发过奖励 / 没有邀请人 / 邀请人被封禁 / since 之前注册的都不出现；
//  2. 累计实际消费就是 usage_logs.actual_cost 的求和，按 cost_unit 分开，只含传进来的用户；
//  3. 邀请人被封禁或已删除时发放本身不入账。
//
// 隔离方式：每个用例用 testEntTx 开一个事务，所有写入（users、user_affiliates、api_keys、accounts、
// usage_logs、积分流水）都在这个事务里，用例结束自动回滚，不会在共享测试库里留下任何数据，
// 也就不会污染 TestUsageLogRepoSuite 这类按全库统计的用例。
// repo 用事务内的 ent client 构造，ctx 带上 dbent.NewTxContext，仓储内部的 withTx 会加入这个事务。

type sweepEnv struct {
	t      *testing.T
	ctx    context.Context // 带事务的 ctx
	client *dbent.Client   // 事务内的 ent client
	repo   service.PointsRepository
}

func newSweepEnv(t *testing.T) *sweepEnv {
	t.Helper()
	tx := testEntTx(t) // 结束时自动 Rollback
	return &sweepEnv{
		t:      t,
		ctx:    dbent.NewTxContext(context.Background(), tx),
		client: tx.Client(),
		repo:   NewPointsRepository(tx.Client(), integrationDB),
	}
}

func (e *sweepEnv) user() *service.User {
	e.t.Helper()
	return mustCreateUser(e.t, e.client, &service.User{
		Email:       fmt.Sprintf("sweep-user-%s@example.com", pointsUniq()),
		Username:    "sweep_" + pointsUniq(),
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Concurrency: 5,
	})
}

func (e *sweepEnv) exec(query string, args ...any) {
	e.t.Helper()
	_, err := e.client.ExecContext(e.ctx, query, args...)
	require.NoError(e.t, err)
}

func (e *sweepEnv) bindInviter(inviteeID, inviterID int64) {
	e.t.Helper()
	e.exec(`INSERT INTO user_affiliates (user_id, aff_code, inviter_id) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO UPDATE SET inviter_id = EXCLUDED.inviter_id`,
		inviteeID, fmt.Sprintf("SW%s", pointsUniq()), inviterID)
}

func (e *sweepEnv) insertUsageCost(userID int64, costUnit *int16, costs ...float64) {
	e.t.Helper()
	account := mustCreateAccount(e.t, e.client, &service.Account{Name: "sweep-usage-account-" + pointsUniq()})
	apiKey := mustCreateApiKey(e.t, e.client, &service.APIKey{UserID: userID})
	for _, c := range costs {
		e.exec(`INSERT INTO usage_logs (user_id, api_key_id, account_id, model, input_tokens, output_tokens, total_cost, actual_cost, cost_unit, created_at)
			 VALUES ($1, $2, $3, 'gpt-test', 1, 1, $4, $4, $5, $6)`,
			userID, apiKey.ID, account.ID, c, costUnit, time.Now().UTC())
	}
}

func (e *sweepEnv) setUserStatus(userID int64, status string) {
	e.t.Helper()
	e.exec(`UPDATE users SET status = $2 WHERE id = $1`, userID, status)
}

func (e *sweepEnv) signupRewardLedgerCount(userID int64) int {
	e.t.Helper()
	return querySingleInt(e.t, e.ctx, e.client,
		`SELECT COUNT(*) FROM user_points_ledger WHERE user_id = $1 AND kind = $2`, userID, service.PointsKindSignupReward)
}

func TestPointsRepo_SumUsageActualCost_SplitsByCostUnit(t *testing.T) {
	e := newSweepEnv(t)
	a := e.user()
	b := e.user()
	idle := e.user()

	cny := service.UsageLogCostUnitCNY
	e.insertUsageCost(a.ID, nil, 0.25, 0.5, 0.3) // 额度行（cost_unit NULL）
	e.insertUsageCost(a.ID, &cny, 2)             // 人民币行
	e.insertUsageCost(b.ID, nil, 2)

	got, err := e.repo.SumUsageActualCost(e.ctx, []int64{a.ID, b.ID, idle.ID})
	require.NoError(t, err)

	byKey := map[string]float64{}
	for _, row := range got {
		byKey[fmt.Sprintf("%d/%t", row.UserID, row.CostUnitCNY)] = row.ActualCost
	}
	require.InDelta(t, 1.05, byKey[fmt.Sprintf("%d/false", a.ID)], 1e-6, "额度行和人民币行必须分开求和，换算交给 service 层")
	require.InDelta(t, 2.0, byKey[fmt.Sprintf("%d/true", a.ID)], 1e-6)
	require.InDelta(t, 2.0, byKey[fmt.Sprintf("%d/false", b.ID)], 1e-6)
	for _, row := range got {
		require.NotEqual(t, idle.ID, row.UserID, "没有用量的用户不出现，调用方按 0 处理")
	}

	empty, err := e.repo.SumUsageActualCost(e.ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestPointsRepo_ListSignupRewardCandidates_Filters(t *testing.T) {
	e := newSweepEnv(t)
	inviter := e.user()
	bannedInviter := e.user()
	pending := e.user()
	rewarded := e.user()
	unbound := e.user()
	underBanned := e.user()
	old := e.user()

	e.bindInviter(pending.ID, inviter.ID)
	e.bindInviter(rewarded.ID, inviter.ID)
	e.bindInviter(underBanned.ID, bannedInviter.ID)
	e.bindInviter(old.ID, inviter.ID)
	e.exec(`INSERT INTO user_affiliates (user_id, aff_code) VALUES ($1, $2) ON CONFLICT (user_id) DO NOTHING`,
		unbound.ID, fmt.Sprintf("SW%s", pointsUniq()))

	e.setUserStatus(bannedInviter.ID, "disabled")

	// since 之前注册的被邀请人：把 created_at 改到很久以前
	e.exec(`UPDATE users SET created_at = $2 WHERE id = $1`, old.ID, time.Now().UTC().Add(-48*time.Hour))

	applied, err := e.repo.GrantSignupReward(e.ctx, service.SignupRewardInput{
		InviterID: inviter.ID, SourceUserID: rewarded.ID, Points: 5, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)
	require.True(t, applied)

	// 游标从本用例创建的第一个用户之前开始：本事务里的 ID 是连续新分配的，
	// 区间内只有本用例的用户，共享库里其它测试遗留的数据不会混进来。
	after := inviter.ID - 1
	since := time.Now().UTC().Add(-24 * time.Hour)
	got, err := e.repo.ListSignupRewardCandidates(e.ctx, since, after, 100)
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
	none, err := e.repo.ListSignupRewardCandidates(e.ctx, time.Time{}, after, 100)
	require.NoError(t, err)
	require.Empty(t, none)

	// limit 生效
	one, err := e.repo.ListSignupRewardCandidates(e.ctx, since, after, 1)
	require.NoError(t, err)
	require.LessOrEqual(t, len(one), 1)
}

// 邀请人被封禁或已删除时，发放本身也不入账（注册即发与达标后发两条路径共用这一步）。
func TestPointsRepo_GrantSignupReward_SkipsInactiveInviter(t *testing.T) {
	e := newSweepEnv(t)

	banned := e.user()
	inviteeA := e.user()
	e.setUserStatus(banned.ID, "disabled")
	applied, err := e.repo.GrantSignupReward(e.ctx, service.SignupRewardInput{InviterID: banned.ID, SourceUserID: inviteeA.ID, Points: 5, PegAt: pointsTestPeg})
	require.NoError(t, err)
	require.False(t, applied)
	require.Zero(t, e.signupRewardLedgerCount(banned.ID))

	deleted := e.user()
	inviteeB := e.user()
	e.exec(`UPDATE users SET deleted_at = NOW() WHERE id = $1`, deleted.ID)
	applied, err = e.repo.GrantSignupReward(e.ctx, service.SignupRewardInput{InviterID: deleted.ID, SourceUserID: inviteeB.ID, Points: 5, PegAt: pointsTestPeg})
	require.NoError(t, err)
	require.False(t, applied)
	require.Zero(t, e.signupRewardLedgerCount(deleted.ID))
}
