//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// Fork-specific coverage for REFUND_PENDING finalization: the deduction
// replayed on gateway success comes from the REFUND_PENDING snapshot and uses
// the fork's wallet clamp / subscription close primitives.

func setPendingRefundSnapshotForTest(t *testing.T, ctx context.Context, client *dbent.Client, orderID int64, detail string) {
	t.Helper()
	_, err := client.PaymentAuditLog.Delete().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Exec(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(orderID, 10)).
		SetAction("REFUND_PENDING").
		SetOperator("admin").
		SetDetail(detail).
		Save(ctx)
	require.NoError(t, err)
}

func countRefundAuditForTest(t *testing.T, ctx context.Context, client *dbent.Client, orderID int64, action string) int {
	t.Helper()
	n, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)), paymentauditlog.ActionEQ(action)).
		Count(ctx)
	require.NoError(t, err)
	return n
}

func TestQueryAndFinalizeRefundReplaysSnapshotDeduction(t *testing.T) {
	for _, tc := range []struct {
		name          string
		detail        string
		balance       float64
		wantDeducted  float64
		wantShortfall int
	}{
		{name: "none", detail: `{"deductionType":"none","deductionRollbackOK":true,"gatewayAmount":95}`, balance: 500},
		{name: "clamped", detail: `{"deductionType":"balance","balanceToDeduct":30,"deductionRollbackOK":true,"gatewayAmount":95}`, balance: 10, wantDeducted: 10, wantShortfall: 1},
		{name: "full", detail: `{"deductionType":"balance","balanceToDeduct":30,"deductionRollbackOK":true,"gatewayAmount":95}`, balance: 80, wantDeducted: 30},
		{name: "negative-balance", detail: `{"deductionType":"balance","balanceToDeduct":30,"deductionRollbackOK":true,"gatewayAmount":95}`, balance: -5, wantShortfall: 1},
		{name: "rollback-failed", detail: `{"deductionType":"balance","balanceToDeduct":30,"deductionRollbackOK":false,"gatewayAmount":95}`, balance: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "snapshot-"+tc.name)
			setPendingRefundSnapshotForTest(t, ctx, client, order.ID, tc.detail)

			var deducted float64
			svc := &PaymentService{
				entClient:    client,
				loadBalancer: &captureLoadBalancer{},
				userRepo: &mockUserRepo{
					getByIDUser: &User{Balance: tc.balance},
					deductBalanceFn: func(_ context.Context, _ int64, amount float64) error {
						deducted += amount
						return nil
					},
				},
			}
			prov := &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{RefundID: "rf", Status: payment.ProviderStatusSuccess}}
			restore := replacePaymentProviderFactoryForTest(t, prov)
			defer restore()

			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, tc.wantDeducted, deducted)
			require.Equal(t, tc.wantDeducted, result.BalanceDeducted)
			require.Equal(t, "95.00", prov.lastQuery.Amount, "query must reuse the gateway amount sent to Refund")
			require.Equal(t, order.OutTradeNo, prov.lastQuery.OrderID)
			require.Equal(t, order.PaymentTradeNo, prov.lastQuery.TradeNo)
			require.Equal(t, tc.wantShortfall, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_FINALIZE_BALANCE_SHORTFALL"))

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefunded, reloaded.Status)
			require.Equal(t, 1, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_SUCCESS"))
		})
	}
}

func TestQueryAndFinalizeRefundLegacySnapshotRecomputesGatewayAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "legacy-gateway")
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	prov := &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusPending}}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.RefundPending)
	require.Equal(t, "100.00", prov.lastQuery.Amount)
	require.Equal(t, 1, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_QUERY_PENDING"))
}

