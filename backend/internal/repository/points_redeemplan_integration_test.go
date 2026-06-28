//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）—— 换套餐「服务层端到端」集成测试（真实 PG）。
// 覆盖 RedeemToPlan 的单事务 happy path（扣积分 + 发卡同事务原子）与幂等键去重，
// 这是仓储/单测都覆盖不到的 entClient.Tx + AssignOrExtendSubscription 编排。

func TestPointsService_RedeemToPlan_EndToEndAndIdempotent(t *testing.T) {
	ctx := context.Background()
	h := makePointsEarningHarness(t)
	setPointsEarnSettings(t, h.settingRepo, true, pointsTestPeg, 20, 0)
	require.NoError(t, h.settingRepo.Set(ctx, service.SettingKeyPointsRedeemPlanOn, "true"))

	daily := 2.0
	group := mustCreateGroup(t, h.client, &service.Group{
		Name:                "redeem-plan-" + pointsUniq(),
		SubscriptionType:    service.SubscriptionTypeSubscription,
		DailyLimitUSD:       &daily,
		DefaultValidityDays: 30,
	})

	user := mustCreatePointsUser(t, "user")

	// 给足积分（quote price 约 2.0×30×unit；need 数千分）。
	pointsRepo := NewPointsRepository(h.client, integrationDB)
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 1000)
	_, err := pointsRepo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100000, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)
	before := pointsAvailableOf(t, user.ID)
	require.EqualValues(t, 100000, before)

	key := uuid.NewString()
	sub, err := h.pointsSvc.RedeemToPlan(ctx, user.ID, group.ID, 30, key)
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Equal(t, user.ID, sub.UserID)

	// 积分被扣（need>0）。
	after := pointsAvailableOf(t, user.ID)
	require.Less(t, after, before, "redeem must deduct points")

	// 写入恰一条 to_plan 流水（带幂等键）。
	var toPlan int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_points_ledger WHERE user_id=$1 AND kind='to_plan'`, user.ID).Scan(&toPlan))
	require.Equal(t, 1, toPlan)

	// 开通了订阅卡。
	var subCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_subscriptions WHERE user_id=$1 AND group_id=$2`, user.ID, group.ID).Scan(&subCount))
	require.GreaterOrEqual(t, subCount, 1)

	// 幂等：同 key 再兑换 → ErrPointsPlanDuplicate，整事务回滚、不二次扣分、不重复发卡。
	_, err = h.pointsSvc.RedeemToPlan(ctx, user.ID, group.ID, 30, key)
	require.ErrorIs(t, err, service.ErrPointsPlanDuplicate)
	require.Equal(t, after, pointsAvailableOf(t, user.ID), "duplicate redeem must not deduct again")

	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_points_ledger WHERE user_id=$1 AND kind='to_plan'`, user.ID).Scan(&toPlan))
	require.Equal(t, 1, toPlan, "duplicate redeem must not write a second to_plan row")
}
