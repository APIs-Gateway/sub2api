//go:build unit

package service

import (
	"context"
	stdsql "database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// Keep real SQLite transactions and rows beneath the injected storage failure.
// Assertions read through the original client after the service rolls back.
type refundStorageFaultDriver struct {
	dialect.Driver
	phase   string
	reached int
}

func (d *refundStorageFaultDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	if d.phase == "begin" {
		d.reached++
		return nil, errors.New("refund storage unavailable")
	}
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &refundStorageFaultTx{Tx: tx, fault: d}, nil
}

type refundStorageFaultTx struct {
	dialect.Tx
	fault *refundStorageFaultDriver
}

func (t *refundStorageFaultTx) fail(query string) error {
	q := strings.ToUpper(query)
	phase := t.fault.phase
	match := (phase == "user update" && strings.HasPrefix(q, "UPDATE") && strings.Contains(q, `"USERS"`)) ||
		(phase == "user query" && strings.HasPrefix(q, "SELECT") && strings.Contains(q, `"USERS"`)) ||
		(phase == "order query" && strings.HasPrefix(q, "SELECT") && strings.Contains(q, `"PAYMENT_ORDERS"`)) ||
		(phase == "order update" && strings.HasPrefix(q, "UPDATE") && strings.Contains(q, `"PAYMENT_ORDERS"`)) ||
		(phase == "savepoint" && strings.HasPrefix(q, "SAVEPOINT ")) ||
		(phase == "recover" && strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT ")) ||
		(phase == "release" && strings.HasPrefix(q, "RELEASE SAVEPOINT "))
	if match {
		t.fault.reached++
		return errors.New("refund storage unavailable")
	}
	return nil
}

func (t *refundStorageFaultTx) Exec(ctx context.Context, query string, args, value any) error {
	if err := t.fail(query); err != nil {
		return err
	}
	return t.Tx.Exec(ctx, query, args, value)
}

func (t *refundStorageFaultTx) Query(ctx context.Context, query string, args, value any) error {
	if err := t.fail(query); err != nil {
		return err
	}
	return t.Tx.Query(ctx, query, args, value)
}

// Ent forwards raw savepoint statements through the optional ExecContext
// extension. Preserve it as well as dialect.Tx so the intended fault executes.
func (t *refundStorageFaultTx) ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error) {
	if err := t.fail(query); err != nil {
		return nil, err
	}
	executor, ok := t.Tx.(interface {
		ExecContext(context.Context, string, ...any) (stdsql.Result, error)
	})
	if !ok {
		return nil, errors.New("fixture transaction does not support ExecContext")
	}
	return executor.ExecContext(ctx, query, args...)
}

func (t *refundStorageFaultTx) Commit() error {
	if t.fault.phase == "commit" {
		t.fault.reached++
		_ = t.Tx.Rollback()
		return errors.New("refund storage unavailable")
	}
	return t.Tx.Commit()
}

func TestRefundPendingStorageFailuresKeepDeductionAndForbidResubmission(t *testing.T) {
	for _, phase := range []string{"begin", "user update", "user query", "order query", "savepoint", "recover", "release", "order update", "commit"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "fault")
			order, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
			require.NoError(t, err)
			_, err = client.User.UpdateOneID(order.UserID).SetBalance(23).Save(ctx)
			require.NoError(t, err)
			fault := &refundStorageFaultDriver{Driver: client.Driver(), phase: phase}
			wrapped := dbent.NewClient(dbent.Driver(fault))
			updates := 0
			repo := &mockUserRepo{updateBalanceFn: func(ctx context.Context, id int64, amount float64) error {
				updates++
				if phase == "recover" {
					return errors.New("compensation unavailable")
				}
				tx := dbent.TxFromContext(ctx)
				require.NotNil(t, tx)
				_, err := tx.Client().User.UpdateOneID(id).AddBalance(amount).Save(ctx)
				return err
			}}
			svc := &PaymentService{entClient: wrapped, userRepo: repo}
			plan := &RefundPlan{OrderID: order.ID, Order: order, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 10, RefundAmount: 10, GatewayAmount: 10}
			result, err := svc.markRefundPending(ctx, plan, &payment.RefundResponse{RefundID: "already_accepted", Status: payment.ProviderStatusPending})
			require.ErrorContains(t, err, "do not resubmit")
			require.Nil(t, result)
			require.Positive(t, fault.reached, "the intended storage failure must execute")
			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefunding, reloaded.Status)
			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.Equal(t, 23.0, user.Balance)
			require.Equal(t, 10.0, plan.BalanceToDeduct)
			require.Zero(t, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_SUCCESS"))
			if phase == "release" || phase == "order update" || phase == "commit" {
				require.Equal(t, 1, updates, "rollback must undo actual successful compensation")
			}
		})
	}
}

