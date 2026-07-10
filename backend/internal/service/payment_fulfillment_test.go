//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type paymentFulfillmentTestProvider struct {
	key            string
	supportedTypes []payment.PaymentType
}

type paymentFulfillmentRedeemRepo struct {
	RedeemCodeRepository
	code            *RedeemCode
	createErr       error
	persistOnCreate bool
	createCalls     int
}

func (r *paymentFulfillmentRedeemRepo) Create(_ context.Context, code *RedeemCode) error {
	r.createCalls++
	if r.persistOnCreate {
		cloned := *code
		r.code = &cloned
	}
	return r.createErr
}

func (r *paymentFulfillmentRedeemRepo) GetByCode(_ context.Context, code string) (*RedeemCode, error) {
	if r.code == nil || r.code.Code != code {
		return nil, ErrRedeemCodeNotFound
	}
	cloned := *r.code
	return &cloned, nil
}

func (p paymentFulfillmentTestProvider) Name() string        { return p.key }
func (p paymentFulfillmentTestProvider) ProviderKey() string { return p.key }
func (p paymentFulfillmentTestProvider) SupportedTypes() []payment.PaymentType {
	return p.supportedTypes
}
func (p paymentFulfillmentTestProvider) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected call")
}
func (p paymentFulfillmentTestProvider) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	panic("unexpected call")
}
func (p paymentFulfillmentTestProvider) VerifyNotification(ctx context.Context, rawBody string, headers map[string]string) (*payment.PaymentNotification, error) {
	panic("unexpected call")
}
func (p paymentFulfillmentTestProvider) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	panic("unexpected call")
}

// ---------------------------------------------------------------------------
// resolveRedeemAction — pure idempotency decision logic
// ---------------------------------------------------------------------------

func TestResolveRedeemAction_CodeNotFound(t *testing.T) {
	t.Parallel()
	action := resolveRedeemAction(nil, nil)
	assert.Equal(t, redeemActionCreate, action, "nil code with nil error should create")
}

func TestPointsEarnBaseAmountForOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		o    *dbent.PaymentOrder
		want float64
		ok   bool
	}{
		{
			name: "subscription uses actual paid amount before credited value",
			o:    &dbent.PaymentOrder{OrderType: payment.OrderTypeSubscription, Amount: 900, PayAmount: 45},
			want: 45,
			ok:   true,
		},
		{
			name: "balance also uses actual paid amount when present",
			o:    &dbent.PaymentOrder{OrderType: payment.OrderTypeBalance, Amount: 90, PayAmount: 50},
			want: 50,
			ok:   true,
		},
		{
			name: "legacy order falls back to amount",
			o:    &dbent.PaymentOrder{OrderType: payment.OrderTypeSubscription, Amount: 30},
			want: 30,
			ok:   true,
		},
		{
			name: "order type does not change paid amount base",
			o:    &dbent.PaymentOrder{OrderType: "other", Amount: 30, PayAmount: 30},
			want: 30,
			ok:   true,
		},
		{
			name: "supported type without any positive amount skipped",
			o:    &dbent.PaymentOrder{OrderType: payment.OrderTypeSubscription},
			ok:   false,
		},
		{
			name: "nil skipped",
			o:    nil,
			ok:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := pointsEarnBaseAmountForOrder(tc.o)
			assert.Equal(t, tc.ok, ok)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

func TestResolveRedeemAction_LookupError(t *testing.T) {
	t.Parallel()
	action := resolveRedeemAction(nil, errors.New("db connection lost"))
	assert.Equal(t, redeemActionCreate, action, "lookup error should fall back to create")
}

func TestResolveRedeemAction_LookupErrorWithNonNilCode(t *testing.T) {
	t.Parallel()
	// Edge case: both code and error are non-nil (shouldn't happen in practice,
	// but the function should still treat error as authoritative)
	code := &RedeemCode{Status: StatusUnused}
	action := resolveRedeemAction(code, errors.New("partial error"))
	assert.Equal(t, redeemActionCreate, action, "non-nil error should always result in create regardless of code")
}

func TestResolveRedeemAction_CodeExistsAndUsed(t *testing.T) {
	t.Parallel()
	code := &RedeemCode{
		Code:   "test-code-123",
		Status: StatusUsed,
		Type:   RedeemTypeBalance,
		Value:  10.0,
	}
	action := resolveRedeemAction(code, nil)
	assert.Equal(t, redeemActionSkipCompleted, action, "used code should skip to completed")
}

func TestResolveRedeemAction_CodeExistsAndUnused(t *testing.T) {
	t.Parallel()
	code := &RedeemCode{
		Code:   "test-code-456",
		Status: StatusUnused,
		Type:   RedeemTypeBalance,
		Value:  25.0,
	}
	action := resolveRedeemAction(code, nil)
	assert.Equal(t, redeemActionRedeem, action, "unused code should skip creation and proceed to redeem")
}

func TestResolveRedeemAction_CodeExistsWithExpiredStatus(t *testing.T) {
	t.Parallel()
	// A code with a non-standard status (neither "unused" nor "used")
	// should NOT be treated as used, so it falls through to redeemActionRedeem.
	code := &RedeemCode{
		Code:   "expired-code",
		Status: StatusExpired,
	}
	action := resolveRedeemAction(code, nil)
	assert.Equal(t, redeemActionRedeem, action, "expired-status code is not IsUsed(), should redeem")
}

func TestGetOrCreateBalanceRedeemCodeRecoversConcurrentCreate(t *testing.T) {
	ctx := context.Background()
	repo := &paymentFulfillmentRedeemRepo{
		createErr:       errors.New("unique constraint"),
		persistOnCreate: true,
	}
	svc := &PaymentService{redeemService: &RedeemService{redeemRepo: repo}}
	order := &dbent.PaymentOrder{RechargeCode: "balance-concurrent-code", Amount: 80}

	code, err := svc.getOrCreateBalanceRedeemCode(ctx, order)
	require.NoError(t, err)
	require.Equal(t, order.RechargeCode, code.Code)
	require.Equal(t, RedeemTypeBalance, code.Type)
	require.Equal(t, order.Amount, code.Value)
	require.Equal(t, StatusUnused, code.Status)
}

func TestGetOrCreateBalanceRedeemCodeUsesExistingCode(t *testing.T) {
	ctx := context.Background()
	order := &dbent.PaymentOrder{RechargeCode: "balance-existing-code", Amount: 80}

	for _, status := range []string{StatusUnused, StatusUsed} {
		t.Run(status, func(t *testing.T) {
			repo := &paymentFulfillmentRedeemRepo{
				code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeBalance, Value: order.Amount, Status: status},
			}
			svc := &PaymentService{redeemService: &RedeemService{redeemRepo: repo}}

			code, err := svc.getOrCreateBalanceRedeemCode(ctx, order)
			require.NoError(t, err)
			require.Equal(t, status, code.Status)
			require.Zero(t, repo.createCalls)
		})
	}
}

func TestGetOrCreateBalanceRedeemCodeCreatesMissingCode(t *testing.T) {
	ctx := context.Background()
	repo := &paymentFulfillmentRedeemRepo{persistOnCreate: true}
	svc := &PaymentService{redeemService: &RedeemService{redeemRepo: repo}}
	order := &dbent.PaymentOrder{RechargeCode: "balance-new-code", Amount: 80}

	code, err := svc.getOrCreateBalanceRedeemCode(ctx, order)
	require.NoError(t, err)
	require.Equal(t, order.RechargeCode, code.Code)
	require.Equal(t, RedeemTypeBalance, code.Type)
	require.Equal(t, order.Amount, code.Value)
	require.Equal(t, StatusUnused, code.Status)
	require.Equal(t, 1, repo.createCalls)
}

func TestGetOrCreateBalanceRedeemCodeReturnsCreateErrorWhenCodeStaysMissing(t *testing.T) {
	ctx := context.Background()
	repo := &paymentFulfillmentRedeemRepo{createErr: errors.New("database unavailable")}
	svc := &PaymentService{redeemService: &RedeemService{redeemRepo: repo}}

	_, err := svc.getOrCreateBalanceRedeemCode(ctx, &dbent.PaymentOrder{RechargeCode: "balance-create-error", Amount: 80})
	require.ErrorContains(t, err, "database unavailable")
	require.Equal(t, 1, repo.createCalls)
}

// ---------------------------------------------------------------------------
// Table-driven comprehensive test
// ---------------------------------------------------------------------------

func TestResolveRedeemAction_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		code     *RedeemCode
		err      error
		expected redeemAction
	}{
		{
			name:     "nil code, nil error — first run",
			code:     nil,
			err:      nil,
			expected: redeemActionCreate,
		},
		{
			name:     "nil code, lookup error — treat as not found",
			code:     nil,
			err:      ErrRedeemCodeNotFound,
			expected: redeemActionCreate,
		},
		{
			name:     "nil code, generic DB error — treat as not found",
			code:     nil,
			err:      errors.New("connection refused"),
			expected: redeemActionCreate,
		},
		{
			name:     "code exists, used — previous run completed redeem",
			code:     &RedeemCode{Status: StatusUsed},
			err:      nil,
			expected: redeemActionSkipCompleted,
		},
		{
			name:     "code exists, unused — previous run created code but crashed before redeem",
			code:     &RedeemCode{Status: StatusUnused},
			err:      nil,
			expected: redeemActionRedeem,
		},
		{
			name:     "code exists but error also set — error takes precedence",
			code:     &RedeemCode{Status: StatusUsed},
			err:      errors.New("unexpected"),
			expected: redeemActionCreate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveRedeemAction(tt.code, tt.err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// ---------------------------------------------------------------------------
// redeemAction enum value sanity
// ---------------------------------------------------------------------------

func TestRedeemAction_DistinctValues(t *testing.T) {
	t.Parallel()
	// Ensure the three actions have distinct values (iota correctness)
	assert.NotEqual(t, redeemActionCreate, redeemActionRedeem)
	assert.NotEqual(t, redeemActionCreate, redeemActionSkipCompleted)
	assert.NotEqual(t, redeemActionRedeem, redeemActionSkipCompleted)
}

// ---------------------------------------------------------------------------
// RedeemCode.IsUsed / CanUse interaction with resolveRedeemAction
// ---------------------------------------------------------------------------

func TestResolveRedeemAction_IsUsedCanUseConsistency(t *testing.T) {
	t.Parallel()

	usedCode := &RedeemCode{Status: StatusUsed}
	unusedCode := &RedeemCode{Status: StatusUnused}

	// Verify our decision function is consistent with the domain model methods
	assert.True(t, usedCode.IsUsed())
	assert.False(t, usedCode.CanUse())
	assert.Equal(t, redeemActionSkipCompleted, resolveRedeemAction(usedCode, nil))

	assert.False(t, unusedCode.IsUsed())
	assert.True(t, unusedCode.CanUse())
	assert.Equal(t, redeemActionRedeem, resolveRedeemAction(unusedCode, nil))
}

func TestExpectedNotificationProviderKeyPrefersOrderInstanceProvider(t *testing.T) {
	t.Parallel()

	registry := payment.NewRegistry()
	registry.Register(paymentFulfillmentTestProvider{
		key:            payment.TypeAlipay,
		supportedTypes: []payment.PaymentType{payment.TypeAlipay},
	})

	assert.Equal(t,
		payment.TypeEasyPay,
		expectedNotificationProviderKey(registry, payment.TypeAlipay, "", payment.TypeEasyPay),
	)
}

func TestExpectedNotificationProviderKeyUsesRegistryMappingForLegacyOrders(t *testing.T) {
	t.Parallel()

	registry := payment.NewRegistry()
	registry.Register(paymentFulfillmentTestProvider{
		key:            payment.TypeEasyPay,
		supportedTypes: []payment.PaymentType{payment.TypeAlipay},
	})

	assert.Equal(t,
		payment.TypeEasyPay,
		expectedNotificationProviderKey(registry, payment.TypeAlipay, "", ""),
	)
}

func TestExpectedNotificationProviderKeyFallsBackToPaymentType(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		payment.TypeWxpay,
		expectedNotificationProviderKey(nil, payment.TypeWxpay, "", ""),
	)
}

func TestExpectedNotificationProviderKeyPrefersOrderSnapshotProviderKey(t *testing.T) {
	t.Parallel()

	registry := payment.NewRegistry()
	registry.Register(paymentFulfillmentTestProvider{
		key:            payment.TypeAlipay,
		supportedTypes: []payment.PaymentType{payment.TypeAlipay},
	})

	assert.Equal(t,
		payment.TypeEasyPay,
		expectedNotificationProviderKey(registry, payment.TypeAlipay, payment.TypeEasyPay, ""),
	)
}

func TestExpectedNotificationProviderKeyForOrderUsesSnapshotProviderKey(t *testing.T) {
	t.Parallel()

	registry := payment.NewRegistry()
	registry.Register(paymentFulfillmentTestProvider{
		key:            payment.TypeAlipay,
		supportedTypes: []payment.PaymentType{payment.TypeAlipay},
	})

	order := &dbent.PaymentOrder{
		PaymentType: payment.TypeAlipay,
		ProviderSnapshot: map[string]any{
			"schema_version": 1,
			"provider_key":   payment.TypeEasyPay,
		},
	}

	assert.Equal(t,
		payment.TypeEasyPay,
		expectedNotificationProviderKeyForOrder(registry, order, ""),
	)
}

func TestValidateProviderNotificationMetadataRejectsWxpaySnapshotMismatch(t *testing.T) {
	t.Parallel()

	order := &dbent.PaymentOrder{
		PaymentType: payment.TypeWxpay,
		ProviderSnapshot: map[string]any{
			"schema_version":  1,
			"merchant_app_id": "wx-app-expected",
			"merchant_id":     "mch-expected",
			"currency":        "CNY",
		},
	}

	err := validateProviderNotificationMetadata(order, payment.TypeWxpay, map[string]string{
		"appid":       "wx-app-other",
		"mchid":       "mch-expected",
		"currency":    "CNY",
		"trade_state": "SUCCESS",
	})
	assert.ErrorContains(t, err, "wxpay appid mismatch")
}

func TestValidateProviderNotificationMetadataAllowsLegacyOrdersWithoutSnapshotFields(t *testing.T) {
	t.Parallel()

	order := &dbent.PaymentOrder{
		PaymentType: payment.TypeWxpay,
		ProviderSnapshot: map[string]any{
			"schema_version":       1,
			"provider_instance_id": "9",
			"provider_key":         payment.TypeWxpay,
		},
	}

	err := validateProviderNotificationMetadata(order, payment.TypeWxpay, map[string]string{
		"appid":       "wx-app-runtime",
		"mchid":       "mch-runtime",
		"currency":    "CNY",
		"trade_state": "SUCCESS",
	})
	assert.NoError(t, err)
}

func TestParseLegacyPaymentOrderID(t *testing.T) {
	t.Parallel()

	oid, ok := parseLegacyPaymentOrderID("sub2_42", &dbent.NotFoundError{})
	assert.True(t, ok)
	assert.EqualValues(t, 42, oid)

	_, ok = parseLegacyPaymentOrderID("42", &dbent.NotFoundError{})
	assert.False(t, ok)

	_, ok = parseLegacyPaymentOrderID("sub2_42", errors.New("db down"))
	assert.False(t, ok)
}

func TestIsValidProviderAmount(t *testing.T) {
	t.Parallel()

	assert.True(t, isValidProviderAmount(0.01))
	assert.False(t, isValidProviderAmount(0))
	assert.False(t, isValidProviderAmount(-1))
	assert.False(t, isValidProviderAmount(math.NaN()))
	assert.False(t, isValidProviderAmount(math.Inf(1)))
}

func TestValidateProviderNotificationMetadataRejectsAlipaySnapshotMismatch(t *testing.T) {
	t.Parallel()

	order := &dbent.PaymentOrder{
		PaymentType: payment.TypeAlipay,
		ProviderSnapshot: map[string]any{
			"schema_version":  2,
			"merchant_app_id": "alipay-app-expected",
		},
	}

	err := validateProviderNotificationMetadata(order, payment.TypeAlipay, map[string]string{
		"app_id": "alipay-app-other",
	})
	assert.ErrorContains(t, err, "alipay app_id mismatch")
}

func TestValidateProviderNotificationMetadataRejectsEasyPaySnapshotMismatch(t *testing.T) {
	t.Parallel()

	order := &dbent.PaymentOrder{
		PaymentType: payment.TypeAlipay,
		ProviderSnapshot: map[string]any{
			"schema_version": 2,
			"merchant_id":    "pid-expected",
		},
	}

	err := validateProviderNotificationMetadata(order, payment.TypeEasyPay, map[string]string{
		"pid": "pid-other",
	})
	assert.ErrorContains(t, err, "easypay pid mismatch")
}

func TestValidateProviderNotificationMetadataRejectsAirwallexSnapshotMismatch(t *testing.T) {
	t.Parallel()

	order := &dbent.PaymentOrder{
		PaymentType: payment.TypeAirwallex,
		ProviderSnapshot: map[string]any{
			"schema_version": 2,
			"merchant_id":    "acct_expected",
			"currency":       "CNY",
		},
	}

	err := validateProviderNotificationMetadata(order, payment.TypeAirwallex, map[string]string{
		"account_id": "acct_other",
		"currency":   "CNY",
		"status":     "SUCCEEDED",
	})
	assert.ErrorContains(t, err, "airwallex account_id mismatch")

	err = validateProviderNotificationMetadata(order, payment.TypeAirwallex, map[string]string{
		"account_id": "acct_expected",
		"currency":   "USD",
		"status":     "SUCCEEDED",
	})
	assert.ErrorContains(t, err, "airwallex currency mismatch")
}

func TestValidateProviderNotificationMetadataRejectsStripeCurrencyMismatch(t *testing.T) {
	t.Parallel()

	order := &dbent.PaymentOrder{
		PaymentType: payment.TypeStripe,
		ProviderSnapshot: map[string]any{
			"schema_version": 2,
			"currency":       "HKD",
		},
	}

	err := validateProviderNotificationMetadata(order, payment.TypeStripe, map[string]string{
		"currency": "USD",
	})
	assert.ErrorContains(t, err, "stripe currency mismatch")
}

func TestPaymentAmountToleranceForThreeDecimalCurrency(t *testing.T) {
	t.Parallel()

	assert.Equal(t, amountToleranceCNY, paymentAmountToleranceForCurrency("CNY"))
	assert.Equal(t, amountToleranceCNY, paymentAmountToleranceForCurrency("JPY"))
	assert.InDelta(t, 0.0005, paymentAmountToleranceForCurrency("KWD"), 1e-12)
}

func TestAcquireBalanceFulfillmentLeaseRejectsFreshRechargingOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, time.Now().UTC())
	svc := &PaymentService{entClient: client}

	lease, err := svc.acquireBalanceFulfillmentLease(ctx, order)
	require.Nil(t, lease)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRecharging, reloaded.Status)
}

