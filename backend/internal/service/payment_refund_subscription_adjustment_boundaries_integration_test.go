//go:build integration

package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestRefundSubscriptionAdjustmentPG_HeldAuditOwnership(t *testing.T) {
	for _, state := range []string{"malformed", "already restored", "legacy absence"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			c, s, p, provider := refundAdjustmentFixture(t, 40)
			_, err := c.ExecContext(ctx, `CREATE FUNCTION hold_owned_restore() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.expire_day > OLD.expire_day THEN RAISE EXCEPTION 'hold owned restoration'; END IF; RETURN NEW; END $$; CREATE TRIGGER hold_owned_restore BEFORE UPDATE ON user_subscriptions FOR EACH ROW EXECUTE FUNCTION hold_owned_restore()`)
			require.NoError(t, err)
			_, err = s.ExecuteRefund(ctx, p)
			require.Error(t, err)
			require.Equal(t, 1, provider.calls)
			_, err = c.ExecContext(ctx, `DROP TRIGGER hold_owned_restore ON user_subscriptions; DROP FUNCTION hold_owned_restore()`)
			require.NoError(t, err)
			entry, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_DEDUCT_")).Only(ctx)
			require.NoError(t, err)
			switch state {
			case "malformed":
				_, err = c.PaymentAuditLog.UpdateOneID(entry.ID).SetDetail("{invalid").Save(ctx)
			case "already restored":
				// Simulate a durable marker that conflicts with the older failed
				// rollback event. It cannot authorize a second debit or refund.
				_, err = c.PaymentAuditLog.Create().SetOrderID(entry.OrderID).SetAction("REFUND_SUB_RESTORED_" + entry.Action[len("REFUND_SUB_DEDUCT_"):]).SetOperator("admin").SetDetail("{}").Save(ctx)
			case "legacy absence":
				err = c.PaymentAuditLog.DeleteOneID(entry.ID).Exec(ctx)
			}
			require.NoError(t, err)
			retry, early, err := s.PrepareRefund(ctx, p.OrderID, 10, "held owner boundary", false, false)
			require.NoError(t, err)
			require.Nil(t, early)
			result, err := s.ExecuteRefund(ctx, retry)
			if state == "legacy absence" {
				require.NoError(t, err)
				require.False(t, result.Success)
				require.Equal(t, 2, provider.calls)
				assertRefundAdjustmentCard(t, c, retry, 10)
				count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(entry.OrderID), paymentauditlog.ActionHasPrefix("REFUND_SUB_DEDUCT_")).Count(ctx)
				require.NoError(t, err)
				require.Zero(t, count, "legacy absence must not invent an adjustment or restore an unproven interval")
			} else {
				require.Error(t, err)
				require.Equal(t, 1, provider.calls, "invalid held owner must stop before a second provider dispatch")
				card, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
				require.NoError(t, err)
				require.Equal(t, TodayEastDayNumber()+10, card.ExpireDay)
				order, err := c.PaymentOrder.Get(ctx, p.OrderID)
				require.NoError(t, err)
				require.Equal(t, OrderStatusCompleted, order.Status, "retain existing restoreStatus after rejected retry")
			}
			u, err := c.User.Get(ctx, p.Order.UserID)
			require.NoError(t, err)
			require.Zero(t, u.Balance)
		})
	}
}

func TestRefundSubscriptionAdjustmentPG_DeductionRejectsInvalidCurrentCard(t *testing.T) {
	for _, state := range []string{"missing", "suspended", "sql fault"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			c, s, p, provider := refundAdjustmentFixture(t, 40)
			switch state {
			case "missing":
				err := c.UserSubscription.DeleteOneID(p.SubscriptionID).Exec(ctx)
				require.NoError(t, err)
			case "suspended":
				_, err := c.UserSubscription.UpdateOneID(p.SubscriptionID).SetStatus(SubscriptionStatusSuspended).Save(ctx)
				require.NoError(t, err)
			case "sql fault":
				_, err := c.ExecContext(ctx, `CREATE FUNCTION reject_owned_deduction() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.expire_day < OLD.expire_day THEN RAISE EXCEPTION 'card deduction failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_owned_deduction BEFORE UPDATE ON user_subscriptions FOR EACH ROW EXECUTE FUNCTION reject_owned_deduction()`)
				require.NoError(t, err)
			}
			_, err := s.ExecuteRefund(ctx, p)
			require.Error(t, err)
			require.Zero(t, provider.calls)
			order, err := c.PaymentOrder.Get(ctx, p.OrderID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusCompleted, order.Status)
			count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_DEDUCT_")).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
			if state != "missing" {
				card, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
				require.NoError(t, err)
				require.Equal(t, TodayEastDayNumber()+40, card.ExpireDay)
				require.Equal(t, 4.5, card.TodayRemaining)
			}
		})
	}
}

