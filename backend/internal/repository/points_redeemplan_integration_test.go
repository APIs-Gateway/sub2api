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

	user := mustCreatePointsUser(t, "user")

	// 给足积分（覆盖新购、同档续费、异档转套餐补差）。
	pointsRepo := NewPointsRepository(h.client, integrationDB)
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 1000)
	_, err := pointsRepo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 2000000, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)
	before := pointsAvailableOf(t, user.ID)
	require.EqualValues(t, 2000000, before)

	key := uuid.NewString()
	sub, err := h.pointsSvc.RedeemToPlan(ctx, user.ID, 30, 30, key)
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Equal(t, user.ID, sub.UserID)
	require.EqualValues(t, 0, sub.GroupID)
	require.InDelta(t, 30, sub.DailyAmountUSD, 1e-9)

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
		`SELECT COUNT(*) FROM user_subscriptions WHERE user_id=$1 AND group_id IS NULL`, user.ID).Scan(&subCount))
	require.Equal(t, 1, subCount)

	// 幂等：同 key 再兑换 → ErrPointsPlanDuplicate，整事务回滚、不二次扣分、不重复发卡。
	_, err = h.pointsSvc.RedeemToPlan(ctx, user.ID, 30, 30, key)
	require.ErrorIs(t, err, service.ErrPointsPlanDuplicate)
	require.Equal(t, after, pointsAvailableOf(t, user.ID), "duplicate redeem must not deduct again")

	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_points_ledger WHERE user_id=$1 AND kind='to_plan'`, user.ID).Scan(&toPlan))
	require.Equal(t, 1, toPlan, "duplicate redeem must not write a second to_plan row")

	var expireDayBeforeRenew int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT expire_day FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&expireDayBeforeRenew))

	// 已有同 D active 卡时，积分兑换应转为续费：延长同一张卡，不创建第二张卡。
	renewed, err := h.pointsSvc.RedeemToPlan(ctx, user.ID, 30, 30, uuid.NewString())
	require.NoError(t, err)
	require.Equal(t, sub.ID, renewed.ID)
	require.Greater(t, pointsAvailableOf(t, user.ID), int64(0))
	require.Less(t, pointsAvailableOf(t, user.ID), after, "renew by points must deduct points")

	var expireDayAfterRenew int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT expire_day FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&expireDayAfterRenew))
	require.Greater(t, expireDayAfterRenew, expireDayBeforeRenew)
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_subscriptions WHERE user_id=$1 AND status='active' AND expires_at > NOW()`, user.ID).Scan(&subCount))
	require.Equal(t, 1, subCount, "renew must keep single active subscription")

	afterRenew := pointsAvailableOf(t, user.ID)

	// 已有不同 D active 卡时，积分兑换应转为转套餐：关旧卡、开新卡，仍保持单 active 卡。
	changed, err := h.pointsSvc.RedeemToPlan(ctx, user.ID, 60, 90, uuid.NewString())
	require.NoError(t, err)
	require.NotEqual(t, sub.ID, changed.ID)
	require.InDelta(t, 60, changed.DailyAmountUSD, 1e-9)
	require.Less(t, pointsAvailableOf(t, user.ID), afterRenew, "change plan by points must deduct only the diff")

	var oldStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT status FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&oldStatus))
	require.Equal(t, service.SubscriptionStatusExpired, oldStatus)
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_subscriptions WHERE user_id=$1 AND status='active' AND expires_at > NOW()`, user.ID).Scan(&subCount))
	require.Equal(t, 1, subCount, "change plan must keep single active subscription")

	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_points_ledger WHERE user_id=$1 AND kind='to_plan'`, user.ID).Scan(&toPlan))
	require.Equal(t, 3, toPlan, "purchase, renew, and change-plan each write one to_plan row")
}
