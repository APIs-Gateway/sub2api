//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// This private service transaction needs a real PostgreSQL statement failure:
// unlike a mock error, it aborts the transaction until ROLLBACK TO SAVEPOINT.
// Every fixture has its own container/schema and cannot pollute repository data.
func refundPendingPostgresFixture(t *testing.T) (*dbent.Client, *PaymentService, *RefundPlan) {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("refund_pending"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, client.Schema.Create(ctx))
	user, err := client.User.Create().SetEmail("refund@example.com").SetPasswordHash("hash").SetBalance(0).Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(user.Email).SetUserName("refund").SetAmount(10).SetPayAmount(70).SetFeeRate(0).SetRechargeCode("refund").SetOutTradeNo("refund").SetPaymentType(payment.TypeStripe).SetPaymentTradeNo("").SetOrderType(payment.OrderTypeBalance).SetStatus(OrderStatusRefunding).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("api.example.com").Save(ctx)
	require.NoError(t, err)
	inst, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeStripe).SetName("refund provider").SetConfig("{}").SetSupportedTypes("stripe").SetEnabled(true).SetRefundEnabled(true).Save(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).Save(ctx)
	require.NoError(t, err)
	repo := &refundPendingPostgresWallet{client: client}
	svc := &PaymentService{entClient: client, userRepo: repo, loadBalancer: &refundPendingPostgresLoadBalancer{}}
	plan := &RefundPlan{OrderID: order.ID, Order: order, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 10, RefundAmount: 10, GatewayBaseAmount: 70, GatewayAmount: 66.5, RefundFeeRate: .05, RefundFeeAmount: 3.5}
	return client, svc, plan
}

type refundPendingPostgresLoadBalancer struct{ payment.LoadBalancer }

func (r *refundPendingPostgresLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	return map[string]string{}, nil
}

type refundPendingAcceptedProvider struct {
	payment.Provider
	calls int
}

func (p *refundPendingAcceptedProvider) ProviderKey() string { return string(payment.TypeStripe) }
func (p *refundPendingAcceptedProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	p.calls++
	return &payment.RefundResponse{RefundID: "rf_pending", Status: payment.ProviderStatusPending}, nil
}

type refundPendingPostgresWallet struct {
	UserRepository
	client *dbent.Client
}

func (r *refundPendingPostgresWallet) ambient(ctx context.Context) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.client
}
func (r *refundPendingPostgresWallet) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	_, err := r.ambient(ctx).User.Update().Where(dbuser.IDEQ(id)).AddBalance(amount).Save(ctx)
	return err
}
func (r *refundPendingPostgresWallet) GetByID(ctx context.Context, id int64) (*User, error) {
	u, err := r.ambient(ctx).User.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &User{ID: u.ID, Balance: u.Balance}, nil
}
func (r *refundPendingPostgresWallet) DeductRefundBalance(ctx context.Context, id int64, amount float64, partial bool) (float64, error) {
	u, err := r.GetByID(ctx, id)
	if err != nil {
		return 0, err
	}
	actual := math.Min(math.Max(u.Balance, 0), amount)
	if !partial && actual < amount {
		return 0, ErrRefundBalanceInsufficient
	}
	return actual, r.UpdateBalance(ctx, id, -actual)
}

func assertPendingPostgresState(t *testing.T, c *dbent.Client, p *RefundPlan, status string, balance float64) {
	t.Helper()
	o, err := c.PaymentOrder.Get(context.Background(), p.OrderID)
	require.NoError(t, err)
	require.Equal(t, status, o.Status)
	u, err := c.User.Get(context.Background(), p.Order.UserID)
	require.NoError(t, err)
	require.Equal(t, balance, u.Balance)
}