func TestQueryAndFinalizeRefundDeductionFailureKeepsPending(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "deduct-fail")
	setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"balance","balanceToDeduct":30,"deductionRollbackOK":true,"gatewayAmount":100}`)
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{
			getByIDUser:     &User{Balance: 100},
			deductBalanceFn: func(context.Context, int64, float64) error { return errors.New("db down") },
		},
	}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusSuccess}})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Nil(t, result)
	require.ErrorContains(t, err, "db down")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.Equal(t, 1, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_FINALIZE_DEDUCTION_FAILED"))

	// Balance lookup failure takes the same path.
	svc.userRepo = &mockUserRepo{getByIDErr: errors.New("user lookup failed")}
	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.ErrorContains(t, err, "user lookup failed")
}

func TestQueryAndFinalizeRefundConcurrentStatusChangeConflicts(t *testing.T) {
	for _, status := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusFailed} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "conflict-"+status)
			svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: &mockUserRepo{getByIDUser: &User{Balance: 500}}}
			prov := &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: status}}
			prov.onQuery = func() {
				// Another admin finalized the order while this query was in flight.
				_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunded).Save(ctx)
				require.NoError(t, err)
			}
			restore := replacePaymentProviderFactoryForTest(t, prov)
			defer restore()

			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.Nil(t, result)
			require.Equal(t, "CONFLICT", infraerrors.Reason(err))
			require.Zero(t, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_SUCCESS"))
			require.Zero(t, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_FAILED"))
		})
	}
}

func TestQueryAndFinalizeRefundGuards(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}

	_, err := svc.QueryAndFinalizeRefund(ctx, 987654)
	require.Equal(t, "NOT_FOUND", infraerrors.Reason(err))

	order := createPendingRefundOrderForTest(t, ctx, client, "guards")
	prov := &refundQueryProviderTestDouble{queryErr: errors.New("gateway timeout")}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.ErrorContains(t, err, "gateway timeout")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status, "query errors must leave the order pending")

	prov.queryErr = nil
	prov.refundResponse = &payment.RefundResponse{Status: "weird"}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	reloaded, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)

	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))
}

func TestQueryAndFinalizeRefundSubscriptionUsesForkPrimitives(t *testing.T) {
	ctx := context.Background()
	today := TodayEastDayNumber()

	t.Run("purchase closes card", func(t *testing.T) {
		client := newPaymentConfigServiceTestClient(t)
		order := createPendingRefundOrderForTest(t, ctx, client, "sub-close")
		_, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscription).Save(ctx)
		require.NoError(t, err)
		setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"subscription","subscriptionID":77,"subDaysToDeduct":9,"deductionRollbackOK":true,"gatewayAmount":100}`)
		repo := newRefundUserSubRepoStub(&UserSubscription{ID: 77, UserID: order.UserID, GroupID: 8, Status: SubscriptionStatusActive, ExpireDay: today + 10, ExpiresAt: ExpireDayToExpiresAt(today + 10)})
		svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, subscriptionSvc: NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)}
		restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusSuccess}})
		defer restore()

		result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
		require.NoError(t, err)
		require.True(t, result.Success)
		require.Equal(t, int64(77), repo.closeSubscription)
		require.NotNil(t, repo.closeDeleteRow)
		require.False(t, *repo.closeDeleteRow)
		require.Empty(t, repo.shortenDays)
	})

	t.Run("renew revokes renewed days", func(t *testing.T) {
		client := newPaymentConfigServiceTestClient(t)
		order := createPendingRefundOrderForTest(t, ctx, client, "sub-renew")
		_, err := client.PaymentOrder.UpdateOneID(order.ID).
			SetOrderType(payment.OrderTypeSubscription).
			SetSubscriptionDays(30).
			SetProviderSnapshot(map[string]any{
				subscriptionSnapshotKey: map[string]any{
					"daily_amount_usd":       10.0,
					"validity_days":          30.0,
					"intent":                 SubscriptionIntentRenew,
					"target_subscription_id": 78.0,
				},
			}).
			Save(ctx)
		require.NoError(t, err)
		setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"subscription","subscriptionID":78,"subDaysToDeduct":30,"deductionRollbackOK":true,"gatewayAmount":100}`)
		repo := newRefundUserSubRepoStub(&UserSubscription{ID: 78, UserID: order.UserID, GroupID: 8, Status: SubscriptionStatusActive, ExpireDay: today + 40, ExpiresAt: ExpireDayToExpiresAt(today + 40)})
		svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, subscriptionSvc: NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)}
		restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusRefunded}})
		defer restore()

		result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
		require.NoError(t, err)
		require.True(t, result.Success)
		require.Equal(t, 30, result.SubDaysDeducted)
		require.Equal(t, []int{30}, repo.shortenDays)
		require.Nil(t, repo.closeDeleteRow)
	})

	t.Run("missing card tolerated and legacy snapshot falls back", func(t *testing.T) {
		client := newPaymentConfigServiceTestClient(t)
		order := createPendingRefundOrderForTest(t, ctx, client, "sub-missing")
		_, err := client.PaymentOrder.UpdateOneID(order.ID).
			SetOrderType(payment.OrderTypeSubscription).
			SetSubscriptionDays(30).
			SetProviderSnapshot(map[string]any{
				subscriptionSnapshotKey: map[string]any{
					"daily_amount_usd":       10.0,
					"validity_days":          30.0,
					"intent":                 SubscriptionIntentRenew,
					"target_subscription_id": 79.0,
				},
			}).
			Save(ctx)
		require.NoError(t, err)
		repo := newRefundUserSubRepoStub(nil)
		svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, subscriptionSvc: NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)}
		restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusSuccess}})
		defer restore()

		result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
		require.NoError(t, err)
		require.True(t, result.Success)
		require.Zero(t, result.SubDaysDeducted)
	})

	t.Run("subscription error keeps pending", func(t *testing.T) {
		client := newPaymentConfigServiceTestClient(t)
		order := createPendingRefundOrderForTest(t, ctx, client, "sub-error")
		_, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscription).Save(ctx)
		require.NoError(t, err)
		setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"subscription","subscriptionID":80,"deductionRollbackOK":true,"gatewayAmount":100}`)
		repo := newRefundUserSubRepoStub(nil)
		repo.getErr = errors.New("sub repo down")
		svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, subscriptionSvc: NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)}
		restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusSuccess}})
		defer restore()

		_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
		require.ErrorContains(t, err, "sub repo down")
		reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
		require.NoError(t, err)
		require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	})
}