func TestAcquireBalanceFulfillmentLeaseRecoversStaleRechargingOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	staleAt := time.Now().UTC().Add(-balanceFulfillmentLeaseDuration - time.Minute)
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleAt)
	svc := &PaymentService{entClient: client}

	lease, err := svc.acquireBalanceFulfillmentLease(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, lease)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRecharging, reloaded.Status)
	require.True(t, reloaded.UpdatedAt.After(staleAt))
	require.True(t, lease.version.Equal(reloaded.UpdatedAt))
}

func TestAcquireBalanceFulfillmentLeaseHandlesClaimableAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client}

	paid := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusPaid, time.Now().UTC())
	lease, err := svc.acquireBalanceFulfillmentLease(ctx, paid)
	require.NoError(t, err)
	require.NotNil(t, lease)

	completed := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusCompleted, time.Now().UTC())
	lease, err = svc.acquireBalanceFulfillmentLease(ctx, completed)
	require.NoError(t, err)
	require.Nil(t, lease)

	expired := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusExpired, time.Now().UTC())
	lease, err = svc.acquireBalanceFulfillmentLease(ctx, expired)
	require.Nil(t, lease)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))

	lease, err = svc.acquireBalanceFulfillmentLease(ctx, nil)
	require.Nil(t, lease)
	require.Error(t, err)
	require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))
}

