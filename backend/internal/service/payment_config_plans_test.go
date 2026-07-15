//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestPaymentConfigPlanCRUD_PersistsDailyAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	plan, err := svc.CreatePlan(ctx, CreatePlanRequest{
		GroupID:        1,
		Name:           "Pro",
		DailyAmountUSD: 10,
		Price:          545,
		Currency:       " usd ",
		ValidityDays:   30,
		ValidityUnit:   "day",
		ForSale:        true,
	})
	require.NoError(t, err)
	require.InDelta(t, 10, plan.DailyAmountUsd, 1e-9)
	require.Equal(t, "USD", plan.Currency)

	updatedD := 25.0
	updatedCurrency := " nzd "
	updated, err := svc.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{DailyAmountUSD: &updatedD, Currency: &updatedCurrency})
	require.NoError(t, err)
	require.InDelta(t, updatedD, updated.DailyAmountUsd, 1e-9)
	require.Equal(t, "NZD", updated.Currency)
}

func TestPaymentConfigPlanCRUD_RejectsMissingDailyAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	_, err := svc.CreatePlan(ctx, CreatePlanRequest{
		GroupID:      1,
		Name:         "Pro",
		Price:        545,
		ValidityDays: 30,
		ValidityUnit: "day",
		ForSale:      true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "daily amount")
}

// update 显式传 D<=0 应被拒。
func TestPaymentConfigPlanCRUD_RejectsZeroDailyAmountUpdate(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	plan, err := svc.CreatePlan(ctx, CreatePlanRequest{
		GroupID: 1, Name: "Pro", DailyAmountUSD: 10, Price: 545, ValidityDays: 30, ValidityUnit: "day",
	})
	require.NoError(t, err)

	zero := 0.0
	_, err = svc.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{DailyAmountUSD: &zero})
	require.Error(t, err)
	require.Contains(t, err.Error(), "daily amount")
}

// patch 语义：update 不传 D（nil）时保持原值不动。
func TestPaymentConfigPlanCRUD_UpdateKeepsDailyAmountWhenNil(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	plan, err := svc.CreatePlan(ctx, CreatePlanRequest{
		GroupID: 1, Name: "Pro", DailyAmountUSD: 10, Price: 545, ValidityDays: 30, ValidityUnit: "day",
	})
	require.NoError(t, err)

	newName := "Pro-renamed"
	updated, err := svc.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{Name: &newName})
	require.NoError(t, err)
	require.Equal(t, "Pro-renamed", updated.Name)
	require.InDelta(t, 10, updated.DailyAmountUsd, 1e-9)
}

// 纵深防御：把存量 D=0 套餐仅靠 ForSale=true 翻牌（不提 D）必须被拒；同时提正 D + 上架则放行。
func TestPaymentConfigPlanCRUD_RejectsForSaleFlipWhenDailyAmountZero(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	// 经 ent 直接造 D=0 未上架套餐，模拟迁移155回填的存量脏数据。
	plan, err := client.SubscriptionPlan.Create().
		SetGroupID(1).SetName("legacy-zero").SetDailyAmountUsd(0).
		SetPrice(545).SetValidityDays(30).SetValidityUnit("day").SetForSale(false).
		Save(ctx)
	require.NoError(t, err)

	on := true
	_, err = svc.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{ForSale: &on})
	require.Error(t, err)
	require.Equal(t, "PLAN_DAILY_AMOUNT_INVALID", infraerrors.Reason(err))

	d := 12.0
	updated, err := svc.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{ForSale: &on, DailyAmountUSD: &d})
	require.NoError(t, err)
	require.True(t, updated.ForSale)
	require.InDelta(t, 12, updated.DailyAmountUsd, 1e-9)
}
