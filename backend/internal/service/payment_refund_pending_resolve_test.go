//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// Follow-up coverage for REFUND_PENDING hardening (PR #1175 review):
//   (a) retries of a refund that already reached the gateway are pinned to
//       the original amount / gateway breakdown (same refund number);
//   (b) a renew refund with no days to revoke settles instead of erroring;
//   (c) admins can resolve a REFUND_PENDING order by hand.

type refundCaptureProviderTestDouble struct {
	refundProviderTestDouble
	requests []payment.RefundRequest
	resp     *payment.RefundResponse
}

func (p *refundCaptureProviderTestDouble) Refund(_ context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	p.requests = append(p.requests, req)
	return p.resp, nil
}

func countManualResolvedAuditForTest(t *testing.T, ctx context.Context, svc *PaymentService, orderID int64) int {
	t.Helper()
	n, err := svc.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_MANUAL_RESOLVED")).
		Count(ctx)
	require.NoError(t, err)
	return n
}

func TestPrepareRefundPinsRetryToPreviouslySubmittedRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "pin-retry")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefundFailed).SetRefundAmount(60).Save(ctx)
	require.NoError(t, err)
	setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"none","deductionRollbackOK":true,"refundAmount":60,"gatewayBaseAmount":60,"gatewayAmount":57,"refundFeeRate":5,"refundFeeAmount":3}`)
	svc := &PaymentService{entClient: client}

	_, _, err = svc.PrepareRefund(ctx, order.ID, 80, "different amount", false, false)
	require.Equal(t, "REFUND_AMOUNT_LOCKED", infraerrors.Reason(err))

	for _, amt := range []float64{0, 60, 60.001} {
		plan, early, err := svc.PrepareRefund(ctx, order.ID, amt, "retry", false, false)
		require.NoError(t, err)
		require.Nil(t, early)
		require.Equal(t, 60.0, plan.RefundAmount)
		require.Equal(t, 57.0, plan.GatewayAmount, "retry must reuse the submitted gateway amount")
		require.Equal(t, 60.0, plan.GatewayBaseAmount)
		require.Equal(t, 5.0, plan.RefundFeeRate)
		require.Equal(t, 3.0, plan.RefundFeeAmount)
	}

	// A later user refund request can overwrite refund_amount on the order;
	// the snapshot amount still wins.
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetRefundAmount(90).Save(ctx)
	require.NoError(t, err)
	plan, _, err := svc.PrepareRefund(ctx, order.ID, 0, "retry", false, false)
	require.NoError(t, err)
	require.Equal(t, 60.0, plan.RefundAmount)
}

func TestPrepareRefundPinLegacySnapshotFallsBackToOrderAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	// Helper snapshot carries neither refundAmount nor gatewayAmount.
	order := createPendingRefundOrderForTest(t, ctx, client, "pin-legacy")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefundFailed).SetRefundAmount(40).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}

	_, _, err = svc.PrepareRefund(ctx, order.ID, 50, "", false, false)
	require.Equal(t, "REFUND_AMOUNT_LOCKED", infraerrors.Reason(err))

	plan, _, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false)
	require.NoError(t, err)
	require.Equal(t, 40.0, plan.RefundAmount)
	require.Equal(t, 40.0, plan.GatewayAmount, "no snapshot gateway amount: recomputed from the pinned amount")
}

func TestPrepareRefundWithoutPriorPendingIsNotPinned(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "pin-none")
	_, err := client.PaymentAuditLog.Delete().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10))).Exec(ctx)
	require.NoError(t, err)
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}

	plan, _, err := svc.PrepareRefund(ctx, order.ID, 80, "", false, false)
	require.NoError(t, err)
	require.Equal(t, 80.0, plan.RefundAmount)
	require.Equal(t, 80.0, plan.GatewayAmount)
}

func TestQueryAndFinalizeRefundRenewWithoutDaysCompletes(t *testing.T) {
	ctx := context.Background()
	today := TodayEastDayNumber()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "renew-zero")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeSubscription).
		SetProviderSnapshot(map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd":       10.0,
				"intent":                 SubscriptionIntentRenew,
				"target_subscription_id": 81.0,
			},
		}).
		Save(ctx)
	require.NoError(t, err)
	// Forced refund of a renew order without recorded validity days: the
	// snapshot planned zero days to revoke.
	setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"subscription","subscriptionID":81,"subDaysToDeduct":0,"deductionRollbackOK":true,"gatewayAmount":100}`)
	repo := newRefundUserSubRepoStub(&UserSubscription{ID: 81, UserID: order.UserID, GroupID: 8, Status: SubscriptionStatusActive, ExpireDay: today + 20, ExpiresAt: ExpireDayToExpiresAt(today + 20)})
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, subscriptionSvc: NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusSuccess}})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Zero(t, result.SubDaysDeducted)
	require.Empty(t, repo.shortenDays, "nothing to revoke")
	require.Nil(t, repo.closeDeleteRow, "renew refund must not close the card")

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
	require.Equal(t, 1, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_FINALIZE_NO_RENEW_DAYS"))
}

