//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func refundAtomicFixture(t *testing.T, balance float64, status string) (*dbent.Client, *userRepository, *service.PaymentService, *dbent.User, *dbent.PaymentOrder) {
	t.Helper()
	client := inflightTestEntClient(t)
	db := inflightTestDB(t)
	repo := newUserRepositoryWithSQL(client, db)
	ctx := context.Background()
	user, err := client.User.Create().SetEmail("refund-atomic-" + uuid.NewString() + "@example.com").SetPasswordHash("hash").SetBalance(balance).Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(user.Email).SetUserName("refund fixture").SetAmount(10).SetPayAmount(70).SetFeeRate(0).
		SetRechargeCode(uuid.NewString()).SetOutTradeNo(uuid.NewString()).SetPaymentType(payment.TypeStripe).SetPaymentTradeNo("").SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).SetRefundAmount(10).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("api.example.com").Save(ctx)
	require.NoError(t, err)
	if status == service.OrderStatusRefundPending {
		detail := `{"deductionType":"balance","balanceToDeduct":10,"deductionRollbackOK":true,"refundAmount":10,"gatewayBaseAmount":70,"gatewayAmount":66.5,"refundFeeRate":0.05,"refundFeeAmount":3.5}`
		_, err = client.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(order.ID, 10)).SetAction("REFUND_PENDING_1").SetOperator("admin").SetDetail(detail).Save(ctx)
		require.NoError(t, err)
	}
	pay := service.NewPaymentService(client, nil, nil, nil, nil, nil, repo, nil, nil)
	return client, repo, pay, user, order
}

func TestRefundAtomicPostgres_ConcurrentUsageUsesActualAvailableDebit(t *testing.T) {
	client, repo, _, user, _ := refundAtomicFixture(t, 10, service.OrderStatusCompleted)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	client.User.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, mutation dbent.Mutation) (dbent.Value, error) {
			if m, ok := mutation.(*dbent.UserMutation); ok {
				if _, set := m.Balance(); set && paused.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-release:
					case <-time.After(5 * time.Second):
						return nil, errors.New("wallet race fixture timed out")
					}
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	type outcome struct {
		amount float64
		err    error
	}
	done := make(chan outcome, 1)
	go func() { amount, err := repo.DeductRefundBalance(ctx, user.ID, 10, true); done <- outcome{amount, err} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refund did not reach its first conditional update")
	}
	// Ordinary usage changes the balance after the refund has read it but
	// before its CAS reaches PostgreSQL. The stale update must not debit 10.
	require.NoError(t, repo.DeductBalance(ctx, user.ID, 8))
	close(release)
	got := <-done
	require.NoError(t, got.err)
	require.Equal(t, 2.0, got.amount)
	updated, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Zero(t, updated.Balance)
	// The ordinary usage policy is still allowed to overdraw.
	require.NoError(t, repo.DeductBalance(ctx, user.ID, 1))
	updated, err = client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, -1.0, updated.Balance)
}

func TestRefundAtomicPostgres_ExecuteRechecksForceAndReturnsActual(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%t", force), func(t *testing.T) {
			client, repo, pay, user, order := refundAtomicFixture(t, 10, service.OrderStatusCompleted)
			ctx := context.Background()
			plan := &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 10, GatewayBaseAmount: 70, GatewayAmount: 66.5, RefundFeeRate: .05, RefundFeeAmount: 3.5, Force: force, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 10}
			require.NoError(t, repo.DeductBalance(ctx, user.ID, 8), "consume after the plan was prepared")
			result, err := pay.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			updated, err := client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			if force {
				require.True(t, result.Success)
				require.Equal(t, 2.0, result.BalanceDeducted)
				require.Zero(t, updated.Balance)
				require.Equal(t, 66.5, result.GatewayAmount)
				require.Equal(t, 3.5, result.RefundFeeAmount)
			} else {
				require.True(t, result.RequireForce)
				require.False(t, result.Success)
				require.Equal(t, 2.0, updated.Balance)
				reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
				require.NoError(t, err)
				require.Equal(t, service.OrderStatusCompleted, reloaded.Status)
			}
		})
	}
}

func TestRefundAtomicPostgres_PendingCompetitionSettlesOnce(t *testing.T) {
	client, _, pay, user, order := refundAtomicFixture(t, 6, service.OrderStatusRefundPending)
	ctx := context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "verified", "admin:1")
			if err == nil && result.Success {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int32(1), successes.Load())
	updated, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Zero(t, updated.Balance)
	logs, err := client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).All(ctx)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(logs[0].Detail), &detail))
	require.Equal(t, 6.0, detail["balanceDeducted"])
	require.Equal(t, 66.5, detail["gatewayAmount"])
	require.Equal(t, 3.5, detail["refundFeeAmount"])
	_, err = pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "retry", "admin:1")
	require.Error(t, err)
	updated, err = client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Zero(t, updated.Balance)
}

