//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

// Fork adaptation of upstream 7a70de401 (fix(payment): isolate fulfillment
// from redeem limits). Upstream's tests live in payment_fulfillment_test.go /
// redeem_admin_fulfillment_test.go and rely on helpers the fork does not have,
// so they are rebuilt here on the fork's balance-fulfillment lease helpers.

type redeemIsolationCacheStub struct {
	count          int
	getCalls       int
	incrementCalls int
	acquireCalls   int
	releaseCalls   int
}

func (c *redeemIsolationCacheStub) GetRedeemAttemptCount(context.Context, int64) (int, error) {
	c.getCalls++
	return c.count, nil
}

func (c *redeemIsolationCacheStub) IncrementRedeemAttemptCount(context.Context, int64) error {
	c.incrementCalls++
	c.count++
	return nil
}

func (c *redeemIsolationCacheStub) AcquireRedeemLock(context.Context, string, time.Duration) (bool, error) {
	c.acquireCalls++
	return true, nil
}

func (c *redeemIsolationCacheStub) ReleaseRedeemLock(context.Context, string) error {
	c.releaseCalls++
	return nil
}

// redeemIsolationRepo is an in-memory redeem code repository that supports the
// full Redeem flow (Create / GetByCode / Use / GetByID).
type redeemIsolationRepo struct {
	redeemCodeRepoStub
	createCalls int
}

func (r *redeemIsolationRepo) Create(_ context.Context, code *RedeemCode) error {
	r.createCalls++
	if r.codesByCode == nil {
		r.codesByCode = make(map[string]*RedeemCode)
	}
	cloned := *code
	cloned.ID = int64(100 + r.createCalls)
	code.ID = cloned.ID
	r.codesByCode[cloned.Code] = &cloned
	return nil
}

func (r *redeemIsolationRepo) GetByID(_ context.Context, id int64) (*RedeemCode, error) {
	for _, code := range r.codesByCode {
		if code.ID == id {
			cloned := *code
			return &cloned, nil
		}
	}
	return nil, ErrRedeemCodeNotFound
}

// redeemRaceRepo misses on the first lookup, fails Create, and then returns a
// code written by someone else on the re-read.
type redeemRaceRepo struct {
	RedeemCodeRepository
	lookups int
	reread  *RedeemCode
}

func (r *redeemRaceRepo) GetByCode(context.Context, string) (*RedeemCode, error) {
	r.lookups++
	if r.lookups == 1 {
		return nil, ErrRedeemCodeNotFound
	}
	cloned := *r.reread
	return &cloned, nil
}

func (r *redeemRaceRepo) Create(context.Context, *RedeemCode) error {
	return errors.New("unique constraint")
}

func TestValidatePaymentRedeemCode(t *testing.T) {
	t.Parallel()
	userID := int64(42)
	otherUserID := int64(43)
	order := &dbent.PaymentOrder{ID: 7, UserID: userID, RechargeCode: "PAY-7-12345", Amount: 80}

	tests := []struct {
		name    string
		code    *RedeemCode
		wantErr string
	}{
		{name: "unused code", code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeBalance, Value: 80, Status: StatusUnused}},
		{name: "used by order user", code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeBalance, Value: 80, Status: StatusUsed, UsedBy: &userID}},
		{name: "unused with user", code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeBalance, Value: 80, Status: StatusUnused, UsedBy: &userID}, wantErr: "unused payment redeem code has a user"},
		{name: "used without user", code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeBalance, Value: 80, Status: StatusUsed}, wantErr: "user mismatch"},
		{name: "used by another user", code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeBalance, Value: 80, Status: StatusUsed, UsedBy: &otherUserID}, wantErr: "user mismatch"},
		{name: "wrong code", code: &RedeemCode{Code: "OTHER", Type: RedeemTypeBalance, Value: 80, Status: StatusUnused}, wantErr: "code mismatch"},
		{name: "wrong type", code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeConcurrency, Value: 80, Status: StatusUnused}, wantErr: "type mismatch"},
		{name: "wrong amount", code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeBalance, Value: 79, Status: StatusUnused}, wantErr: "amount mismatch"},
		{name: "invalid status", code: &RedeemCode{Code: order.RechargeCode, Type: RedeemTypeBalance, Value: 80, Status: StatusDisabled}, wantErr: "invalid status"},
		{name: "nil code", code: nil, wantErr: "requires an order and code"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePaymentRedeemCode(order, tt.code)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPublicRedeemStillEnforcesFailureLimit(t *testing.T) {
	cache := &redeemIsolationCacheStub{count: redeemMaxErrorsPerHour}
	svc := &RedeemService{cache: cache}

	result, err := svc.Redeem(context.Background(), 42, "PUBLIC-CODE")

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrRedeemRateLimited)
	require.Equal(t, 1, cache.getCalls)
	require.Zero(t, cache.incrementCalls)
	require.Zero(t, cache.acquireCalls)
}