func TestResolvePendingRefundGuards(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client}

	_, err := svc.ResolvePendingRefund(ctx, 1, "maybe", "", "admin:1")
	require.Equal(t, "INVALID_OUTCOME", infraerrors.Reason(err))

	_, err = svc.ResolvePendingRefund(ctx, 987654, RefundResolutionFailed, "", "admin:1")
	require.Equal(t, "NOT_FOUND", infraerrors.Reason(err))

	order := createPendingRefundOrderForTest(t, ctx, client, "resolve-guards")
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunded).Save(ctx)
	require.NoError(t, err)
	_, err = svc.ResolvePendingRefund(ctx, order.ID, RefundResolutionSucceeded, "", "admin:1")
	require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))
	require.Zero(t, countManualResolvedAuditForTest(t, ctx, svc, order.ID))
}

func TestResolvePendingRefundSucceededReplaysDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "resolve-ok")
	setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"balance","balanceToDeduct":30,"deductionRollbackOK":true,"gatewayAmount":95,"refundAmount":100}`)
	var deducted float64
	svc := &PaymentService{
		entClient: client,
		userRepo: &mockUserRepo{
			getByIDUser: &User{Balance: 500},
			deductBalanceFn: func(_ context.Context, _ int64, amount float64) error {
				deducted += amount
				return nil
			},
		},
	}

	result, err := svc.ResolvePendingRefund(ctx, order.ID, " Succeeded ", " verified in console ", "admin:7")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 30.0, deducted)
	require.Equal(t, 95.0, result.GatewayAmount)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
	require.Equal(t, 1, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_SUCCESS"))

	logs, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionHasPrefix("REFUND_MANUAL_RESOLVED")).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, "admin:7", logs[0].Operator)
	require.Contains(t, logs[0].Detail, `"outcome":"succeeded"`)
	require.Contains(t, logs[0].Detail, `"note":"verified in console"`)

	// Already settled: a second resolution is rejected.
	_, err = svc.ResolvePendingRefund(ctx, order.ID, RefundResolutionFailed, "", "admin:7")
	require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))
}

func TestResolvePendingRefundSucceededDeductionFailureKeepsPending(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "resolve-deduct-fail")
	setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"balance","balanceToDeduct":30,"deductionRollbackOK":true,"gatewayAmount":100}`)
	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDErr: errors.New("user lookup failed")}}

	result, err := svc.ResolvePendingRefund(ctx, order.ID, RefundResolutionSucceeded, "", "")
	require.Nil(t, result)
	require.ErrorContains(t, err, "user lookup failed")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.Zero(t, countManualResolvedAuditForTest(t, ctx, svc, order.ID))
}

func TestResolvePendingRefundFailedThenRetryReusesGatewayRequest(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "resolve-failed")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetRefundAmount(60).Save(ctx)
	require.NoError(t, err)
	setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"none","deductionRollbackOK":true,"refundAmount":60,"gatewayBaseAmount":60,"gatewayAmount":57,"refundFeeRate":5,"refundFeeAmount":3}`)
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: &mockUserRepo{}}

	result, err := svc.ResolvePendingRefund(ctx, order.ID, RefundResolutionFailed, "gateway console shows closed", "")
	require.NoError(t, err)
	require.False(t, result.Success)
	require.False(t, result.RefundPending)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)
	require.NotNil(t, reloaded.FailedReason)
	require.Contains(t, *reloaded.FailedReason, "manually marked failed by admin: gateway console shows closed")
	require.Equal(t, 1, countManualResolvedAuditForTest(t, ctx, svc, order.ID))

	// The retry resubmits the identical gateway request (same amount => same
	// deterministic refund number), so the gateway dedups it if the original
	// refund landed after all.
	prov := &refundCaptureProviderTestDouble{resp: &payment.RefundResponse{RefundID: "rf_retry", Status: payment.ProviderStatusSuccess}}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	plan, early, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false)
	require.NoError(t, err)
	require.Nil(t, early)
	res, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Len(t, prov.requests, 1)
	require.Equal(t, "57.00", prov.requests[0].Amount)
	require.Equal(t, order.OutTradeNo, prov.requests[0].OrderID)

	reloaded, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPartiallyRefunded, reloaded.Status)
}

func TestResolvePendingRefundConcurrentChangeConflicts(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "resolve-conflict")
	svc := &PaymentService{entClient: client}

	// The status CAS inside the finalizers rejects an order that left
	// REFUND_PENDING after the initial read.
	o, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunded).Save(ctx)
	require.NoError(t, err)
	detail := svc.latestRefundPendingDetail(ctx, order.ID)
	_, err = svc.finalizeRefundSucceeded(ctx, o, detail, svc.refundFinalizePlan(ctx, o, detail))
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	_, err = svc.finalizeRefundFailed(ctx, o, errors.New("manual"))
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
}
