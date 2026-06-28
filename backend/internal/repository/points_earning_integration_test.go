//go:build integration

package repository

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）—— earning 钩子「服务层端到端」集成测试（真实 PG）。
//
// 补 spec §9 / §11.3② 的待办缺口：仓储层 EarnPoints 已直测，但「被邀请人买套餐 → 邀请人返积分」
// 这条 #1 头号一致性（"套餐单不再漏返"）此前**无任何经 fulfillment 钩子的端到端测试**。
// 本文件拼出含 PointsService 的真实 PaymentService，驱动 ExecuteSubscriptionFulfillment
// （doSub 新购 / doSubLifecycle 续费两条钩子），断言：
//   - 套餐单履约成功后邀请人按 floor(Amount×rate%/peg) 返积分（earn 流水 + 账户 + POINTS_EARNED 审计）；
//   - 重放回调按来源单幂等不重复返；
//   - 无邀请人 / 功能停用 → 不返。

// pointsEarningHarness 一套共享同一 entClient 的服务装配（PaymentService 已接 PointsService）。
type pointsEarningHarness struct {
	paymentSvc    *service.PaymentService
	pointsSvc     *service.PointsService
	settingRepo   service.SettingRepository
	affiliateRepo service.AffiliateRepository
	client        *dbent.Client
}

func makePointsEarningHarness(t *testing.T) *pointsEarningHarness {
	t.Helper()
	client := testEntClient(t)
	groupRepo := NewGroupRepository(client, integrationDB)
	settingRepo := NewSettingRepository(client)
	settingSvc := service.NewSettingService(settingRepo, nil)
	subscriptionSvc := service.NewSubscriptionService(
		groupRepo,
		NewUserSubscriptionRepository(client),
		nil,
		nil,
		nil,
		client,
		nil,
		nil,
	)
	affiliateRepo := NewAffiliateRepository(client, integrationDB)
	affiliateSvc := service.NewAffiliateService(affiliateRepo, settingSvc, nil, nil)
	pointsRepo := NewPointsRepository(client, integrationDB)
	pointsSvc := service.NewPointsService(pointsRepo, settingSvc, client, subscriptionSvc, groupRepo, affiliateSvc, nil, nil)
	paymentSvc := service.NewPaymentService(
		client,
		nil,
		nil,
		nil,
		subscriptionSvc,
		service.NewPaymentConfigService(client, settingRepo, nil),
		NewUserRepository(client, integrationDB),
		groupRepo,
		affiliateSvc,
	)
	paymentSvc.SetPointsService(pointsSvc)
	return &pointsEarningHarness{
		paymentSvc:    paymentSvc,
		pointsSvc:     pointsSvc,
		settingRepo:   settingRepo,
		affiliateRepo: affiliateRepo,
		client:        client,
	}
}

func setPointsEarnSettings(t *testing.T, settingRepo service.SettingRepository, enabled bool, peg, ratePercent float64, freezeHours int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, settingRepo.Set(ctx, service.SettingKeyPointsEnabled, strconv.FormatBool(enabled)))
	require.NoError(t, settingRepo.Set(ctx, service.SettingKeyPointsPeg, strconv.FormatFloat(peg, 'f', -1, 64)))
	require.NoError(t, settingRepo.Set(ctx, service.SettingKeyPointsCashbackRate, strconv.FormatFloat(ratePercent, 'f', -1, 64)))
	require.NoError(t, settingRepo.Set(ctx, service.SettingKeyPointsFreezeHours, strconv.Itoa(freezeHours)))
}

// bindInvitee 确保两个 affiliate 行存在，并把 invitee 的 inviter_id 绑到 inviter。
func bindInvitee(t *testing.T, h *pointsEarningHarness, inviteeID, inviterID int64) {
	t.Helper()
	ctx := context.Background()
	_, err := h.affiliateRepo.EnsureUserAffiliate(ctx, inviterID)
	require.NoError(t, err)
	_, err = h.affiliateRepo.EnsureUserAffiliate(ctx, inviteeID)
	require.NoError(t, err)
	bound, err := h.affiliateRepo.BindInviter(ctx, inviteeID, inviterID)
	require.NoError(t, err)
	require.True(t, bound, "inviter binding should take on a fresh invitee")
}