func TestRefundAtomicPostgres_PendingWriteFailureRollsBackAndRetries(t *testing.T) {
	for _, action := range []string{"REFUND_SUCCESS", "REFUND_FINALIZE_BALANCE_SHORTFALL", "order_terminal"} {
		t.Run(action, func(t *testing.T) {
			client, _, pay, user, order := refundAtomicFixture(t, 6, service.OrderStatusRefundPending)
			db := inflightTestDB(t)
			ctx := context.Background()
			// A real PostgreSQL trigger fails after the wallet mutation, so
			// the test observes database rollback rather than a mocked debit.
			body := "IF NEW.action = '" + action + "' THEN RAISE EXCEPTION 'refund injected audit failure'; END IF; RETURN NEW;"
			table, events := "payment_audit_logs", "INSERT"
			if action == "order_terminal" {
				body = "IF NEW.status IN ('REFUNDED','PARTIALLY_REFUNDED') THEN RAISE EXCEPTION 'refund injected order failure'; END IF; RETURN NEW;"
				table, events = "payment_orders", "UPDATE"
			}
			_, err := db.Exec(`CREATE FUNCTION refund_atomic_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN ` + body + ` END $$`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE TRIGGER refund_atomic_fail BEFORE ` + events + ` ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION refund_atomic_fail()`)
			require.NoError(t, err)
			result, err := pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "verified", "admin:1")
			require.Error(t, err)
			require.Nil(t, result)
			updated, err := client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			require.Equal(t, 6.0, updated.Balance)
			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, service.OrderStatusRefundPending, reloaded.Status)
			count, err := client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
			_, err = db.Exec(`DROP TRIGGER refund_atomic_fail ON ` + table)
			require.NoError(t, err)
			result, err = pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "retry after repair", "admin:1")
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, 6.0, result.BalanceDeducted)
			updated, err = client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			require.Zero(t, updated.Balance)
		})
	}
}

func TestRefundAtomicPostgres_PendingCardAndAuditAreOneTransaction(t *testing.T) {
	for _, mode := range []string{"close", "renew", "renew without days"} {
		t.Run(mode, func(t *testing.T) {
			client, repo, _, user, order := refundAtomicFixture(t, 6, service.OrderStatusRefundPending)
			db, ctx := inflightTestDB(t), context.Background()
			group := reclaimSeedGroup(t, client)
			card := reclaimSeedSub(t, client, user.ID, group.ID, 12)
			snapshot := map[string]any{"subscription": map[string]any{"intent": "purchase"}}
			if mode != "close" {
				snapshot["subscription"] = map[string]any{"intent": service.SubscriptionIntentRenew, "target_subscription_id": card.ID}
			}
			var err error
			order, err = client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscription).SetProviderSnapshot(snapshot).Save(ctx)
			require.NoError(t, err)
			days := 3
			if mode == "renew without days" {
				days = 0
			}
			detail, err := json.Marshal(map[string]any{"deductionType": payment.DeductionTypeSubscription, "deductionRollbackOK": true, "subscriptionID": card.ID, "subDaysToDeduct": days, "refundAmount": 10, "gatewayAmount": 66.5})
			require.NoError(t, err)
			_, err = client.PaymentAuditLog.Update().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10))).SetDetail(string(detail)).Save(ctx)
			require.NoError(t, err)
			subSvc := service.NewSubscriptionService(NewGroupRepository(client, db), NewUserSubscriptionRepository(client), repo, nil, nil, client, nil, nil)
			pay := service.NewPaymentService(client, nil, nil, nil, subSvc, nil, repo, nil, nil)
			action := "REFUND_SUCCESS"
			if mode == "renew without days" {
				action = "REFUND_FINALIZE_NO_RENEW_DAYS"
			}
			_, err = db.Exec(`CREATE FUNCTION refund_card_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action = '` + action + `' THEN RAISE EXCEPTION 'card audit failure'; END IF; RETURN NEW; END $$`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE TRIGGER refund_card_fail BEFORE INSERT ON payment_audit_logs FOR EACH ROW EXECUTE FUNCTION refund_card_fail()`)
			require.NoError(t, err)
			result, err := pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "verified", "admin:1")
			require.Error(t, err)
			require.Nil(t, result)
			unchanged, err := client.UserSubscription.Get(ctx, card.ID)
			require.NoError(t, err)
			require.Equal(t, card.ExpireDay, unchanged.ExpireDay)
			require.Equal(t, card.TodayRemaining, unchanged.TodayRemaining)
			require.Equal(t, card.Status, unchanged.Status)
			pending, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, service.OrderStatusRefundPending, pending.Status)
			_, err = db.Exec(`DROP TRIGGER refund_card_fail ON payment_audit_logs`)
			require.NoError(t, err)
			result, err = pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "retry", "admin:1")
			require.NoError(t, err)
			require.True(t, result.Success)
			settled, err := client.UserSubscription.Get(ctx, card.ID)
			require.NoError(t, err)
			if mode == "close" {
				require.Equal(t, service.SubscriptionStatusExpired, settled.Status)
				require.Zero(t, settled.TodayRemaining)
			} else {
				require.Equal(t, card.ExpireDay-days, settled.ExpireDay)
				require.Equal(t, card.TodayRemaining, settled.TodayRemaining)
			}
			wallet, err := client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			require.Equal(t, 6.0, wallet.Balance, "card settlement must not move wallet value")
		})
	}
}