func TestRetryFulfillmentKeepsFreshSubscriptionRechargingBlocked(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, time.Now().UTC())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionGroupID(7).
		SetSubscriptionDays(30).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	err = svc.RetryFulfillment(ctx, order.ID)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
}

func TestAcquireSubscriptionFulfillmentLeaseRejectsFreshAndRecoversStaleOrders(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client}

	fresh := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, time.Now().UTC())
	lease, err := svc.acquireSubscriptionFulfillmentLease(ctx, fresh)
	require.Nil(t, lease)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))

	staleAt := staleSubscriptionFulfillmentTimeForTest()
	stale := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleAt)
	lease, err = svc.acquireSubscriptionFulfillmentLease(ctx, stale)
	require.NoError(t, err)
	require.NotNil(t, lease)

	reloaded, err := client.PaymentOrder.Get(ctx, stale.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRecharging, reloaded.Status)
	require.True(t, reloaded.UpdatedAt.After(staleAt))
	require.True(t, lease.version.Equal(reloaded.UpdatedAt))
}

func TestAcquireSubscriptionFulfillmentLeaseHandlesClaimableAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client}

	paid := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusPaid, time.Now().UTC())
	lease, err := svc.acquireSubscriptionFulfillmentLease(ctx, paid)
	require.NoError(t, err)
	require.NotNil(t, lease)

	completed := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusCompleted, time.Now().UTC())
	lease, err = svc.acquireSubscriptionFulfillmentLease(ctx, completed)
	require.NoError(t, err)
	require.Nil(t, lease)

	expired := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusExpired, time.Now().UTC())
	lease, err = svc.acquireSubscriptionFulfillmentLease(ctx, expired)
	require.Nil(t, lease)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))

	lease, err = svc.acquireSubscriptionFulfillmentLease(ctx, nil)
	require.Nil(t, lease)
	require.Error(t, err)
	require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))
}