func TestPublicRedeemStillIncrementsInvalidCodeFailures(t *testing.T) {
	cache := &redeemIsolationCacheStub{}
	svc := &RedeemService{redeemRepo: &redeemIsolationRepo{}, cache: cache}

	result, err := svc.Redeem(context.Background(), 42, "MISSING")

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrRedeemCodeNotFound)
	require.Equal(t, 1, cache.getCalls)
	require.Equal(t, 1, cache.incrementCalls)
	require.Equal(t, 1, cache.count)
	require.Equal(t, 1, cache.acquireCalls)
	require.Equal(t, 1, cache.releaseCalls)
}

func TestPaymentRedeemDoesNotIncrementFailureLimit(t *testing.T) {
	ctx := context.Background()
	cases := map[string]*RedeemCode{
		"expired": {ID: 1, Code: "PAY-EXPIRED", Type: RedeemTypeBalance, Value: 10, Status: StatusExpired},
		"used":    {ID: 2, Code: "PAY-USED", Type: RedeemTypeBalance, Value: 10, Status: StatusUsed},
	}
	wantErr := map[string]error{"expired": ErrRedeemCodeExpired, "used": ErrRedeemCodeUsed}

	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			cache := &redeemIsolationCacheStub{count: redeemMaxErrorsPerHour}
			repo := &redeemIsolationRepo{redeemCodeRepoStub: redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{code.Code: code}}}
			svc := &RedeemService{redeemRepo: repo, cache: cache}

			result, err := svc.redeemForPaymentFulfillment(ctx, 42, code.Code)

			require.Nil(t, result)
			require.ErrorIs(t, err, wantErr[name])
			require.Zero(t, cache.getCalls)
			require.Zero(t, cache.incrementCalls)
			require.Equal(t, 1, cache.acquireCalls)
			require.Equal(t, 1, cache.releaseCalls)
		})
	}

	t.Run("missing", func(t *testing.T) {
		cache := &redeemIsolationCacheStub{count: redeemMaxErrorsPerHour}
		svc := &RedeemService{redeemRepo: &redeemIsolationRepo{}, cache: cache}

		result, err := svc.RedeemForAdminFulfillment(ctx, 42, "ADMIN-MISSING")

		require.Nil(t, result)
		require.ErrorIs(t, err, ErrRedeemCodeNotFound)
		require.Zero(t, cache.getCalls)
		require.Zero(t, cache.incrementCalls)
	})
}

func TestExecuteBalanceFulfillmentBypassesUserRedeemRateLimit(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusPaid, time.Now().UTC())

	redeemRepo := &redeemIsolationRepo{}
	credited := 0.0
	userRepo := &mockUserRepo{getByIDUser: &User{ID: order.UserID, Balance: 0}}
	userRepo.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		require.Equal(t, order.UserID, id)
		credited += amount
		return nil
	}
	cache := &redeemIsolationCacheStub{count: redeemMaxErrorsPerHour}
	redeemService := NewRedeemService(redeemRepo, userRepo, nil, cache, nil, client, nil, nil)
	svc := &PaymentService{entClient: client, redeemService: redeemService, userRepo: userRepo}

	require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
	require.InDelta(t, order.Amount, credited, 1e-8)
	require.Zero(t, cache.getCalls, "trusted payment fulfillment must not read the public failure counter")
	require.Zero(t, cache.incrementCalls, "trusted payment fulfillment must not mutate the public failure counter")
	require.Equal(t, 1, cache.acquireCalls)
	require.Equal(t, 1, cache.releaseCalls)
	require.Equal(t, 1, redeemRepo.createCalls)
	require.Len(t, redeemRepo.useCalls, 1)

	usedCode := redeemRepo.codesByCode[order.RechargeCode]
	require.Equal(t, StatusUsed, usedCode.Status)
	require.NotNil(t, usedCode.UsedBy)
	require.Equal(t, order.UserID, *usedCode.UsedBy)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}

func TestExecuteBalanceFulfillmentRecoversAfterRedeemWithoutCreditingAgain(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusPaid, time.Now().UTC())

	usedBy := order.UserID
	redeemRepo := &redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{
		order.RechargeCode: {
			ID:     101,
			Code:   order.RechargeCode,
			Type:   RedeemTypeBalance,
			Value:  order.Amount,
			Status: StatusUsed,
			UsedBy: &usedBy,
		},
	}}
	svc := &PaymentService{entClient: client, redeemService: &RedeemService{redeemRepo: redeemRepo}}

	require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
	require.Empty(t, redeemRepo.useCalls, "an already-used order code must not be redeemed again")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}