func TestRefundConfirmedSettlementStorageFailureCannotCommitFinancialState(t *testing.T) {
	for _, phase := range []string{"begin", "user update", "user query", "commit"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "confirmed")
			_, err := client.User.UpdateOneID(order.UserID).SetBalance(23).Save(ctx)
			require.NoError(t, err)
			setPendingRefundSnapshotForTest(t, ctx, client, order.ID, `{"deductionType":"balance","balanceToDeduct":10,"deductionRollbackOK":true}`)
			fault := &refundStorageFaultDriver{Driver: client.Driver(), phase: phase}
			debits := 0
			svc := &PaymentService{entClient: dbent.NewClient(dbent.Driver(fault)), userRepo: &mockUserRepo{
				getByIDUser: &User{ID: order.UserID, Balance: 23},
				deductBalanceFn: func(ctx context.Context, id int64, amount float64) error {
					debits++
					tx := dbent.TxFromContext(ctx)
					require.NotNil(t, tx)
					_, err := tx.Client().User.UpdateOneID(id).AddBalance(-amount).Save(ctx)
					return err
				},
			}}
			result, err := svc.ResolvePendingRefund(ctx, order.ID, "succeeded", "verified", "admin")
			require.ErrorContains(t, err, "refund storage unavailable")
			require.Nil(t, result)
			require.Positive(t, fault.reached)
			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundPending, reloaded.Status)
			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.Equal(t, 23.0, user.Balance)
			require.Zero(t, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_SUCCESS"))
			if phase == "commit" {
				require.Equal(t, 1, debits, "failed commit must roll back an actual wallet deduction")
			} else {
				require.Zero(t, debits)
			}
		})
	}
}

func TestRefundSettlementDeletedUserCannotBeClaimed(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "deleted")
	_, err := client.User.UpdateOneID(order.UserID).SetBalance(23).SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	result, err := svc.ResolvePendingRefund(ctx, order.ID, "succeeded", "verified", "admin")
	require.ErrorIs(t, err, ErrUserNotFound)
	require.Nil(t, result)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 23.0, user.Balance)
	require.Zero(t, countRefundAuditForTest(t, ctx, client, order.ID, "REFUND_SUCCESS"))
}

func TestRefundMalformedSnapshotCannotSettlePositiveWallet(t *testing.T) {
	for _, body := range []string{`{`, `{"balanceToDeduct":`, `{} {}`, `{"subDaysToDeduct":-1}`, `{"subscriptionID":-1}`} {
		t.Run(body, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "malformed")
			_, err := client.User.UpdateOneID(order.UserID).SetBalance(23).Save(ctx)
			require.NoError(t, err)
			setPendingRefundSnapshotForTest(t, ctx, client, order.ID, body)
			svc := &PaymentService{entClient: client}
			result, err := svc.ResolvePendingRefund(ctx, order.ID, "succeeded", "verified", "admin")
			require.Error(t, err)
			require.Nil(t, result)
			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundPending, reloaded.Status)
			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.Equal(t, 23.0, user.Balance)
		})
	}
}
