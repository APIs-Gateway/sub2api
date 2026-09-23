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

// Multi-attempt REFUND_PENDING money safety (PR #1175 R1 review): every
// pending attempt keeps its own snapshot, settlement replays the newest one,
// and a failed refund restores a pre-deduction whose rollback had failed.

type refundFlowProviderTestDouble struct {
	refundProviderTestDouble
	refundResp *payment.RefundResponse
	queryResp  *payment.RefundResponse
	refunds    []payment.RefundRequest
}

func (p *refundFlowProviderTestDouble) Refund(_ context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	p.refunds = append(p.refunds, req)
	return p.refundResp, nil
}

func (p *refundFlowProviderTestDouble) QueryRefund(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	return p.queryResp, nil
}

// refundLedger records wallet movements made through the user repo.
type refundLedger struct {
	deducted     float64
	restored     float64
	failRestore  func(call int) bool
	restoreCalls int
}

func (l *refundLedger) repo() *mockUserRepo {
	return &mockUserRepo{
		getByIDUser: &User{Balance: 500},
		deductBalanceFn: func(_ context.Context, _ int64, amount float64) error {
			l.deducted += amount
			return nil
		},
		updateBalanceFn: func(_ context.Context, _ int64, amount float64) error {
			l.restoreCalls++
			if l.failRestore != nil && l.failRestore(l.restoreCalls) {
				return errors.New("wallet db down")
			}
			l.restored += amount
			return nil
		},
	}
}

func (l *refundLedger) net() float64 { return l.restored - l.deducted }

func newRefundAttemptOrderForTest(t *testing.T, ctx context.Context, client *dbent.Client, suffix string) *dbent.PaymentOrder {
	t.Helper()
	order := createPendingRefundOrderForTest(t, ctx, client, suffix)
	_, err := client.PaymentAuditLog.Delete().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10))).Exec(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	return order
}

func runRefundAttemptForTest(t *testing.T, ctx context.Context, svc *PaymentService, orderID int64, deductionType string, balance float64) *RefundResult {
	t.Helper()
	order, err := svc.entClient.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	result, err := svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID:         orderID,
		Order:           order,
		RefundAmount:    100,
		GatewayAmount:   100,
		Reason:          "attempt",
		DeductionType:   deductionType,
		BalanceToDeduct: balance,
	})
	require.NoError(t, err)
	return result
}

func requireOrderStatusForTest(t *testing.T, ctx context.Context, client *dbent.Client, orderID int64, want string) {
	t.Helper()
	o, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, want, o.Status)
}

// (i) The deduct option changes between attempts: settlement must replay the
// latest attempt's snapshot, not the first one.
func TestRefundSettlementUsesLatestAttemptSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name             string
		first, second    string
		wantNet          float64
		wantLatestDeduct string
	}{
		{name: "deduct then no deduct", first: payment.DeductionTypeBalance, second: payment.DeductionTypeNone, wantNet: 0, wantLatestDeduct: payment.DeductionTypeNone},
		{name: "no deduct then deduct", first: payment.DeductionTypeNone, second: payment.DeductionTypeBalance, wantNet: -30, wantLatestDeduct: payment.DeductionTypeBalance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := newRefundAttemptOrderForTest(t, ctx, client, "latest-"+tc.first+"-"+tc.second)
			ledger := &refundLedger{}
			svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: ledger.repo()}
			prov := &refundFlowProviderTestDouble{refundResp: &payment.RefundResponse{RefundID: "rf", Status: payment.ProviderStatusPending}}
			restore := replacePaymentProviderFactoryForTest(t, prov)
			defer restore()

			balance := func(dt string) float64 {
				if dt == payment.DeductionTypeBalance {
					return 30
				}
				return 0
			}

			require.True(t, runRefundAttemptForTest(t, ctx, svc, order.ID, tc.first, balance(tc.first)).RefundPending)
			prov.queryResp = &payment.RefundResponse{Status: payment.ProviderStatusFailed}
			_, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefundFailed)

			require.True(t, runRefundAttemptForTest(t, ctx, svc, order.ID, tc.second, balance(tc.second)).RefundPending)
			require.Equal(t, 2, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_PENDING"), "each attempt keeps its own snapshot")
			require.Equal(t, tc.wantLatestDeduct, svc.latestRefundPendingDetail(ctx, order.ID).DeductionType)
			require.Equal(t, 1, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_FAILED"), "first attempt failure is audited")

			prov.queryResp = &payment.RefundResponse{Status: payment.ProviderStatusSuccess}
			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, tc.wantNet, ledger.net())
			requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefunded)
		})
	}
}

// (ii) Attempt 1 rolls back fine, attempt 2's rollback fails (deduction still
// in place): settlement must not deduct a second time.
func TestRefundSettlementNoDoubleDeductionWhenLatestRollbackFailed(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := newRefundAttemptOrderForTest(t, ctx, client, "latest-rollback-failed")
	ledger := &refundLedger{failRestore: func(call int) bool { return call == 2 }}
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: ledger.repo()}
	prov := &refundFlowProviderTestDouble{refundResp: &payment.RefundResponse{RefundID: "rf", Status: payment.ProviderStatusPending}}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()

	require.True(t, runRefundAttemptForTest(t, ctx, svc, order.ID, payment.DeductionTypeBalance, 30).RefundPending)
	require.Equal(t, 0.0, ledger.net())
	prov.queryResp = &payment.RefundResponse{Status: payment.ProviderStatusFailed}
	_, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)

	result := runRefundAttemptForTest(t, ctx, svc, order.ID, payment.DeductionTypeBalance, 30)
	require.True(t, result.RefundPending)
	require.Contains(t, result.Warning, "rollback failed")
	require.Equal(t, -30.0, ledger.net(), "attempt 2 deduction is still in place")
	require.False(t, svc.latestRefundPendingDetail(ctx, order.ID).DeductionRollbackOK)

	prov.queryResp = &payment.RefundResponse{Status: payment.ProviderStatusSuccess}
	settled, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, settled.Success)
	require.Equal(t, -30.0, ledger.net(), "settlement must not deduct again")
	require.Equal(t, 60.0, ledger.deducted)
	requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefunded)
}