func TestExecuteBalanceFulfillmentRejectsCodeUsedByAnotherUser(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createBalanceFulfillmentLeaseOrder(t, ctx, client, OrderStatusPaid, time.Now().UTC())

	otherUserID := order.UserID + 1
	redeemRepo := &redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{
		order.RechargeCode: {
			ID:     101,
			Code:   order.RechargeCode,
			Type:   RedeemTypeBalance,
			Value:  order.Amount,
			Status: StatusUsed,
			UsedBy: &otherUserID,
		},
	}}
	svc := &PaymentService{entClient: client, redeemService: &RedeemService{redeemRepo: redeemRepo}}

	err := svc.ExecuteBalanceFulfillment(ctx, order.ID)
	require.ErrorContains(t, err, "payment redeem code user mismatch")
	require.Empty(t, redeemRepo.useCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, reloaded.Status)
	require.Nil(t, reloaded.CompletedAt)
}

func TestGetOrCreateBalanceRedeemCodeFailsClosedOnLookupError(t *testing.T) {
	ctx := context.Background()
	repo := &redeemLookupErrorRepo{err: errors.New("db connection lost")}
	svc := &PaymentService{redeemService: &RedeemService{redeemRepo: repo}}

	_, err := svc.getOrCreateBalanceRedeemCode(ctx, &dbent.PaymentOrder{RechargeCode: "balance-lookup-error", Amount: 80})
	require.ErrorContains(t, err, "lookup payment redeem code")
	require.Zero(t, repo.createCalls, "a lookup failure must not fall through to creating a new code")
}

func TestGetOrCreateBalanceRedeemCodeValidatesConcurrentlyCreatedCode(t *testing.T) {
	ctx := context.Background()
	otherUserID := int64(43)
	order := &dbent.PaymentOrder{ID: 9, UserID: 42, RechargeCode: "balance-race-code", Amount: 80}
	repo := &redeemRaceRepo{reread: &RedeemCode{
		Code: order.RechargeCode, Type: RedeemTypeBalance, Value: order.Amount, Status: StatusUsed, UsedBy: &otherUserID,
	}}
	svc := &PaymentService{redeemService: &RedeemService{redeemRepo: repo}}

	_, err := svc.getOrCreateBalanceRedeemCode(ctx, order)
	require.ErrorContains(t, err, "payment redeem code user mismatch")
	require.Equal(t, 2, repo.lookups)
}

type redeemLookupErrorRepo struct {
	RedeemCodeRepository
	err         error
	createCalls int
}

func (r *redeemLookupErrorRepo) GetByCode(context.Context, string) (*RedeemCode, error) {
	return nil, r.err
}

func (r *redeemLookupErrorRepo) Create(context.Context, *RedeemCode) error {
	r.createCalls++
	return nil
}

// Upstream keeps the normal redeem-level affiliate rebate for admin
// fulfillment; in the fork the redeem-level reward is points accrual, which
// likewise stays enabled because admin fulfillment does not mark the context
// with ContextSkipRedeemAffiliate. This test covers the limit bypass.
func TestAdminFulfillmentBypassesRedeemRateLimit(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userID := int64(42)
	code := &RedeemCode{ID: 102, Code: "ADMIN-SUCCESS", Type: RedeemTypeBalance, Value: 20, Status: StatusUnused}
	redeemRepo := &redeemIsolationRepo{redeemCodeRepoStub: redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{code.Code: code}}}
	credited := 0.0
	userRepo := &mockUserRepo{getByIDUser: &User{ID: userID}}
	userRepo.updateBalanceFn = func(_ context.Context, _ int64, amount float64) error {
		credited += amount
		return nil
	}
	cache := &redeemIsolationCacheStub{count: redeemMaxErrorsPerHour}
	svc := NewRedeemService(redeemRepo, userRepo, nil, cache, nil, client, nil, nil)

	result, err := svc.RedeemForAdminFulfillment(ctx, userID, code.Code)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, StatusUsed, result.Status)
	require.InDelta(t, 20, credited, 1e-8)
	require.Zero(t, cache.getCalls)
	require.Zero(t, cache.incrementCalls)
	require.Equal(t, 1, cache.acquireCalls)
	require.Equal(t, 1, cache.releaseCalls)
	require.Len(t, redeemRepo.useCalls, 1)
}
