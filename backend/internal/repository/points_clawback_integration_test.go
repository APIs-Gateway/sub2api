//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）—— clawback 钩子「服务层端到端」集成测试（真实 PG）。
//
// 补 spec §9 / §11.3② 待办：clawback 的取整/转负/幂等已由 points_repo_integration_test.go 直测，
// 但「clawback **仅** 在 markRefundOk（退款网关成功落单）触发、网关失败 / 未落单不触发」这条**挂点 wiring**
// 此前无端到端守卫。本文件驱动真实 ExecuteRefund：
//   - 退款成功（空 tradeNo → 网关成功 → PartiallyRefunded）→ 按 RefundAmount/Amount 比例撤回邀请人积分；
//   - 网关失败（非空 tradeNo → handleGwFail 回滚）→ **不撤**（积分纹丝不动、无 clawback 流水）。
//
// earn 由仓储直接 seed（clawback 只按 source_order_id 读该单 earn 流水，与 earn 如何产生解耦），
// 聚焦验证「挂点」而非重复 earn 路径。

func clawbackLedgerCountForOrder(t *testing.T, orderID int64) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM user_points_ledger WHERE kind='clawback' AND source_order_id=$1`, orderID).Scan(&n))
	return n
}

// seedEarnForOrder 直接为 inviter 记一笔来源单 earn（无冻结，全部可用）。
func seedEarnForOrder(t *testing.T, h *pointsEarningHarness, inviterID, inviteeID, orderID, points int64) {
	t.Helper()
	pointsRepo := NewPointsRepository(h.client, integrationDB)
	applied, err := pointsRepo.EarnPoints(context.Background(), service.EarnPointsInput{
		InviterID:     inviterID,
		SourceUserID:  inviteeID,
		SourceOrderID: orderID,
		Points:        points,
		FreezeHours:   0,
		PegAt:         0.01,
	})
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, points, pointsAvailableOf(t, inviterID))
}

// 退款成功（markRefundOk → PartiallyRefunded）→ 按 RefundAmount/Amount 比例撤回邀请人积分。
func TestPointsClawback_OnRefundSuccess_PartialRatio(t *testing.T) {
	ctx := context.Background()
	h := makePointsEarningHarness(t)
	setPointsEarnSettings(t, h.settingRepo, true, 0.01, 10, 0)

	inviter := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-claw-inviter-%s@example.com", uuid.NewString()),
		Username: "pts_claw_inviter",
	})
	invitee := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-claw-invitee-%s@example.com", uuid.NewString()),
		Username: "pts_claw_invitee",
	})
	group := mustCreateGroup(t, h.client, &service.Group{Name: "pts-claw-ok-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	sub := mustCreateSubscription(t, h.client, &service.UserSubscription{
		UserID:         invitee.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 6,
		TodayDay:       today,
		DailySpentDay:  today,
		StartDay:       today - 5,
		ExpireDay:      today + 12,
		ExpiresAt:      service.ExpireDayToExpiresAt(today + 12),
		Status:         service.SubscriptionStatusActive,
	})
	// 空 tradeNo → 网关退款成功路径。order.Amount=300。
	order := createCompletedSubscriptionRefundOrderForIntegration(t, h.client, invitee, group.ID, 300, 30, "")

	// seed：该来源单已为 inviter 返 1000 积分。
	seedEarnForOrder(t, h, inviter.ID, invitee.ID, order.ID, 1000)

	// 部分退款 120/300 → markRefundOk 置 PartiallyRefunded → clawback = floor(1000×120/300)=400。
	result, err := h.paymentSvc.ExecuteRefund(ctx, &service.RefundPlan{
		OrderID:                    order.ID,
		Order:                      order,
		RefundAmount:               120,
		GatewayAmount:              120,
		Reason:                     "subscription refund",
		DeductionType:              payment.DeductionTypeSubscription,
		SubscriptionID:             sub.ID,
		SubDaysToDeduct:            12,
		SubDaysToRestore:           13,
		SubExpireDayToRestore:      today + 12,
		SubTodayRemainingToRestore: 6,
		SubTodayDayToRestore:       today,
	})
	require.NoError(t, err)
	require.True(t, result.Success)

	reloaded, err := h.client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusPartiallyRefunded, reloaded.Status)

	want := service.ComputeClawbackPoints(1000, 120, 300) // floor(1000×0.4)=400
	require.Equal(t, int64(1000)-want, pointsAvailableOf(t, inviter.ID), "按 RefundAmount/Amount 比例撤回")
	require.Equal(t, 1, clawbackLedgerCountForOrder(t, order.ID), "一单一撤")
}

// 退款网关失败（handleGwFail 回滚，未到 markRefundOk）→ **不撤**积分。
func TestPointsClawback_NotTriggered_OnGatewayFailure(t *testing.T) {
	ctx := context.Background()
	h := makePointsEarningHarness(t)
	setPointsEarnSettings(t, h.settingRepo, true, 0.01, 10, 0)

	inviter := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-clawfail-inviter-%s@example.com", uuid.NewString()),
		Username: "pts_clawfail_inviter",
	})
	invitee := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-clawfail-invitee-%s@example.com", uuid.NewString()),
		Username: "pts_clawfail_invitee",
	})
	group := mustCreateGroup(t, h.client, &service.Group{Name: "pts-claw-fail-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	originalExpireDay := today + 15
	sub := mustCreateSubscription(t, h.client, &service.UserSubscription{
		UserID:         invitee.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 4.5,
		TodayDay:       today,
		DailySpentDay:  today,
		StartDay:       today - 3,
		ExpireDay:      originalExpireDay,
		ExpiresAt:      service.ExpireDayToExpiresAt(originalExpireDay),
		Status:         service.SubscriptionStatusActive,
	})
	// 非空 tradeNo → 网关退款失败路径（handleGwFail 回滚，订单复位 Completed）。
	order := createCompletedSubscriptionRefundOrderForIntegration(t, h.client, invitee, group.ID, 300, 30, "provider-trade-"+uuid.NewString())

	seedEarnForOrder(t, h, inviter.ID, invitee.ID, order.ID, 1000)

	result, err := h.paymentSvc.ExecuteRefund(ctx, &service.RefundPlan{
		OrderID:                    order.ID,
		Order:                      order,
		RefundAmount:               150,
		GatewayAmount:              150,
		Reason:                     "subscription refund",
		DeductionType:              payment.DeductionTypeSubscription,
		SubscriptionID:             sub.ID,
		SubDaysToDeduct:            15,
		SubDaysToRestore:           16,
		SubExpireDayToRestore:      originalExpireDay,
		SubTodayRemainingToRestore: 4.5,
		SubTodayDayToRestore:       today,
	})
	require.NoError(t, err)
	require.False(t, result.Success, "网关失败 → 退款未落单")

	reloaded, err := h.client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, reloaded.Status, "回滚后订单复位 Completed")

	require.Equal(t, int64(1000), pointsAvailableOf(t, inviter.ID), "网关失败不得撤回积分")
	require.Equal(t, 0, clawbackLedgerCountForOrder(t, order.ID), "未到 markRefundOk → 无 clawback 流水")
}