func TestRefundSubscriptionAdjustmentPG_RestorationCannotReviveSuspendedCard(t *testing.T) {
	ctx := context.Background()
	c, s, p, provider := refundAdjustmentFixture(t, 40)
	provider.onRefund = func() {
		_, err := c.UserSubscription.UpdateOneID(p.SubscriptionID).SetStatus(SubscriptionStatusSuspended).Save(ctx)
		require.NoError(t, err)
	}
	_, err := s.ExecuteRefund(ctx, p)
	require.Error(t, err)
	require.Equal(t, 1, provider.calls)
	card, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusSuspended, card.Status)
	require.Equal(t, TodayEastDayNumber()+10, card.ExpireDay)
	count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_RESTORED_")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestRefundSubscriptionAdjustmentPG_OtherActiveCardPreventsPurchaseReactivation(t *testing.T) {
	ctx := context.Background()
	c, s, p, provider := refundAdjustmentFixture(t, 40)
	// Reuse the real private fixture, but bind both durable order and selected
	// plan to a purchase-close operation rather than a renewal shortening.
	snapshot := map[string]any{"subscription": map[string]any{"daily_amount_usd": 10.0, "validity_days": 30.0, "intent": SubscriptionIntentPurchase}}
	_, err := c.PaymentOrder.UpdateOneID(p.OrderID).SetProviderSnapshot(snapshot).Save(ctx)
	require.NoError(t, err)
	p.Order.ProviderSnapshot = snapshot
	var otherID int64
	provider.onRefund = func() {
		other, err := c.UserSubscription.Create().SetUserID(p.Order.UserID).SetStartsAt(time.Now()).SetExpiresAt(ExpireDayToExpiresAt(TodayEastDayNumber() + 20)).SetStatus(SubscriptionStatusActive).SetExpireDay(TodayEastDayNumber() + 20).SetStartDay(TodayEastDayNumber()).SetDailyAmountUsd(10).SetTodayRemaining(10).SetTodayDay(TodayEastDayNumber()).Save(ctx)
		require.NoError(t, err)
		otherID = other.ID
	}
	_, err = s.ExecuteRefund(ctx, p)
	require.Error(t, err)
	require.Equal(t, 1, provider.calls)
	card, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusExpired, card.Status)
	require.Equal(t, TodayEastDayNumber()-1, card.ExpireDay)
	other, err := c.UserSubscription.Get(ctx, otherID)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusActive, other.Status)
	require.Equal(t, TodayEastDayNumber()+20, other.ExpireDay)
	count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_RESTORED_")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestRefundSubscriptionAdjustmentPG_RestoreMarkerFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	c, s, p, provider := refundAdjustmentFixture(t, 40)
	provider.onRefund = func() {
		_, err := c.ExecContext(ctx, `CREATE FUNCTION reject_owned_marker() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'REFUND_SUB_RESTORED_%' THEN RAISE EXCEPTION 'restoration marker failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_owned_marker BEFORE INSERT ON payment_audit_logs FOR EACH ROW EXECUTE FUNCTION reject_owned_marker()`)
		require.NoError(t, err)
	}
	_, err := s.ExecuteRefund(ctx, p)
	require.Error(t, err)
	require.Equal(t, 1, provider.calls)
	card, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
	require.NoError(t, err)
	require.Equal(t, TodayEastDayNumber()+10, card.ExpireDay, "marker failure must roll back card compensation")
	for _, prefix := range []string{"REFUND_SUB_RESTORED_", "REFUND_ROLLBACK_RECOVERED_"} {
		count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix(prefix)).Count(ctx)
		require.NoError(t, err)
		require.Zero(t, count)
	}
}

func TestRefundSubscriptionAdjustmentPG_CancelledTransactionHasNoMutation(t *testing.T) {
	c, s, p, provider := refundAdjustmentFixture(t, 40)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Component-level transaction boundary: cancellation must be observed
	// before the transaction callback, not swallowed by a detached context.
	called := false
	err := s.withRefundSubscriptionTx(ctx, p, func(context.Context) error { called = true; return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
	require.Zero(t, provider.calls)
	assertRefundAdjustmentCard(t, c, p, 40)
	_, err = c.PaymentOrder.Get(context.Background(), p.OrderID)
	require.NoError(t, err)
	require.Equal(t, payment.DeductionTypeSubscription, p.DeductionType)
}
