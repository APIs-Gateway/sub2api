//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// Existing service fixtures supply wallet values and record debit callbacks.
// Model the new atomic repository contract; actual DB races/rollback are
// verified with the real repository in integration tests.
func (m *mockUserRepo) DeductRefundBalance(ctx context.Context, id int64, amount float64, allowPartial bool) (float64, error) {
	u, err := m.GetByID(ctx, id)
	if err != nil {
		return 0, err
	}
	available := math.Max(u.Balance, 0)
	if !allowPartial && available < amount {
		return 0, ErrRefundBalanceInsufficient
	}
	deducted := math.Min(available, amount)
	if deducted == 0 {
		return 0, nil
	}
	if err := m.DeductBalance(ctx, id, deducted); err != nil {
		return 0, err
	}
	return deducted, nil
}

type refundWalletCacheTestStub struct {
	BillingCache
	userIDs      []int64
	onInvalidate func()
}

func (c *refundWalletCacheTestStub) InvalidateUserBalance(_ context.Context, userID int64) error {
	c.userIDs = append(c.userIDs, userID)
	if c.onInvalidate != nil {
		c.onInvalidate()
	}
	return nil
}

func TestRefundSettlementWalletCacheInvalidatesOnlyAfterTransaction(t *testing.T) {
	for _, failAudit := range []bool{false, true} {
		name := "committed"
		if failAudit {
			name = "rolled back"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "cache-"+name)
			setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"none","deductionRollbackOK":true,"gatewayAmount":95}`)
			if failAudit {
				client.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
					return dbent.MutateFunc(func(ctx context.Context, mutation dbent.Mutation) (dbent.Value, error) {
						m := mutation.(*dbent.PaymentAuditLogMutation)
						if action, _ := m.Action(); action == "REFUND_SUCCESS" {
							return nil, ErrRefundBalanceInsufficient
						}
						return next.Mutate(ctx, mutation)
					})
				})
			}
			cache := &refundWalletCacheTestStub{onInvalidate: func() {
				// This separate client read would see the old state (or block)
				// if invalidation ran before the financial transaction ended.
				reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
				require.NoError(t, err)
				want := OrderStatusRefunded
				if failAudit {
					want = OrderStatusRefundPending
				}
				require.Equal(t, want, reloaded.Status)
			}}
			billing := &BillingCacheService{cache: cache}
			// Exercise the fallback with no SubscriptionService at all.
			svc := &PaymentService{entClient: client, redeemService: &RedeemService{billingCacheService: billing}}
			result, err := svc.ResolvePendingRefund(ctx, order.ID, "succeeded", "verified", "admin:1")
			if failAudit {
				require.Error(t, err)
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.True(t, result.Success)
			}
			require.Equal(t, []int64{order.UserID}, cache.userIDs)
		})
	}
}

func TestExecuteRefundRechecksWalletBeforeProvider(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "requires force after concurrent consumption"
		if force {
			name = "force debits only current wallet"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "atomic-"+name)
			order, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).Save(ctx)
			require.NoError(t, err)
			var deducted, restored float64
			svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{
				getByIDUser:     &User{Balance: 2},
				deductBalanceFn: func(_ context.Context, _ int64, amount float64) error { deducted += amount; return nil },
				updateBalanceFn: func(_ context.Context, _ int64, amount float64) error { restored += amount; return nil },
			}}
			// No payment trade number means the existing provider bypass can
			// settle success. Non-force must return before reaching even that.
			order.PaymentTradeNo = ""
			plan := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 10, GatewayAmount: 10, Force: force, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 10}
			result, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			if force {
				require.True(t, result.Success)
				require.Equal(t, 2.0, result.BalanceDeducted)
				require.Equal(t, 2.0, plan.BalanceToDeduct)
				require.Equal(t, 2.0, deducted)
			} else {
				require.False(t, result.Success)
				require.True(t, result.RequireForce)
				require.Zero(t, deducted)
				reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
				require.NoError(t, err)
				require.Equal(t, OrderStatusCompleted, reloaded.Status)
				require.Zero(t, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_SUCCESS"))
			}
			require.Zero(t, restored)
		})
	}
}