func TestSubscriptionFulfillmentLeaseFencesStaleWorker(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	staleAt := staleSubscriptionFulfillmentTimeForTest()
	order := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleAt)
	svc := &PaymentService{entClient: client}

	firstLease, err := svc.acquireSubscriptionFulfillmentLease(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, firstLease)

	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetUpdatedAt(staleAt).Save(ctx)
	require.NoError(t, err)
	secondLease, err := svc.acquireSubscriptionFulfillmentLease(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, secondLease)
	require.False(t, firstLease.version.Equal(secondLease.version))

	err = svc.markSubscriptionCompletedWithLease(ctx, order, firstLease, "SUBSCRIPTION_SUCCESS")
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	svc.markSubscriptionFailedWithLease(ctx, order.ID, firstLease, errors.New("stale worker failure"))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRecharging, reloaded.Status)

	require.NoError(t, svc.markSubscriptionCompletedWithLease(ctx, order, secondLease, "SUBSCRIPTION_SUCCESS"))
	reloaded, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.NoError(t, svc.markSubscriptionCompletedWithLease(ctx, order, firstLease, "SUBSCRIPTION_SUCCESS"))
}

func TestMarkSubscriptionFailedWithLeaseOnlyChangesCurrentOwner(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleSubscriptionFulfillmentTimeForTest())
	svc := &PaymentService{entClient: client}

	lease, err := svc.acquireSubscriptionFulfillmentLease(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, lease)
	svc.markSubscriptionFailedWithLease(ctx, order.ID, lease, errors.New("temporary fulfillment error"))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, reloaded.Status)
	require.NotNil(t, reloaded.FailedAt)
	require.NotEmpty(t, reloaded.FailedReason)
}

