//go:build integration

package service

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type refundPendingDelayedProvider struct {
	refundPendingAcceptedProvider
	started chan payment.RefundQueryRequest
	release chan struct{}
	status  string
}

func (p *refundPendingDelayedProvider) QueryRefund(ctx context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	p.started <- req
	select {
	case <-p.release:
		return &payment.RefundResponse{RefundID: "old_refund", Status: p.status}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRefundPendingAtomicPostgres_DelayedOutcomeCannotSettleNewAttempt(t *testing.T) {
	for _, outcome := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusFailed} {
		t.Run(outcome, func(t *testing.T) {
			c, s, p := refundPendingPostgresFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err := c.User.UpdateOneID(p.Order.UserID).SetBalance(20).Save(ctx)
			require.NoError(t, err)
			p.Order, err = c.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(OrderStatusRefundPending).SetRefundAmount(10).SetPaymentTradeNo("pi_aba").Save(ctx)
			require.NoError(t, err)
			old, err := c.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(p.OrderID, 10)).SetAction("REFUND_PENDING_old").SetOperator("admin").SetDetail(`{"refundID":"old_refund","deductionType":"balance","balanceToDeduct":3,"deductionRollbackOK":false,"refundAmount":10,"gatewayBaseAmount":70,"gatewayAmount":66.5,"refundFeeRate":0.05,"refundFeeAmount":3.5}`).Save(ctx)
			require.NoError(t, err)
			provider := &refundPendingDelayedProvider{started: make(chan payment.RefundQueryRequest, 1), release: make(chan struct{}), status: outcome}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(provider.release) }) }
			defer release()
			originalFactory := createPaymentProviderFromInstance
			createPaymentProviderFromInstance = func(string, string, map[string]string) (payment.Provider, error) { return provider, nil }
			defer func() { createPaymentProviderFromInstance = originalFactory }()
			type queryResult struct {
				result *RefundResult
				err    error
			}
			completed := make(chan queryResult, 1)
			go func() { result, err := s.QueryAndFinalizeRefund(ctx, p.OrderID); completed <- queryResult{result, err} }()
			select {
			case request := <-provider.started:
				require.Equal(t, "old_refund", request.RefundID)
			case <-time.After(15 * time.Second):
				t.Fatal("old provider query never started")
			}
			// A real failed resolution restores the old outstanding debit 3.
			result, err := s.ResolvePendingRefund(ctx, p.OrderID, "failed", "verified old failure", "admin")
			require.NoError(t, err)
			require.False(t, result.Success)
			assertPendingPostgresState(t, c, p, OrderStatusRefundFailed, 23)
			retry, early, err := s.PrepareRefund(ctx, p.OrderID, 10, "retry same provider amount", true, true)
			require.NoError(t, err)
			require.Nil(t, early)
			result, err = s.ExecuteRefund(ctx, retry)
			require.NoError(t, err)
			require.True(t, result.RefundPending)
			require.Equal(t, 1, provider.calls)
			assertPendingPostgresState(t, c, p, OrderStatusRefundPending, 23)
			current, err := s.latestRefundPendingDetail(ctx, p.OrderID)
			require.NoError(t, err)
			require.True(t, current.DeductionRollbackOK)
			require.Equal(t, 10.0, current.BalanceToDeduct)
			latest, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_PENDING")).Order(paymentauditlog.ByID()).All(ctx)
			require.NoError(t, err)
			require.Len(t, latest, 2)
			require.NotEqual(t, old.ID, latest[1].ID)
			auditsBefore, err := c.PaymentAuditLog.Query().Count(ctx)
			require.NoError(t, err)
			release()
			select {
			case stale := <-completed:
				require.Nil(t, stale.result)
				require.Equal(t, "CONFLICT", infraerrors.Reason(stale.err))
			case <-time.After(15 * time.Second):
				t.Fatal("stale provider query did not finish")
			}
			assertPendingPostgresState(t, c, p, OrderStatusRefundPending, 23)
			auditsAfter, err := c.PaymentAuditLog.Query().Count(ctx)
			require.NoError(t, err)
			require.Equal(t, auditsBefore, auditsAfter, "a stale result must not commit terminal or financial audits")
			successCount, err := c.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("REFUND_SUCCESS")).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, successCount)
			// Current attempt remains resolvable and deducts only its actual 10 once.
			result, err = s.ResolvePendingRefund(ctx, p.OrderID, "succeeded", "verified current attempt", "admin")
			require.NoError(t, err)
			require.True(t, result.Success)
			assertPendingPostgresState(t, c, p, OrderStatusRefunded, 13)
		})
	}
}

func TestRefundPendingAtomicPostgres_FailedTerminalAuditRollsBackActualRestoration(t *testing.T) {
	for _, action := range []string{"REFUND_ROLLBACK_RECOVERED", "REFUND_FAILED"} {
		t.Run(action, func(t *testing.T) {
			c, s, p := refundPendingPostgresFixture(t)
			ctx := context.Background()
			_, err := c.User.UpdateOneID(p.Order.UserID).SetBalance(20).Save(ctx)
			require.NoError(t, err)
			_, err = c.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(OrderStatusRefundPending).Save(ctx)
			require.NoError(t, err)
			_, err = c.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(p.OrderID, 10)).SetAction("REFUND_PENDING_old").SetOperator("admin").SetDetail(`{"deductionType":"balance","balanceToDeduct":3,"deductionRollbackOK":false,"refundAmount":10}`).Save(ctx)
			require.NoError(t, err)
			_, err = c.ExecContext(ctx, `CREATE FUNCTION reject_failed_refund_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE '`+action+`%' THEN RAISE EXCEPTION 'terminal audit unavailable'; END IF; RETURN NEW; END $$`)
			require.NoError(t, err)
			_, err = c.ExecContext(ctx, `CREATE TRIGGER reject_failed_refund_audit BEFORE INSERT ON payment_audit_logs FOR EACH ROW EXECUTE FUNCTION reject_failed_refund_audit()`)
			require.NoError(t, err)
			result, err := s.ResolvePendingRefund(ctx, p.OrderID, "failed", "verified", "admin")
			require.ErrorContains(t, err, "terminal audit unavailable")
			require.Nil(t, result)
			assertPendingPostgresState(t, c, p, OrderStatusRefundPending, 20)
			count, err := c.PaymentAuditLog.Query().Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count, "restoration, temporary claim and all financial audits must roll back")
			_, err = c.ExecContext(ctx, `DROP TRIGGER reject_failed_refund_audit ON payment_audit_logs`)
			require.NoError(t, err)
			result, err = s.ResolvePendingRefund(ctx, p.OrderID, "failed", "retry verified result", "admin")
			require.NoError(t, err)
			require.False(t, result.Success)
			assertPendingPostgresState(t, c, p, OrderStatusRefundFailed, 23)
			_, err = s.ResolvePendingRefund(ctx, p.OrderID, "failed", "repeat", "admin")
			require.Error(t, err)
			assertPendingPostgresState(t, c, p, OrderStatusRefundFailed, 23)
		})
	}
}