func TestRefundPendingAtomicPostgres_SnapshotFailureRollsBackCompensationAndStatus(t *testing.T) {
	c, s, p := refundPendingPostgresFixture(t)
	ctx := context.Background()
	_, err := c.ExecContext(ctx, `CREATE FUNCTION reject_pending_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'REFUND_PENDING%' THEN RAISE EXCEPTION 'snapshot unavailable'; END IF; RETURN NEW; END $$`)
	require.NoError(t, err)
	_, err = c.ExecContext(ctx, `CREATE TRIGGER reject_pending_snapshot BEFORE INSERT ON payment_audit_logs FOR EACH ROW EXECUTE FUNCTION reject_pending_snapshot()`)
	require.NoError(t, err)
	_, err = c.User.UpdateOneID(p.Order.UserID).SetBalance(10).Save(ctx)
	require.NoError(t, err)
	p.Order, err = c.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(OrderStatusCompleted).SetPaymentTradeNo("pi_pending").Save(ctx)
	require.NoError(t, err)
	provider := &refundPendingAcceptedProvider{}
	originalFactory := createPaymentProviderFromInstance
	createPaymentProviderFromInstance = func(string, string, map[string]string) (payment.Provider, error) { return provider, nil }
	t.Cleanup(func() { createPaymentProviderFromInstance = originalFactory })
	result, err := s.ExecuteRefund(ctx, p)
	require.Equal(t, 1, provider.calls, "provider accepted exactly one refund before the storage failure")
	require.ErrorContains(t, err, "do not resubmit")
	require.Nil(t, result)
	assertPendingPostgresState(t, c, p, OrderStatusRefunding, 0)
	require.Equal(t, 10.0, p.BalanceToDeduct, "failed commit must not mutate the caller's actual debit")
	count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.ActionHasPrefix("REFUND_PENDING")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	result, err = s.ExecuteRefund(ctx, p)
	require.Equal(t, 1, provider.calls, "a failed pending persistence is not a safe provider retry")
	require.Error(t, err)
	require.Nil(t, result)
	assertPendingPostgresState(t, c, p, OrderStatusRefunding, 0)
	_, err = c.ExecContext(ctx, `DROP TRIGGER reject_pending_snapshot ON payment_audit_logs`)
	require.NoError(t, err)
	// Explicit recovery of the same accepted response, not another provider call.
	result, err = s.finishRefund(ctx, p, &payment.RefundResponse{RefundID: "rf_pending", Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	require.True(t, result.RefundPending)
	assertPendingPostgresState(t, c, p, OrderStatusRefundPending, 10)
	require.Zero(t, p.BalanceToDeduct)
	d, err := s.latestRefundPendingDetail(ctx, p.OrderID)
	require.NoError(t, err)
	require.True(t, d.DeductionRollbackOK)
	require.Equal(t, 10.0, d.BalanceToDeduct)
	require.Equal(t, "rf_pending", d.RefundID)
	result, err = s.ResolvePendingRefund(ctx, p.OrderID, "succeeded", "verified same response", "admin")
	require.NoError(t, err)
	require.True(t, result.Success)
	assertPendingPostgresState(t, c, p, OrderStatusRefunded, 0)
	_, err = s.ResolvePendingRefund(ctx, p.OrderID, "succeeded", "repeat", "admin")
	require.Error(t, err)
	assertPendingPostgresState(t, c, p, OrderStatusRefunded, 0)
}

func TestRefundPendingAtomicPostgres_CompensationSQLFailureIsSavedWithoutRedebit(t *testing.T) {
	c, s, p := refundPendingPostgresFixture(t)
	ctx := context.Background()
	_, err := c.ExecContext(ctx, `CREATE FUNCTION reject_pending_restore() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.balance > OLD.balance THEN RAISE EXCEPTION 'restore unavailable'; END IF; RETURN NEW; END $$`)
	require.NoError(t, err)
	_, err = c.ExecContext(ctx, `CREATE TRIGGER reject_pending_restore BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION reject_pending_restore()`)
	require.NoError(t, err)
	result, err := s.finishRefund(ctx, p, &payment.RefundResponse{RefundID: "rf_pending", Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	require.True(t, result.RefundPending)
	require.Contains(t, result.Warning, "rollback failed")
	assertPendingPostgresState(t, c, p, OrderStatusRefundPending, 0)
	require.Equal(t, 10.0, p.BalanceToDeduct)
	d, err := s.latestRefundPendingDetail(ctx, p.OrderID)
	require.NoError(t, err)
	require.False(t, d.DeductionRollbackOK)
	outstanding, err := s.hasOutstandingRefundRollbackFailure(ctx, p.OrderID)
	require.NoError(t, err)
	require.True(t, outstanding)
	logs, err := c.PaymentAuditLog.Query().Where(paymentauditlog.ActionHasPrefix("REFUND_ROLLBACK_FAILED")).All(ctx)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	result, err = s.ResolvePendingRefund(ctx, p.OrderID, "succeeded", "provider verified", "admin")
	require.NoError(t, err)
	require.True(t, result.Success)
	assertPendingPostgresState(t, c, p, OrderStatusRefunded, 0)
}

func TestRefundPendingAtomicPostgres_NoneChoicePersistsWithoutWalletMovement(t *testing.T) {
	c, s, p := refundPendingPostgresFixture(t)
	ctx := context.Background()
	p.DeductionType = payment.DeductionTypeNone
	p.BalanceToDeduct = 0
	result, err := s.finishRefund(ctx, p, &payment.RefundResponse{RefundID: "rf_none", Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	require.True(t, result.RefundPending)
	assertPendingPostgresState(t, c, p, OrderStatusRefundPending, 0)
	_, err = c.User.UpdateOneID(p.Order.UserID).SetBalance(23).Save(ctx)
	require.NoError(t, err)
	result, err = s.ResolvePendingRefund(ctx, p.OrderID, "succeeded", "verified", "admin")
	require.NoError(t, err)
	require.True(t, result.Success)
	assertPendingPostgresState(t, c, p, OrderStatusRefunded, 23)
}

func TestRefundPendingAtomicPostgres_HistoryFailureCannotClaimOrSettle(t *testing.T) {
	c, s, p := refundPendingPostgresFixture(t)
	ctx := context.Background()
	_, err := c.User.UpdateOneID(p.Order.UserID).SetBalance(23).Save(ctx)
	require.NoError(t, err)
	_, err = c.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	_, err = c.ExecContext(ctx, `ALTER TABLE payment_audit_logs RENAME TO unavailable_refund_history`)
	require.NoError(t, err)
	result, err := s.ExecuteRefund(ctx, p)
	require.ErrorContains(t, err, "read refund rollback history")
	require.Nil(t, result)
	assertPendingPostgresState(t, c, p, OrderStatusCompleted, 23)
	prepared, result, err := s.PrepareRefund(ctx, p.OrderID, 10, "verified", false, true)
	require.ErrorContains(t, err, "read pending refund snapshot")
	require.Nil(t, prepared)
	require.Nil(t, result)
	_, err = c.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(OrderStatusRefundPending).SetRefundAmount(10).Save(ctx)
	require.NoError(t, err)
	result, err = s.ResolvePendingRefund(ctx, p.OrderID, "succeeded", "verified", "admin")
	require.ErrorContains(t, err, "read pending refund snapshot")
	require.Nil(t, result)
	result, err = s.QueryAndFinalizeRefund(ctx, p.OrderID)
	require.ErrorContains(t, err, "read pending refund snapshot")
	require.Nil(t, result)
	assertPendingPostgresState(t, c, p, OrderStatusRefundPending, 23)
	_, err = c.ExecContext(ctx, `ALTER TABLE unavailable_refund_history RENAME TO payment_audit_logs`)
	require.NoError(t, err)
	for i, body := range []string{`{broken`, `null`, `[]`, `{"deductionType":"surprise"}`, `{"balanceToDeduct":"bad"}`, `{"deductionType":"none","deductionType":"balance"}`, `{"deductionRollbackOK":null}`, `{"deductionType":"balance","balanceToDeduct":-10}`} {
		t.Run(fmt.Sprintf("malformed_%d", i), func(t *testing.T) {
			_, err := c.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(p.OrderID, 10)).SetAction(fmt.Sprintf("REFUND_PENDING_%d", i)).SetOperator("admin").SetDetail(body).Save(ctx)
			require.NoError(t, err)
			result, err := s.ResolvePendingRefund(ctx, p.OrderID, "succeeded", "verified", "admin")
			require.Error(t, err)
			require.Nil(t, result)
			result, err = s.QueryAndFinalizeRefund(ctx, p.OrderID)
			require.Error(t, err)
			require.Nil(t, result)
			assertPendingPostgresState(t, c, p, OrderStatusRefundPending, 23)
		})
	}
	// Proven missing history preserves the existing legacy reconstruction.
	_, err = c.PaymentAuditLog.Delete().Exec(ctx)
	require.NoError(t, err)
	d, err := s.latestRefundPendingDetail(ctx, p.OrderID)
	require.NoError(t, err)
	require.False(t, d.found)
	require.True(t, d.DeductionRollbackOK)
	result, err = s.ResolvePendingRefund(ctx, p.OrderID, "succeeded", "verified legacy missing snapshot", "admin")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 10.0, result.BalanceDeducted)
	assertPendingPostgresState(t, c, p, OrderStatusRefunded, 13)
}