func TestRefundAtomicPostgres_PointsOnlyAfterFinancialCommit(t *testing.T) {
	for _, failPoints := range []bool{false, true} {
		t.Run(fmt.Sprintf("points failure=%t", failPoints), func(t *testing.T) {
			client, _, pay, user, order := refundAtomicFixture(t, 6, service.OrderStatusRefundPending)
			db, ctx := inflightTestDB(t), context.Background()
			inviter := reclaimSeedUser(t, client, 0)
			pointsRepo := NewPointsRepository(client, db)
			applied, err := pointsRepo.EarnPoints(ctx, service.EarnPointsInput{InviterID: inviter.ID, SourceUserID: user.ID, SourceOrderID: order.ID, Points: 100, PegAt: .01})
			require.NoError(t, err)
			require.True(t, applied)
			settings := NewSettingRepository(client)
			require.NoError(t, settings.Set(ctx, service.SettingKeyPointsEnabled, "true"))
			pay.SetPointsService(service.NewPointsService(pointsRepo, service.NewSettingService(settings, nil), client, nil, nil, nil, nil, nil))
			_, err = db.Exec(`CREATE FUNCTION refund_points_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action = 'REFUND_SUCCESS' THEN RAISE EXCEPTION 'financial audit failure'; END IF; RETURN NEW; END $$`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE TRIGGER refund_points_fail BEFORE INSERT ON payment_audit_logs FOR EACH ROW EXECUTE FUNCTION refund_points_fail()`)
			require.NoError(t, err)
			_, err = pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "verified", "admin:1")
			require.Error(t, err)
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_points_ledger WHERE source_order_id=$1 AND kind='clawback'`, order.ID).Scan(&count))
			require.Zero(t, count, "a rolled-back refund must not claw back points")
			_, err = db.Exec(`DROP TRIGGER refund_points_fail ON payment_audit_logs`)
			require.NoError(t, err)
			if failPoints {
				_, err = db.Exec(`CREATE FUNCTION points_claw_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind = 'clawback' THEN RAISE EXCEPTION 'points failure'; END IF; RETURN NEW; END $$`)
				require.NoError(t, err)
				_, err = db.Exec(`CREATE TRIGGER points_claw_fail BEFORE INSERT ON user_points_ledger FOR EACH ROW EXECUTE FUNCTION points_claw_fail()`)
				require.NoError(t, err)
			}
			result, err := pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "retry", "admin:1")
			require.NoError(t, err)
			require.True(t, result.Success)
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_points_ledger WHERE source_order_id=$1 AND kind='clawback'`, order.ID).Scan(&count))
			want := 1
			if failPoints {
				want = 0
			}
			require.Equal(t, want, count)
			_, err = pay.ResolvePendingRefund(ctx, order.ID, "succeeded", "again", "admin:1")
			require.Error(t, err)
			var available int64
			require.NoError(t, db.QueryRow(`SELECT available FROM user_points_accounts WHERE user_id=$1`, inviter.ID).Scan(&available))
			if failPoints {
				require.Equal(t, int64(100), available)
			} else {
				require.Zero(t, available)
			}
			wallet, err := client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			require.Zero(t, wallet.Balance, "best-effort points failures do not undo a confirmed financial refund")
		})
	}
}
