//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 「邀请注册即得积分」仓储层集成测试（真实 PG）。
//
// 单测验不了这里的要害：partial-unique on (source_user_id) WHERE kind='signup_reward'
// 是纯数据库行为，只有打到真实 PG 才知道那条幂等闸门到底拦不拦得住。
// 而这条闸门就是「每人只发一次」的最终保证——注册流程重放、绑定接口被重复调用，
// 都得撞在它上面。

// TestPointsRepo_GrantSignupReward_CreditsInviter 主路径：钱进邀请人的可用余额，且不冻结。
func TestPointsRepo_GrantSignupReward_CreditsInviter(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")

	applied, err := repo.GrantSignupReward(ctx, service.SignupRewardInput{
		InviterID:    inviter.ID,
		SourceUserID: invitee.ID,
		Points:       50,
		PegAt:        pointsTestPeg,
	})
	require.NoError(t, err)
	require.True(t, applied)

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(50), acct.Available, "注册奖励直接进可用：没有付款可退，冻结它只会平白延迟到账")
	require.Zero(t, acct.Frozen)
	require.Equal(t, int64(50), acct.LifetimeEarned)

	require.Equal(t, 1, pointsLedgerCount(t, inviter.ID, service.PointsKindSignupReward))

	// 快照必须回填，否则流水页显示的「操作后余额」会是空的
	availAfter := querySingleInt(t, ctx, integrationEntClient,
		`SELECT COALESCE(available_after, -1) FROM user_points_ledger WHERE kind = $1 AND source_user_id = $2`,
		service.PointsKindSignupReward, invitee.ID)
	require.Equal(t, 50, availAfter)

	frozenAfter := querySingleInt(t, ctx, integrationEntClient,
		`SELECT COALESCE(frozen_after, -1) FROM user_points_ledger WHERE kind = $1 AND source_user_id = $2`,
		service.PointsKindSignupReward, invitee.ID)
	require.Equal(t, 0, frozenAfter)
}

// TestPointsRepo_GrantSignupReward_OncePerInvitee 是这组里最关键的一条：
// 同一个被邀请人重复触发只入账一次，第二次必须返回未入账且不改动账户。
func TestPointsRepo_GrantSignupReward_OncePerInvitee(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")

	in := service.SignupRewardInput{
		InviterID:    inviter.ID,
		SourceUserID: invitee.ID,
		Points:       50,
		PegAt:        pointsTestPeg,
	}

	applied, err := repo.GrantSignupReward(ctx, in)
	require.NoError(t, err)
	require.True(t, applied)

	applied, err = repo.GrantSignupReward(ctx, in)
	require.NoError(t, err, "重复触发不是错误，安静地什么都不做即可")
	require.False(t, applied)

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(50), acct.Available, "第二次绝不能再加一次")
	require.Equal(t, int64(50), acct.LifetimeEarned)
	require.Equal(t, 1, pointsLedgerCount(t, inviter.ID, service.PointsKindSignupReward))
}

// TestPointsRepo_GrantSignupReward_PerInviteeNotPerInviter 确认幂等键是被邀请人而非邀请人：
// 同一个邀请人拉来两个不同的新用户，应该拿到两笔奖励。
// 如果唯一索引误建在 user_id 上，这条用例会立刻红。
func TestPointsRepo_GrantSignupReward_PerInviteeNotPerInviter(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	first := mustCreatePointsUser(t, "user")
	second := mustCreatePointsUser(t, "user")

	for _, invitee := range []*service.User{first, second} {
		applied, err := repo.GrantSignupReward(ctx, service.SignupRewardInput{
			InviterID:    inviter.ID,
			SourceUserID: invitee.ID,
			Points:       30,
			PegAt:        pointsTestPeg,
		})
		require.NoError(t, err)
		require.True(t, applied, "不同被邀请人应各发一次")
	}

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(60), acct.Available)
	require.Equal(t, 2, pointsLedgerCount(t, inviter.ID, service.PointsKindSignupReward))
}

// TestPointsRepo_GrantSignupReward_RejectsInvalidInput 非法输入一律安静拒绝，
// 既不入账也不报错——这些都是上层没配好的正常状态，不该在仓储层炸出来。
func TestPointsRepo_GrantSignupReward_RejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")

	cases := map[string]service.SignupRewardInput{
		"积分为零":      {InviterID: inviter.ID, SourceUserID: invitee.ID, Points: 0},
		"积分为负":      {InviterID: inviter.ID, SourceUserID: invitee.ID, Points: -10},
		"邀请人 ID 空":  {InviterID: 0, SourceUserID: invitee.ID, Points: 50},
		"被邀请人 ID 空": {InviterID: inviter.ID, SourceUserID: 0, Points: 50},
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			applied, err := repo.GrantSignupReward(ctx, in)
			require.NoError(t, err)
			require.False(t, applied)
		})
	}

	require.Zero(t, pointsLedgerCount(t, inviter.ID, service.PointsKindSignupReward))
}