func TestRetryFulfillmentRecoversStaleSubscriptionOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleSubscriptionFulfillmentTimeForTest())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetProviderSnapshot(map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd": 10.0,
				"validity_days":    30.0,
			},
		}).
		SetUpdatedAt(staleSubscriptionFulfillmentTimeForTest()).
		Save(ctx)
	require.NoError(t, err)

	groupRepo := &subscriptionGroupRepoStub{group: &Group{ID: *order.SubscriptionGroupID, Status: StatusActive}}
	subRepo := newSubscriptionUserSubRepoStub()
	svc := &PaymentService{
		entClient:       client,
		groupRepo:       groupRepo,
		subscriptionSvc: NewSubscriptionService(groupRepo, subRepo, nil, nil, nil, nil, nil, nil),
	}

	require.NoError(t, svc.RetryFulfillment(ctx, order.ID))

	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, 1, subRepo.createCalls)
}

func TestDoSubCompletesAlreadyAppliedPurchaseWithCurrentLease(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleSubscriptionFulfillmentTimeForTest())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetProviderSnapshot(map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd": 10.0,
				"validity_days":    30.0,
			},
		}).
		SetUpdatedAt(staleSubscriptionFulfillmentTimeForTest()).
		Save(ctx)
	require.NoError(t, err)

	groupRepo := &subscriptionGroupRepoStub{group: &Group{ID: *order.SubscriptionGroupID, Status: StatusActive}}
	svc := &PaymentService{entClient: client, groupRepo: groupRepo}
	svc.writeAuditLog(ctx, order.ID, "SUBSCRIPTION_SUCCESS", "system", map[string]any{"recovered": true})
	lease, err := svc.acquireSubscriptionFulfillmentLease(ctx, order)
	require.NoError(t, err)

	require.NoError(t, svc.doSub(ctx, order, lease))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}