func TestMarkRefundPendingSnapshotsPlannedDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "snapshot-write")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{}}
	plan := &RefundPlan{
		OrderID:           order.ID,
		Order:             order,
		RefundAmount:      50,
		GatewayBaseAmount: 50,
		RefundFeeRate:     0.05,
		RefundFeeAmount:   2.5,
		GatewayAmount:     47.5,
		Reason:            "pending snapshot",
		DeductionType:     payment.DeductionTypeBalance,
		BalanceToDeduct:   20,
	}

	result, err := svc.markRefundPending(ctx, plan, &payment.RefundResponse{RefundID: " rf_snap ", Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	require.True(t, result.RefundPending)

	detail := svc.latestRefundPendingDetail(ctx, order.ID)
	require.True(t, detail.hasSnapshot)
	require.Equal(t, "rf_snap", detail.RefundID)
	require.Equal(t, payment.DeductionTypeBalance, detail.DeductionType)
	require.Equal(t, 20.0, detail.BalanceToDeduct)
	require.Equal(t, 47.5, detail.GatewayAmount)
	require.Equal(t, 2.5, detail.RefundFeeAmount)
	require.True(t, detail.DeductionRollbackOK)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	p := svc.refundFinalizePlan(ctx, reloaded, detail)
	require.Equal(t, 47.5, p.GatewayAmount)
	require.Equal(t, 20.0, p.BalanceToDeduct)
	require.Equal(t, "pending snapshot", p.Reason)
}

func TestPrepareRefundRejectsRefundPendingOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "prepare-pending")
	svc := &PaymentService{entClient: client}

	_, _, err := svc.PrepareRefund(ctx, order.ID, 10, "again", false, true)
	require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))

	_, err = svc.ExecuteRefund(ctx, &RefundPlan{OrderID: order.ID, Order: order})
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
}