// pointsAvailableOf 读账户可用积分（无账户视为 0），单行 COALESCE 不抛 no-rows。
func pointsAvailableOf(t *testing.T, userID int64) int64 {
	t.Helper()
	var v int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COALESCE((SELECT available FROM user_points_accounts WHERE user_id=$1), 0)`, userID).Scan(&v))
	return v
}

// earnLedgerCountForOrder 该来源单的 earn 流水行数（幂等断言用）。
func earnLedgerCountForOrder(t *testing.T, orderID int64) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM user_points_ledger WHERE kind='earn' AND source_order_id=$1`, orderID).Scan(&n))
	return n
}

func orderHasAuditAction(t *testing.T, h *pointsEarningHarness, orderID int64, action string) bool {
	t.Helper()
	logs, err := h.paymentSvc.GetOrderAuditLogs(context.Background(), orderID)
	require.NoError(t, err)
	for _, l := range logs {
		if l.Action == action {
			return true
		}
	}
	return false
}

// 新购套餐单（doSub）→ 邀请人返积分；重放按来源单幂等不重复返。
// 这是 issue #11 头号一致性「套餐新购单不再漏返」的端到端守卫。
func TestPointsEarning_SubscriptionPurchase_EarnsForInviter_AndIdempotent(t *testing.T) {
	ctx := context.Background()
	h := makePointsEarningHarness(t)
	const peg, rate = 0.01, 10.0
	setPointsEarnSettings(t, h.settingRepo, true, peg, rate, 0)

	inviter := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-earn-inviter-%s@example.com", uuid.NewString()),
		Username: "pts_earn_inviter",
	})
	invitee := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-earn-invitee-%s@example.com", uuid.NewString()),
		Username: "pts_earn_invitee",
	})
	bindInvitee(t, h, invitee.ID, inviter.ID)

	group := mustCreateGroup(t, h.client, &service.Group{Name: "pts-earn-purchase-" + uuid.NewString()})
	// Amount 固定 545（见 createPaidSubscriptionOrderForIntegration）。
	orderID := createPaidSubscriptionOrderForIntegration(t, h.client, invitee, group.ID, 12, 45)
	require.NoError(t, h.paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	order, err := h.client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, order.Status, "套餐单应履约完成")

	// 被邀请人确实拿到了卡（履约真成功，钩子才在 markCompleted 前触发）。
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, invitee.ID, service.SubscriptionStatusActive))

	want := service.ComputeEarnPoints(order.Amount, rate, peg) // floor(545×10/100/0.01) = 5450
	require.Equal(t, want, pointsAvailableOf(t, inviter.ID), "邀请人按 floor(Amount×rate%%/peg) 返积分")
	require.Equal(t, 1, earnLedgerCountForOrder(t, orderID), "恰好一条 earn 流水")
	require.True(t, orderHasAuditAction(t, h, orderID, "POINTS_EARNED"), "应写 POINTS_EARNED 审计")

	// 重放：复位 PAID 再次履约 → SUBSCRIPTION_SUCCESS 审计拦截 + earn partial-unique 幂等，不得双重返。
	_, err = h.client.PaymentOrder.UpdateOneID(orderID).SetStatus(service.OrderStatusPaid).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, h.paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	require.Equal(t, want, pointsAvailableOf(t, inviter.ID), "重放不得二次返积分")
	require.Equal(t, 1, earnLedgerCountForOrder(t, orderID), "重放后仍只有一条 earn 流水")
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, invitee.ID, service.SubscriptionStatusActive))
}