func TestDoSubLifecycleCompletesAlreadyAppliedRenewWithCurrentLease(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createSubscriptionFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleSubscriptionFulfillmentTimeForTest())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetProviderSnapshot(map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd": 10.0,
				"validity_days":    30.0,
			},
		}).
		SetUpdatedAt(staleSubscriptionFulfillmentTimeForTest()).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	svc.writeAuditLog(ctx, order.ID, "SUBSCRIPTION_SUCCESS", "system", map[string]any{"recovered": true})
	lease, err := svc.acquireSubscriptionFulfillmentLease(ctx, order)
	require.NoError(t, err)

	require.NoError(t, svc.doSubLifecycle(ctx, order, SubscriptionIntentRenew, 1, lease))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}

func TestBalanceFulfillmentLeaseFencesStaleWorker(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	staleAt := staleBalanceFulfillmentTimeForTest()
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleAt)
	svc := &PaymentService{entClient: client}

	firstLease, err := svc.acquireBalanceFulfillmentLease(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, firstLease)

	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetUpdatedAt(staleAt).Save(ctx)
	require.NoError(t, err)
	secondLease, err := svc.acquireBalanceFulfillmentLease(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, secondLease)
	require.False(t, firstLease.version.Equal(secondLease.version))

	err = svc.markBalanceCompletedWithLease(ctx, order, firstLease, "RECHARGE_SUCCESS")
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	svc.markBalanceFailedWithLease(ctx, order.ID, firstLease, errors.New("stale worker failure"))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRecharging, reloaded.Status)

	require.NoError(t, svc.markBalanceCompletedWithLease(ctx, order, secondLease, "RECHARGE_SUCCESS"))
	reloaded, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	// A stale worker that observes completion must not emit a duplicate audit or
	// turn the completed order into FAILED.
	require.NoError(t, svc.markBalanceCompletedWithLease(ctx, order, firstLease, "RECHARGE_SUCCESS"))
}

func TestMarkBalanceFailedWithLeaseOnlyChangesCurrentOwner(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusRecharging, staleBalanceFulfillmentTimeForTest())
	svc := &PaymentService{entClient: client}

	lease, err := svc.acquireBalanceFulfillmentLease(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, lease)
	svc.markBalanceFailedWithLease(ctx, order.ID, lease, errors.New("temporary fulfillment error"))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, reloaded.Status)
	require.NotNil(t, reloaded.FailedAt)
	require.NotEmpty(t, reloaded.FailedReason)
}

func staleBalanceFulfillmentTimeForTest() time.Time {
	return time.Now().UTC().Add(-balanceFulfillmentLeaseDuration - time.Minute)
}

func staleSubscriptionFulfillmentTimeForTest() time.Time {
	return time.Now().UTC().Add(-subscriptionFulfillmentLeaseDuration - time.Minute)
}

func createBalanceFulfillmentLeaseOrder(
	t *testing.T,
	ctx context.Context,
	client *dbent.Client,
	status string,
	updatedAt time.Time,
) *dbent.PaymentOrder {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user, err := client.User.Create().
		SetEmail("balance-lease-" + suffix + "@example.com").
		SetPasswordHash("hash").
		SetUsername("balance-lease-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(80).
		SetPayAmount(80).
		SetFeeRate(0).
		SetRechargeCode("BAL-LEASE-" + suffix).
		SetOutTradeNo("sub2_balance_lease_" + suffix).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-" + suffix).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetPaidAt(time.Now().Add(-time.Hour)).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetUpdatedAt(updatedAt).
		Save(ctx)
	require.NoError(t, err)
	return order
}

func createSubscriptionFulfillmentLeaseOrder(
	t *testing.T,
	ctx context.Context,
	client *dbent.Client,
	status string,
	updatedAt time.Time,
) *dbent.PaymentOrder {
	t.Helper()
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, status, updatedAt)
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionGroupID(7).
		SetSubscriptionDays(30).
		SetUpdatedAt(updatedAt).
		Save(ctx)
	require.NoError(t, err)
	return order
}
