//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const balanceFulfillmentLeaseWindow = 5 * time.Minute

type balanceFulfillmentHarness struct {
	client     *dbent.Client
	paymentSvc *service.PaymentService
	redeemSvc  *service.RedeemService
	redeemRepo service.RedeemCodeRepository

	fixtureMu   sync.Mutex
	orderIDs    []int64
	userIDs     []int64
	redeemCodes []string
}

func newBalanceFulfillmentHarness(t *testing.T, cache service.RedeemCache) *balanceFulfillmentHarness {
	return newBalanceFulfillmentHarnessWithRedeemRepo(t, cache, nil)
}

func newBalanceFulfillmentHarnessWithRedeemRepo(
	t *testing.T,
	cache service.RedeemCache,
	decorate func(service.RedeemCodeRepository) service.RedeemCodeRepository,
) *balanceFulfillmentHarness {
	t.Helper()
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := service.RedeemCodeRepository(NewRedeemCodeRepository(client))
	if decorate != nil {
		redeemRepo = decorate(redeemRepo)
	}
	redeemSvc := service.NewRedeemService(redeemRepo, userRepo, nil, cache, nil, client, nil, nil)
	paymentSvc := service.NewPaymentService(
		client,
		nil,
		nil,
		redeemSvc,
		nil,
		service.NewPaymentConfigService(client, nil, nil),
		userRepo,
		nil,
		nil,
	)
	h := &balanceFulfillmentHarness{
		client:     client,
		paymentSvc: paymentSvc,
		redeemSvc:  redeemSvc,
		redeemRepo: redeemRepo,
	}
	t.Cleanup(func() {
		h.cleanupFixtures(t)
	})
	return h
}

func (h *balanceFulfillmentHarness) trackFixture(orderID, userID int64, redeemCode string) {
	h.fixtureMu.Lock()
	defer h.fixtureMu.Unlock()
	h.orderIDs = append(h.orderIDs, orderID)
	h.userIDs = append(h.userIDs, userID)
	h.redeemCodes = append(h.redeemCodes, redeemCode)
}

func (h *balanceFulfillmentHarness) cleanupFixtures(t *testing.T) {
	t.Helper()
	h.fixtureMu.Lock()
	orderIDs := append([]int64(nil), h.orderIDs...)
	userIDs := append([]int64(nil), h.userIDs...)
	redeemCodes := append([]string(nil), h.redeemCodes...)
	h.fixtureMu.Unlock()
	if len(orderIDs) == 0 {
		return
	}

	ctx := context.Background()
	auditOrderIDs := make([]string, 0, len(orderIDs))
	for _, orderID := range orderIDs {
		auditOrderIDs = append(auditOrderIDs, fmt.Sprint(orderID))
	}
	_, err := h.client.PaymentAuditLog.Delete().
		Where(paymentauditlog.OrderIDIn(auditOrderIDs...)).
		Exec(ctx)
	require.NoError(t, err, "delete balance fulfillment audit fixtures")
	_, err = h.client.PaymentOrder.Delete().
		Where(paymentorder.IDIn(orderIDs...)).
		Exec(ctx)
	require.NoError(t, err, "delete balance fulfillment order fixtures")
	_, err = h.client.RedeemCode.Delete().
		Where(redeemcode.CodeIn(redeemCodes...)).
		Exec(ctx)
	require.NoError(t, err, "delete balance fulfillment redeem code fixtures")
	_, err = h.client.User.Delete().
		Where(dbuser.IDIn(userIDs...)).
		Exec(ctx)
	require.NoError(t, err, "delete balance fulfillment user fixtures")
}

func createBalanceFulfillmentOrder(
	t *testing.T,
	h *balanceFulfillmentHarness,
	user *service.User,
	updatedAt time.Time,
) *dbent.PaymentOrder {
	t.Helper()
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	order, err := h.client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(80).
		SetPayAmount(80).
		SetFeeRate(0).
		SetRechargeCode("bl-" + suffix).
		SetOutTradeNo("sub2_balance_lease_" + suffix).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-" + suffix).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusRecharging).
		SetPaidAt(time.Now().Add(-time.Hour)).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetUpdatedAt(updatedAt).
		Save(context.Background())
	require.NoError(t, err)
	h.trackFixture(order.ID, user.ID, order.RechargeCode)
	return order
}

func createBalanceFulfillmentUser(t *testing.T, h *balanceFulfillmentHarness) *service.User {
	t.Helper()
	return mustCreateUser(t, h.client, &service.User{
		Email:    fmt.Sprintf("balance-lease-%s@example.com", uuid.NewString()),
		Username: "balance-lease",
		Balance:  0,
	})
}

func staleBalanceFulfillmentTime() time.Time {
	return time.Now().UTC().Add(-balanceFulfillmentLeaseWindow - time.Minute)
}

func balanceForUser(t *testing.T, client *dbent.Client, userID int64) float64 {
	t.Helper()
	user, err := client.User.Get(context.Background(), userID)
	require.NoError(t, err)
	return user.Balance
}

