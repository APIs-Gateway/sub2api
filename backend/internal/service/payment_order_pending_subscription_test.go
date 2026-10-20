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

// 单卡：hasPendingSubscriptionOrder 只数「pending 且类型=subscription」的单——并发/连点第二张
// pending 订阅单据此被拦，杜绝已付款无法开卡。余额单、已支付单、其它用户的单都不算。
func TestHasPendingSubscriptionOrder(t *testing.T) {
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client}

	user := mustCreatePendingTestUser(t, client, "pending-sub@example.com", "pending-sub-user")
	other := mustCreatePendingTestUser(t, client, "other@example.com", "other-user")

	// 初始无 pending → false。
	require.False(t, mustHasPending(t, svc, client, user.ID))

	// 其它用户的 pending 订阅单不算。
	createPendingTestOrder(t, client, other.ID, "p-other-sub", payment.OrderTypeSubscription, OrderStatusPending)
	require.False(t, mustHasPending(t, svc, client, user.ID))

	// 本用户的 pending 余额单不算（必须类型=subscription）。
	createPendingTestOrder(t, client, user.ID, "p-bal", payment.OrderTypeBalance, OrderStatusPending)
	require.False(t, mustHasPending(t, svc, client, user.ID))

	// 本用户已支付的订阅单不算（必须 status=pending）。
	createPendingTestOrder(t, client, user.ID, "p-paid-sub", payment.OrderTypeSubscription, OrderStatusPaid)
	require.False(t, mustHasPending(t, svc, client, user.ID))

	// 本用户的 pending 订阅单 → true。
	createPendingTestOrder(t, client, user.ID, "p-sub", payment.OrderTypeSubscription, OrderStatusPending)
	require.True(t, mustHasPending(t, svc, client, user.ID))
}

func mustHasPending(t *testing.T, svc *PaymentService, client *dbent.Client, userID int64) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	got, err := svc.hasPendingSubscriptionOrder(ctx, tx, userID)
	require.NoError(t, err)
	return got
}

func mustCreatePendingTestUser(t *testing.T, client *dbent.Client, email, username string) *dbent.User {
	t.Helper()
	u, err := client.User.Create().SetEmail(email).SetPasswordHash("hash").SetUsername(username).Save(context.Background())
	require.NoError(t, err)
	return u
}

func createPendingTestOrder(t *testing.T, client *dbent.Client, userID int64, outTradeNo, orderType, status string) {
	t.Helper()
	_, err := client.PaymentOrder.Create().
		SetUserID(userID).
		SetUserEmail("u@example.com").
		SetUserName("u").
		SetAmount(545).SetPayAmount(545).SetFeeRate(0).
		SetRechargeCode(outTradeNo).
		SetOutTradeNo(outTradeNo).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(orderType).
		SetStatus(status).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(context.Background())
	require.NoError(t, err)
}
