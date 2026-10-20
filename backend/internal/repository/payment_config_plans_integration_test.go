//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPaymentConfigServiceListPlansForSale_FiltersZeroDailyAmountWithPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := service.NewPaymentConfigService(client, nil, nil)
	group := mustCreateGroup(t, client, &service.Group{Name: "plan-sale-filter-" + uuid.NewString()})

	zeroPlan, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("legacy-zero-d").
		SetDailyAmountUsd(0).
		SetPrice(10).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		SetSortOrder(1).
		Save(ctx)
	require.NoError(t, err)
	hiddenPlan, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("hidden-positive-d").
		SetDailyAmountUsd(5).
		SetPrice(200).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(false).
		SetSortOrder(2).
		Save(ctx)
	require.NoError(t, err)
	okPlan, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("visible-positive-d").
		SetDailyAmountUsd(10).
		SetPrice(545).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		SetSortOrder(3).
		Save(ctx)
	require.NoError(t, err)

	plans, err := svc.ListPlansForSale(ctx)
	require.NoError(t, err)
	ids := make(map[int64]bool, len(plans))
	for _, p := range plans {
		ids[p.ID] = true
		require.Greater(t, p.DailyAmountUsd, 0.0, "用户侧在售列表不得暴露 D<=0 套餐")
		require.True(t, p.ForSale)
	}
	require.False(t, ids[zeroPlan.ID], "迁移产生的 D=0 && for_sale=true 脏套餐应被过滤")
	require.False(t, ids[hiddenPlan.ID], "非在售套餐不应展示")
	require.True(t, ids[okPlan.ID], "D>0 且在售套餐应展示")
}

func TestPaymentConfigServiceUpdatePlan_RejectsForSaleFlipWithZeroDailyAmountPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := service.NewPaymentConfigService(client, nil, nil)
	group := mustCreateGroup(t, client, &service.Group{Name: "plan-sale-guard-" + uuid.NewString()})

	plan, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("legacy-zero-d-offsale").
		SetDailyAmountUsd(0).
		SetPrice(10).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(false).
		Save(ctx)
	require.NoError(t, err)

	on := true
	_, err = svc.UpdatePlan(ctx, plan.ID, service.UpdatePlanRequest{ForSale: &on})
	require.Error(t, err)
	require.Equal(t, "PLAN_DAILY_AMOUNT_INVALID", errors.Reason(err))

	d := 12.0
	updated, err := svc.UpdatePlan(ctx, plan.ID, service.UpdatePlanRequest{DailyAmountUSD: &d, ForSale: &on})
	require.NoError(t, err)
	require.True(t, updated.ForSale)
	require.InDelta(t, d, updated.DailyAmountUsd, 1e-9)
}