func rechargeSuccessAuditCount(t *testing.T, client *dbent.Client, orderID int64) int {
	return paymentAuditActionCount(t, client, orderID, "RECHARGE_SUCCESS")
}

func paymentAuditActionCount(t *testing.T, client *dbent.Client, orderID int64, action string) int {
	t.Helper()
	count, err := client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(fmt.Sprint(orderID)),
			paymentauditlog.ActionEQ(action),
		).
		Count(context.Background())
	require.NoError(t, err)
	return count
}

func requireBalanceFulfillmentCompleted(t *testing.T, h *balanceFulfillmentHarness, order *dbent.PaymentOrder, wantBalance float64) {
	t.Helper()
	reloaded, err := h.client.PaymentOrder.Get(context.Background(), order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, reloaded.Status)
	require.Equal(t, wantBalance, balanceForUser(t, h.client, order.UserID))
	require.Equal(t, 1, rechargeSuccessAuditCount(t, h.client, order.ID))

	code, err := h.redeemRepo.GetByCode(context.Background(), order.RechargeCode)
	require.NoError(t, err)
	require.Equal(t, service.StatusUsed, code.Status)
}

func redeemBalanceOrderCode(t *testing.T, h *balanceFulfillmentHarness, order *dbent.PaymentOrder) {
	t.Helper()
	code := &service.RedeemCode{
		Code:   order.RechargeCode,
		Type:   service.RedeemTypeBalance,
		Value:  order.Amount,
		Status: service.StatusUnused,
	}
	require.NoError(t, h.redeemRepo.Create(context.Background(), code))
	_, err := h.redeemSvc.Redeem(context.Background(), order.UserID, order.RechargeCode)
	require.NoError(t, err)
}

func TestBalanceFulfillment_StaleUsedCodeCompletesWithoutDuplicateCreditPostgres(t *testing.T) {
	ctx := context.Background()
	h := newBalanceFulfillmentHarness(t, nil)
	user := createBalanceFulfillmentUser(t, h)
	order := createBalanceFulfillmentOrder(t, h, user, staleBalanceFulfillmentTime())

	redeemBalanceOrderCode(t, h, order)
	require.Equal(t, order.Amount, balanceForUser(t, h.client, user.ID))

	require.NoError(t, h.paymentSvc.ExecuteBalanceFulfillment(ctx, order.ID))
	requireBalanceFulfillmentCompleted(t, h, order, order.Amount)
}

func TestBalanceFulfillment_FreshRechargingRejectsRecoveryEntriesPostgres(t *testing.T) {
	ctx := context.Background()
	h := newBalanceFulfillmentHarness(t, nil)
	user := createBalanceFulfillmentUser(t, h)
	order := createBalanceFulfillmentOrder(t, h, user, time.Now().UTC())

	err := h.paymentSvc.ExecuteBalanceFulfillment(ctx, order.ID)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	err = h.paymentSvc.RetryFulfillment(ctx, order.ID)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))

	reloaded, err := h.client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRecharging, reloaded.Status)
	require.Zero(t, balanceForUser(t, h.client, user.ID))
	require.Zero(t, rechargeSuccessAuditCount(t, h.client, order.ID))
}

func TestBalanceFulfillment_ConcurrentStaleRecoveryCreditsExactlyOncePostgres(t *testing.T) {
	ctx := context.Background()
	h := newBalanceFulfillmentHarness(t, nil)
	user := createBalanceFulfillmentUser(t, h)
	order := createBalanceFulfillmentOrder(t, h, user, staleBalanceFulfillmentTime())

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.paymentSvc.ExecuteBalanceFulfillment(ctx, order.ID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil {
			continue
		}
		require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	}

	requireBalanceFulfillmentCompleted(t, h, order, order.Amount)
}

func TestBalanceFulfillment_WebhookAndRetryRecoverStaleOrdersPostgres(t *testing.T) {
	tests := []struct {
		name    string
		recover func(context.Context, *balanceFulfillmentHarness, *dbent.PaymentOrder) error
	}{
		{
			name: "webhook",
			recover: func(ctx context.Context, h *balanceFulfillmentHarness, order *dbent.PaymentOrder) error {
				return h.paymentSvc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
					TradeNo:  order.PaymentTradeNo,
					OrderID:  order.OutTradeNo,
					Amount:   order.PayAmount,
					Status:   payment.NotificationStatusSuccess,
					Metadata: map[string]string{},
				}, payment.TypeAlipay)
			},
		},
		{
			name: "manual retry",
			recover: func(ctx context.Context, h *balanceFulfillmentHarness, order *dbent.PaymentOrder) error {
				return h.paymentSvc.RetryFulfillment(ctx, order.ID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBalanceFulfillmentHarness(t, nil)
			user := createBalanceFulfillmentUser(t, h)
			order := createBalanceFulfillmentOrder(t, h, user, staleBalanceFulfillmentTime())

			require.NoError(t, tt.recover(context.Background(), h, order))
			requireBalanceFulfillmentCompleted(t, h, order, order.Amount)
		})
	}
}

