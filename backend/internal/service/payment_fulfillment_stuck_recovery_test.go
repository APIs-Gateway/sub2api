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

// A webhook sender that hangs up while our DB is slow cancels the request
// context. Fulfillment must still finish instead of leaving the order PAID.
func TestHandlePaymentNotificationSurvivesCanceledCallerContext(t *testing.T) {
	client := newPaymentOrderLifecycleTestClient(t)
	fx := newPaidWxpayReconcileFixture(t, client, "wxpay-canceled-webhook")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fx.svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		TradeNo:  "wxpay-upstream-trade-123",
		OrderID:  fx.order.OutTradeNo,
		Amount:   50,
		Status:   payment.NotificationStatusSuccess,
		Metadata: map[string]string{"trade_state": "SUCCESS"},
	}, payment.TypeWxpay)
	require.NoError(t, err)

	reloaded, err := client.PaymentOrder.Get(context.Background(), fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, 50.0, fx.userRepo.getByIDUser.Balance)
	require.Len(t, fx.redeemRepo.useCalls, 1)
}

type stuckFulfillmentFixture struct {
	svc        *PaymentService
	client     *dbent.Client
	userRepo   *mockUserRepo
	redeemRepo *paymentOrderLifecycleRedeemRepo
	userID     int64
}

func newStuckFulfillmentFixture(t *testing.T, client *dbent.Client) *stuckFulfillmentFixture {
	t.Helper()
	ctx := context.Background()
	user, err := client.User.Create().
		SetEmail("stuck-fulfillment@example.com").
		SetPasswordHash("hash").
		SetUsername("stuck-fulfillment-user").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &mockUserRepo{getByIDUser: &User{ID: user.ID, Email: user.Email, Username: user.Username}}
	userRepo.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		userRepo.getByIDUser.Balance += amount
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{codesByCode: map[string]*RedeemCode{}}
	redeemService := NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil)
	svc := &PaymentService{
		entClient:       client,
		registry:        payment.NewRegistry(),
		redeemService:   redeemService,
		userRepo:        userRepo,
		providersLoaded: true,
	}
	return &stuckFulfillmentFixture{svc: svc, client: client, userRepo: userRepo, redeemRepo: redeemRepo, userID: user.ID}
}

// seedOrder creates a paid balance order whose last update was `age` ago,
// with a matching redeem code that is optionally already used.
func (fx *stuckFulfillmentFixture) seedOrder(t *testing.T, name, status string, age time.Duration, codeUsed bool) *dbent.PaymentOrder {
	t.Helper()
	at := time.Now().Add(-age)
	order, err := fx.client.PaymentOrder.Create().
		SetUserID(fx.userID).
		SetUserEmail("stuck-fulfillment@example.com").
		SetUserName("stuck-fulfillment-user").
		SetAmount(26).
		SetPayAmount(2).
		SetFeeRate(0).
		SetRechargeCode("PAY-" + name).
		SetOutTradeNo("sub2_" + name).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-" + name).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetPaidAt(at).
		SetUpdatedAt(at).
		SetExpiresAt(at.Add(30 * time.Minute)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(context.Background())
	require.NoError(t, err)

	code := &RedeemCode{
		ID:     int64(len(fx.redeemRepo.codesByCode) + 1),
		Code:   order.RechargeCode,
		Type:   RedeemTypeBalance,
		Value:  order.Amount,
		Status: StatusUnused,
	}
	if codeUsed {
		usedBy := fx.userID
		code.Status = StatusUsed
		code.UsedBy = &usedBy
	}
	fx.redeemRepo.codesByCode[code.Code] = code
	return order
}

func TestRecoverStuckFulfillmentsRetriesAbandonedOrders(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	fx := newStuckFulfillmentFixture(t, client)

	stuckPaid := fx.seedOrder(t, "stuck_paid", OrderStatusPaid, 10*time.Minute, false)
	// Balance already credited, only the COMPLETED transition was lost.
	stuckRecharging := fx.seedOrder(t, "stuck_recharging", OrderStatusRecharging, 10*time.Minute, true)
	freshPaid := fx.seedOrder(t, "fresh_paid", OrderStatusPaid, 10*time.Second, false)
	liveLease := fx.seedOrder(t, "live_lease", OrderStatusRecharging, time.Minute, false)

	recovered, err := fx.svc.RecoverStuckFulfillments(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, recovered)

	statusOf := func(o *dbent.PaymentOrder) string {
		reloaded, err := client.PaymentOrder.Get(ctx, o.ID)
		require.NoError(t, err)
		return reloaded.Status
	}
	require.Equal(t, OrderStatusCompleted, statusOf(stuckPaid))
	require.Equal(t, OrderStatusCompleted, statusOf(stuckRecharging))
	require.Equal(t, OrderStatusPaid, statusOf(freshPaid))
	require.Equal(t, OrderStatusRecharging, statusOf(liveLease))

	// Only the unused code was redeemed: no double credit for the order whose
	// balance had already landed.
	require.Len(t, fx.redeemRepo.useCalls, 1)
	require.Equal(t, 26.0, fx.userRepo.getByIDUser.Balance)
	require.True(t, fx.svc.hasAuditLog(ctx, stuckPaid.ID, "FULFILLMENT_RECOVERED"))
	require.True(t, fx.svc.hasAuditLog(ctx, stuckRecharging.ID, "FULFILLMENT_RECOVERED"))
}

func TestPaymentOrderExpiryRunOnceRecoversStuckFulfillment(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	fx := newStuckFulfillmentFixture(t, client)
	order := fx.seedOrder(t, "run_once_stuck", OrderStatusPaid, 10*time.Minute, false)

	NewPaymentOrderExpiryService(fx.svc, time.Minute).runOnce()

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, 26.0, fx.userRepo.getByIDUser.Balance)
}

func TestRecoverStuckFulfillmentsSkipsOrdersThatStillFail(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	fx := newStuckFulfillmentFixture(t, client)
	order := fx.seedOrder(t, "bad_code", OrderStatusPaid, 10*time.Minute, false)
	fx.redeemRepo.codesByCode[order.RechargeCode].Value = 999

	recovered, err := fx.svc.RecoverStuckFulfillments(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, reloaded.Status)
	require.Zero(t, fx.userRepo.getByIDUser.Balance)
}

func TestRecoverStuckFulfillmentsReturnsQueryError(t *testing.T) {
	client := newPaymentOrderLifecycleTestClient(t)
	svc := &PaymentService{entClient: client, registry: payment.NewRegistry(), providersLoaded: true}
	require.NoError(t, client.Close())

	recovered, err := svc.RecoverStuckFulfillments(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "query stuck payment fulfillments")
	require.Zero(t, recovered)
}
