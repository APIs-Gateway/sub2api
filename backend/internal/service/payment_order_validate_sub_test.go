//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// per-day 下单期 fail-fast：在售但 D<=0 的套餐（存量迁移155可能回填出 D=0）必须在收款【前】被拒，
// 否则下单冻结 D=0 快照 → 付款后履约判坏快照失败 → 已付款无法履约、无自动退款。
func TestValidateSubOrder_RejectsZeroDailyAmountPlan(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	cfg := &PaymentConfigService{entClient: client}
	groupRepo := &subscriptionGroupRepoStub{group: &Group{ID: 1, Status: payment.EntityStatusActive}}
	svc := &PaymentService{configService: cfg, groupRepo: groupRepo, subscriptionSvc: &SubscriptionService{}}

	// 经 ent 直接造一张 D=0 的在售套餐，绕过 CreatePlan 的 D>0 校验，模拟迁移155回填的存量脏数据。
	zeroPlan, err := client.SubscriptionPlan.Create().
		SetGroupID(1).SetName("legacy-zero").SetDailyAmountUsd(0).
		SetPrice(545).SetValidityDays(30).SetValidityUnit("day").SetForSale(true).
		Save(ctx)
	require.NoError(t, err)

	_, err = svc.validateSubOrder(ctx, CreateOrderRequest{PlanID: zeroPlan.ID})
	require.Error(t, err)
	require.Equal(t, "PLAN_DAILY_AMOUNT_INVALID", infraerrors.Reason(err))

	// 对照：D>0 的在售套餐应正常通过下单校验。
	okPlan, err := client.SubscriptionPlan.Create().
		SetGroupID(1).SetName("pro").SetDailyAmountUsd(10).
		SetPrice(545).SetValidityDays(30).SetValidityUnit("day").SetForSale(true).
		Save(ctx)
	require.NoError(t, err)
	got, err := svc.validateSubOrder(ctx, CreateOrderRequest{PlanID: okPlan.ID})
	require.NoError(t, err)
	require.NotNil(t, got.plan)
	require.Equal(t, okPlan.ID, got.plan.ID)
}