type blockFirstRedeemLockCache struct {
	first   sync.Once
	started chan struct{}
	release <-chan struct{}
}

func (c *blockFirstRedeemLockCache) GetRedeemAttemptCount(context.Context, int64) (int, error) {
	return 0, nil
}

func (c *blockFirstRedeemLockCache) IncrementRedeemAttemptCount(context.Context, int64) error {
	return nil
}

func (c *blockFirstRedeemLockCache) AcquireRedeemLock(context.Context, string, time.Duration) (bool, error) {
	block := false
	c.first.Do(func() {
		block = true
		close(c.started)
	})
	if block {
		<-c.release
	}
	return true, nil
}

func (c *blockFirstRedeemLockCache) ReleaseRedeemLock(context.Context, string) error {
	return nil
}

func TestBalanceFulfillment_StaleWorkerCannotOverwriteNewLeasePostgres(t *testing.T) {
	ctx := context.Background()
	releaseFirstRedeem := make(chan struct{})
	cache := &blockFirstRedeemLockCache{
		started: make(chan struct{}),
		release: releaseFirstRedeem,
	}
	h := newBalanceFulfillmentHarness(t, cache)
	user := createBalanceFulfillmentUser(t, h)
	order := createBalanceFulfillmentOrder(t, h, user, staleBalanceFulfillmentTime())

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- h.paymentSvc.ExecuteBalanceFulfillment(ctx, order.ID)
	}()

	select {
	case <-cache.started:
	case <-time.After(10 * time.Second):
		t.Fatal("first worker did not reach redeem lock")
	}

	_, err := h.client.PaymentOrder.UpdateOneID(order.ID).
		SetUpdatedAt(staleBalanceFulfillmentTime()).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, h.paymentSvc.ExecuteBalanceFulfillment(ctx, order.ID))
	close(releaseFirstRedeem)
	require.Error(t, <-firstResult)

	requireBalanceFulfillmentCompleted(t, h, order, order.Amount)
}

type blockFirstTwoRedeemCodeCreates struct {
	service.RedeemCodeRepository
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  <-chan struct{}
	releaseSecond <-chan struct{}
	mu            sync.Mutex
	createCalls   int
}

func (r *blockFirstTwoRedeemCodeCreates) Create(ctx context.Context, code *service.RedeemCode) error {
	r.mu.Lock()
	r.createCalls++
	call := r.createCalls
	r.mu.Unlock()

	switch call {
	case 1:
		close(r.firstStarted)
		<-r.releaseFirst
	case 2:
		close(r.secondStarted)
		<-r.releaseSecond
	}
	return r.RedeemCodeRepository.Create(ctx, code)
}

func waitForBalanceFulfillmentSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal(message)
	}
}

func TestBalanceFulfillment_LateCreateConflictDoesNotFailNewLeasePostgres(t *testing.T) {
	ctx := context.Background()
	releaseFirstCreate := make(chan struct{})
	releaseSecondCreate := make(chan struct{})
	releaseFirstRedeem := make(chan struct{})
	createGate := &blockFirstTwoRedeemCodeCreates{
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseFirst:  releaseFirstCreate,
		releaseSecond: releaseSecondCreate,
	}
	cache := &blockFirstRedeemLockCache{
		started: make(chan struct{}),
		release: releaseFirstRedeem,
	}
	h := newBalanceFulfillmentHarnessWithRedeemRepo(t, cache, func(repo service.RedeemCodeRepository) service.RedeemCodeRepository {
		createGate.RedeemCodeRepository = repo
		return createGate
	})
	user := createBalanceFulfillmentUser(t, h)
	order := createBalanceFulfillmentOrder(t, h, user, staleBalanceFulfillmentTime())

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- h.paymentSvc.ExecuteBalanceFulfillment(ctx, order.ID)
	}()
	waitForBalanceFulfillmentSignal(t, createGate.firstStarted, "first worker did not reach redeem code creation")

	_, err := h.client.PaymentOrder.UpdateOneID(order.ID).
		SetUpdatedAt(staleBalanceFulfillmentTime()).
		Save(ctx)
	require.NoError(t, err)

	secondResult := make(chan error, 1)
	go func() {
		secondResult <- h.paymentSvc.ExecuteBalanceFulfillment(ctx, order.ID)
	}()
	waitForBalanceFulfillmentSignal(t, createGate.secondStarted, "second worker did not reach redeem code creation")

	close(releaseFirstCreate)
	waitForBalanceFulfillmentSignal(t, cache.started, "first worker did not reach redeem lock")
	close(releaseSecondCreate)
	require.NoError(t, <-secondResult)

	close(releaseFirstRedeem)
	require.Error(t, <-firstResult)

	requireBalanceFulfillmentCompleted(t, h, order, order.Amount)
	require.Zero(t, paymentAuditActionCount(t, h.client, order.ID, "FULFILLMENT_FAILED"))
}
