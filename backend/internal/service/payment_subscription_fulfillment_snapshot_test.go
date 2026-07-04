//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestExecuteSubscriptionFulfillment_UsesFrozenSnapshotWhenGroupInactive(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	orderID := createPaidSubscriptionOrderForSnapshotTest(t, client, map[string]any{
		subscriptionSnapshotKey: map[string]any{
			"daily_amount_usd": 12.0,
			"validity_days":    45.0,
		},
	})

	groupRepo := &subscriptionGroupRepoStub{group: &Group{ID: 1, Status: StatusDisabled}}
	subRepo := newSubscriptionUserSubRepoStub()
	subSvc := NewSubscriptionService(groupRepo, subRepo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, groupRepo: groupRepo, subscriptionSvc: subSvc}

	require.NoError(t, svc.ExecuteSubscriptionFulfillment(ctx, orderID))

	require.Equal(t, 1, subRepo.createCalls)
	var sub *UserSubscription
	for _, created := range subRepo.byID {
		sub = created
		break
	}
	require.NotNil(t, sub)
	require.InDelta(t, 12, sub.DailyAmountUSD, 1e-9)
	require.InDelta(t, 12, sub.TodayRemaining, 1e-9)
	require.InDelta(t, 12*45, sub.GrantedTotalUSD, 1e-9)
	require.Equal(t, sub.StartDay+44, sub.ExpireDay)

	order, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, order.Status)
	subID, ok := readSubscriptionSnapshotSubscriptionID(order)
	require.True(t, ok)
	require.Equal(t, sub.ID, subID)
}

func TestExecuteSubscriptionFulfillment_InvalidSnapshotDoesNotFallbackToGroup(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	orderID := createPaidSubscriptionOrderForSnapshotTest(t, client, map[string]any{
		subscriptionSnapshotKey: map[string]any{
			"daily_amount_usd": 0.0,
			"validity_days":    30.0,
		},
	})
	fallbackD := 99.0
	groupRepo := &subscriptionGroupRepoStub{group: &Group{ID: 1, Status: StatusActive, DailyLimitUSD: &fallbackD}}
	subRepo := newSubscriptionUserSubRepoStub()
	subSvc := NewSubscriptionService(groupRepo, subRepo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, groupRepo: groupRepo, subscriptionSvc: subSvc}

	err := svc.ExecuteSubscriptionFulfillment(ctx, orderID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid subscription snapshot")
	require.Equal(t, 0, subRepo.createCalls, "坏快照不能按当前 group.daily_limit_usd 回退发卡")

	order, getErr := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, getErr)
	require.Equal(t, OrderStatusFailed, order.Status)
}

func createPaidSubscriptionOrderForSnapshotTest(t *testing.T, client *dbent.Client, snapshot map[string]any) int64 {
	t.Helper()
	ctx := context.Background()

	user, err := client.User.Create().
		SetEmail("snapshot-order@example.com").
		SetPasswordHash("hash").
		SetUsername("snapshot-order-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(545).
		SetPayAmount(545).
		SetFeeRate(0).
		SetRechargeCode("SNAPSHOT-ORDER").
		SetOutTradeNo("sub2_snapshot_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("provider-trade-no").
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(1).
		SetSubscriptionGroupID(1).
		SetSubscriptionDays(30).
		SetProviderSnapshot(snapshot).
		SetStatus(OrderStatusPaid).
		SetPaidAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order.ID
}