// (iii) A failed refund restores an attempt's un-rolled-back deduction before
// the order becomes REFUND_FAILED (or stays pending if that restore fails),
// and the following retry deducts exactly once.
func TestRefundFailedRestoresOutstandingDeductionAndRetryDeductsOnce(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := newRefundAttemptOrderForTest(t, ctx, client, "failed-restores")
	walletDown := true
	ledger := &refundLedger{failRestore: func(int) bool { return walletDown }}
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: ledger.repo()}
	prov := &refundFlowProviderTestDouble{refundResp: &payment.RefundResponse{RefundID: "rf", Status: payment.ProviderStatusPending}}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()

	require.True(t, runRefundAttemptForTest(t, ctx, svc, order.ID, payment.DeductionTypeBalance, 30).RefundPending)
	require.Equal(t, -30.0, ledger.net())
	require.True(t, svc.hasOutstandingRefundRollbackFailure(ctx, order.ID))

	// Restore still failing: neither the query nor a manual "failed" may mark
	// the order failed while the user is still debited.
	prov.queryResp = &payment.RefundResponse{Status: payment.ProviderStatusFailed}
	_, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Equal(t, "REFUND_ROLLBACK_FAILED", infraerrors.Reason(err))
	requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefundPending)
	_, err = svc.ResolvePendingRefund(ctx, order.ID, RefundResolutionFailed, "console shows closed", "admin:3")
	require.Equal(t, "REFUND_ROLLBACK_FAILED", infraerrors.Reason(err))
	requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefundPending)
	require.Equal(t, 2, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_FAIL_BLOCKED_ROLLBACK_FAILED"))
	require.Zero(t, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_MANUAL_RESOLVED"))
	require.Equal(t, -30.0, ledger.net())

	// Wallet back: the failure path restores the deduction first.
	walletDown = false
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefundFailed)
	require.Equal(t, 0.0, ledger.net())
	require.Equal(t, 1, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_ROLLBACK_RECOVERED"))
	require.False(t, svc.hasOutstandingRefundRollbackFailure(ctx, order.ID))

	// Retry succeeds at the gateway: exactly one deduction in total.
	prov.refundResp = &payment.RefundResponse{RefundID: "rf2", Status: payment.ProviderStatusSuccess}
	retry := runRefundAttemptForTest(t, ctx, svc, order.ID, payment.DeductionTypeBalance, 30)
	require.True(t, retry.Success)
	require.Equal(t, -30.0, ledger.net())
	requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefunded)
}

func TestRefundFailedManualResolveRestoresOutstandingDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "manual-restores")
	setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"balance","balanceToDeduct":30,"deductionRollbackOK":false,"gatewayAmount":100}`)
	ledger := &refundLedger{}
	svc := &PaymentService{entClient: client, userRepo: ledger.repo()}

	_, err := svc.ResolvePendingRefund(ctx, order.ID, RefundResolutionFailed, "", "admin:4")
	require.NoError(t, err)
	require.Equal(t, 30.0, ledger.restored)
	requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefundFailed)
	require.Equal(t, 1, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_MANUAL_RESOLVED"))
}

func TestRefundFailedSubscriptionRestoreWithoutServiceKeepsPending(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "sub-restore-nosvc")
	setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"subscription","subscriptionID":90,"subDaysToDeduct":5,"subDaysToRestore":6,"subExpireDayToRestore":123,"deductionRollbackOK":false,"gatewayAmount":100}`)
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusFailed}})
	defer restore()

	detail := svc.latestRefundPendingDetail(ctx, order.ID)
	rb := refundRollbackPlanFromSnapshot(order, detail)
	require.Equal(t, 6, rb.SubDaysToRestore)
	require.Equal(t, 123, rb.SubExpireDayToRestore)
	require.Equal(t, int64(90), rb.SubscriptionID)

	_, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Equal(t, "REFUND_ROLLBACK_FAILED", infraerrors.Reason(err))
	requireOrderStatusForTest(t, ctx, client, order.ID, OrderStatusRefundPending)
	require.True(t, svc.hasOutstandingRefundRollbackFailure(ctx, order.ID))
}

func TestMarkRefundPendingSnapshotsRestoreTargets(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := newRefundAttemptOrderForTest(t, ctx, client, "snapshot-restore")
	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{}}
	_, err := svc.markRefundPending(ctx, &RefundPlan{
		OrderID:                    order.ID,
		Order:                      order,
		RefundAmount:               100,
		GatewayAmount:              100,
		DeductionType:              payment.DeductionTypeSubscription,
		SubDaysToRestore:           8,
		SubExpireDayToRestore:      456,
		SubTodayRemainingToRestore: 1.5,
		SubTodayDayToRestore:       455,
	}, &payment.RefundResponse{Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	d := svc.latestRefundPendingDetail(ctx, order.ID)
	require.Equal(t, 8, d.SubDaysToRestore)
	require.Equal(t, 456, d.SubExpireDayToRestore)
	require.Equal(t, 1.5, d.SubTodayRemainingToRestore)
	require.Equal(t, 455, d.SubTodayDayToRestore)
}
