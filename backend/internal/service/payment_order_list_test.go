//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestPaymentServiceListOrdersStatusInFilters(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userOne := createOrderListUser(t, ctx, client, "order-list-one@example.com")
	userTwo := createOrderListUser(t, ctx, client, "order-list-two@example.com")

	createOrderListPaymentOrder(t, ctx, client, userOne, OrderStatusCompleted, "completed")
	createOrderListPaymentOrder(t, ctx, client, userOne, OrderStatusRefundRequested, "refund-requested")
	createOrderListPaymentOrder(t, ctx, client, userOne, OrderStatusRefunded, "refunded")
	createOrderListPaymentOrder(t, ctx, client, userTwo, OrderStatusRefunded, "other-user-refunded")

	svc := &PaymentService{entClient: client}

	adminOrders, adminTotal, err := svc.AdminListOrders(ctx, 0, OrderListParams{
		Status:   OrderStatusCompleted,
		Statuses: []string{OrderStatusRefundRequested, OrderStatusRefunded},
		Page:     1,
		PageSize: 20,
	})
	require.NoError(t, err)
	require.Equal(t, 3, adminTotal)
	require.ElementsMatch(t, []string{OrderStatusRefundRequested, OrderStatusRefunded, OrderStatusRefunded}, orderStatuses(adminOrders))

	userOrders, userTotal, err := svc.GetUserOrders(ctx, userOne.ID, OrderListParams{
		Status:   OrderStatusCompleted,
		Statuses: []string{OrderStatusRefundRequested, OrderStatusRefunded},
		Page:     1,
		PageSize: 20,
	})
	require.NoError(t, err)
	require.Equal(t, 2, userTotal)
	require.ElementsMatch(t, []string{OrderStatusRefundRequested, OrderStatusRefunded}, orderStatuses(userOrders))

	completedOrders, completedTotal, err := svc.AdminListOrders(ctx, 0, OrderListParams{
		Status:   OrderStatusCompleted,
		Page:     1,
		PageSize: 20,
	})
	require.NoError(t, err)
	require.Equal(t, 1, completedTotal)
	require.Equal(t, []string{OrderStatusCompleted}, orderStatuses(completedOrders))
}

func createOrderListUser(t *testing.T, ctx context.Context, client *dbent.Client, email string) *dbent.User {
	t.Helper()
	user, err := client.User.Create().
		SetEmail(email).
		SetPasswordHash("hash").
		SetUsername(email).
		Save(ctx)
	require.NoError(t, err)
	return user
}

func createOrderListPaymentOrder(t *testing.T, ctx context.Context, client *dbent.Client, user *dbent.User, status string, suffix string) *dbent.PaymentOrder {
	t.Helper()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode(fmt.Sprintf("ORDER-LIST-%s", suffix)).
		SetOutTradeNo(fmt.Sprintf("sub2_order_list_%s", suffix)).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(fmt.Sprintf("trade-%s", suffix)).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order
}

func orderStatuses(orders []*dbent.PaymentOrder) []string {
	statuses := make([]string, 0, len(orders))
	for _, order := range orders {
		statuses = append(statuses, order.Status)
	}
	return statuses
}
