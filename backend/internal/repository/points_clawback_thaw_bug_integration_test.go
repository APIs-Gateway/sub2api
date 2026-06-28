//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 边界 bug 探测：clawback 扣 frozen 后，冻结到期 lazy thaw 是否会把「已被 clawback 撤回的」积分
// 重新搬进 available（= clawback 被 thaw 抵消、积分复活）。
//
// 触发链（freeze 默认=退款窗口，退款发生在冻结期内 = 常态）：
//  1. earn 100 进 frozen（earn 流水行 frozen_until=未来）。
//  2. 冻结期内退款 → ClawbackByOrder 扣 frozen（account.frozen: 100→0），但**不清 earn 行的 frozen_until**。
//  3. 冻结到期 → ThawDuePoints 按 frozen_until 把该 earn 行的**原始 points=100** 搬进 available。
//     → 结果 available=100、frozen=-100：被撤回的 100 积分复活为可用。
//
// 本测试断言「正确行为」（撤回的积分不得复活）。若 bug 存在 → 断言失败、暴露资损。
func TestPointsRepo_ClawbackThenThaw_MustNotRecreditClawedPoints_FULL(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	// 1) earn 100，冻结 24h。
	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: inviter.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, FreezeHours: 24, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)
	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(100), acct.Frozen)
	require.Equal(t, int64(0), acct.Available)

	// 2) 冻结期内全额退款 → clawback 扣 frozen。
	clawed, err := repo.ClawbackByOrder(ctx, orderID, 100, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), clawed)
	acct, err = repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), acct.Frozen, "clawback 扣光 frozen")
	require.Equal(t, int64(0), acct.Available)

	// 3) 把冻结到期时间改到过去，模拟冻结期满。
	_, err = integrationDB.ExecContext(ctx,
		`UPDATE user_points_ledger SET frozen_until = NOW() - interval '1 hour'
		 WHERE user_id=$1 AND kind='earn' AND source_order_id=$2`, inviter.ID, orderID)
	require.NoError(t, err)

	// 4) lazy thaw（生产中读账户时触发）。
	thawed, err := repo.ThawDuePoints(ctx, inviter.ID)
	require.NoError(t, err)

	acct, err = repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)

	// 正确行为：被 clawback 撤回的积分**不得**因 thaw 复活。
	require.Equal(t, int64(0), thawed, "已被撤回的冻结积分不应再解冻")
	require.Equal(t, int64(0), acct.Available, "撤回的积分不得被 thaw 重新计入可用（资损）")
	require.Equal(t, int64(0), acct.Frozen, "frozen 不得被 thaw 拉成负数")
}

// 部分 clawback 版：earn 100 frozen，退 40 → 应只「净 60」可解冻；若 bug 存在则解冻满 100。
func TestPointsRepo_ClawbackThenThaw_MustNotRecreditClawedPoints_PARTIAL(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: inviter.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, FreezeHours: 24, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	// 部分退 40/100 → clawback floor(100×40/100)=40，从 frozen 扣。
	clawed, err := repo.ClawbackByOrder(ctx, orderID, 40, 100)
	require.NoError(t, err)
	require.Equal(t, int64(40), clawed)
	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(60), acct.Frozen, "clawback 后 frozen=60")
	require.Equal(t, int64(0), acct.Available)

	_, err = integrationDB.ExecContext(ctx,
		`UPDATE user_points_ledger SET frozen_until = NOW() - interval '1 hour'
		 WHERE user_id=$1 AND kind='earn' AND source_order_id=$2`, inviter.ID, orderID)
	require.NoError(t, err)

	_, err = repo.ThawDuePoints(ctx, inviter.ID)
	require.NoError(t, err)

	acct, err = repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	// 正确：净剩 60 解冻为可用；frozen 归 0。
	require.Equal(t, int64(60), acct.Available, "只应解冻未被撤回的 60")
	require.Equal(t, int64(0), acct.Frozen)
}