// 续费套餐单（doSubLifecycle）→ 邀请人返积分（证明生命周期钩子也返，非仅新购）。
func TestPointsEarning_SubscriptionRenew_EarnsForInviter(t *testing.T) {
	ctx := context.Background()
	h := makePointsEarningHarness(t)
	const peg, rate = 0.01, 10.0
	setPointsEarnSettings(t, h.settingRepo, true, peg, rate, 0)

	inviter := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-renew-inviter-%s@example.com", uuid.NewString()),
		Username: "pts_renew_inviter",
	})
	invitee := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-renew-invitee-%s@example.com", uuid.NewString()),
		Username: "pts_renew_invitee",
	})
	bindInvitee(t, h, invitee.ID, inviter.ID)

	group := mustCreateGroup(t, h.client, &service.Group{Name: "pts-earn-renew-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	dDaily := 10.0
	wLimit, mLimit := service.DeriveWindowCaps(dDaily, 30)
	originalExpireDay := today + 5
	card := mustCreateSubscription(t, h.client, &service.UserSubscription{
		UserID:          invitee.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dDaily,
		DailyLimitUSD:   &dDaily,
		WeeklyLimitUSD:  &wLimit,
		MonthlyLimitUSD: &mLimit,
		TodayRemaining:  dDaily,
		TodayDay:        today,
		StartDay:        today - 5,
		ExpireDay:       originalExpireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(originalExpireDay),
		Status:          service.SubscriptionStatusActive,
	})

	const charge = 100.0
	orderID := createPaidLifecycleOrderForIntegration(t, h.client, invitee, service.SubscriptionIntentRenew, card.ID, dDaily, 30, charge)
	require.NoError(t, h.paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	order, err := h.client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, order.Status)

	want := service.ComputeEarnPoints(charge, rate, peg) // floor(100×10/100/0.01) = 1000
	require.Equal(t, want, pointsAvailableOf(t, inviter.ID), "续费单也应返积分（doSubLifecycle 钩子）")
	require.Equal(t, 1, earnLedgerCountForOrder(t, orderID))
}

// 无邀请人 → 不返（被邀请人没绑 inviter_id）。
func TestPointsEarning_NoInviter_NoEarn(t *testing.T) {
	ctx := context.Background()
	h := makePointsEarningHarness(t)
	setPointsEarnSettings(t, h.settingRepo, true, 0.01, 10, 0)

	buyer := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-noinviter-%s@example.com", uuid.NewString()),
		Username: "pts_noinviter",
	})
	// 故意不绑 inviter。
	group := mustCreateGroup(t, h.client, &service.Group{Name: "pts-noinviter-" + uuid.NewString()})
	orderID := createPaidSubscriptionOrderForIntegration(t, h.client, buyer, group.ID, 12, 45)
	require.NoError(t, h.paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	order, err := h.client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, order.Status, "无邀请人不影响履约")
	require.Equal(t, 0, earnLedgerCountForOrder(t, orderID), "无邀请人 → 无 earn 流水")
}

// 功能总开关关闭 → 不返（即便邀请关系已绑定）。
func TestPointsEarning_Disabled_NoEarn(t *testing.T) {
	ctx := context.Background()
	h := makePointsEarningHarness(t)
	setPointsEarnSettings(t, h.settingRepo, false, 0.01, 10, 0) // 总开关 OFF

	inviter := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-off-inviter-%s@example.com", uuid.NewString()),
		Username: "pts_off_inviter",
	})
	invitee := mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("pts-off-invitee-%s@example.com", uuid.NewString()),
		Username: "pts_off_invitee",
	})
	bindInvitee(t, h, invitee.ID, inviter.ID)

	group := mustCreateGroup(t, h.client, &service.Group{Name: "pts-off-" + uuid.NewString()})
	orderID := createPaidSubscriptionOrderForIntegration(t, h.client, invitee, group.ID, 12, 45)
	require.NoError(t, h.paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))

	order, err := h.client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, order.Status)
	require.Equal(t, 0, earnLedgerCountForOrder(t, orderID), "功能停用 → 无 earn 流水")
	require.Equal(t, int64(0), pointsAvailableOf(t, inviter.ID))
}
