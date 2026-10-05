//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Real private PG transactions and production payment/subscription services.
// This adapter implements only the original repository operations needed by
// the private service fixture; all rows, locks, audits and faults are real SQL.
type refundAdjustmentPGCards struct {
	UserSubscriptionRepository
	client *dbent.Client
}

func (r *refundAdjustmentPGCards) db(ctx context.Context) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.client
}
func (r *refundAdjustmentPGCards) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	m, err := r.db(ctx).UserSubscription.Query().Where(usersubscription.IDEQ(id), usersubscription.DeletedAtIsNil()).Only(ctx)
	if err != nil {
		return nil, err
	}
	return &UserSubscription{ID: m.ID, UserID: m.UserID, Status: m.Status, ExpireDay: m.ExpireDay, ExpiresAt: m.ExpiresAt, TodayRemaining: m.TodayRemaining, TodayDay: m.TodayDay, DailyAmountUSD: m.DailyAmountUsd}, nil
}
func (r *refundAdjustmentPGCards) GrantSubscriptionDays(ctx context.Context, id int64, days int, _, now time.Time) (int64, float64, error) {
	m, err := r.db(ctx).UserSubscription.Query().Where(usersubscription.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		return 0, 0, err
	}
	base := m.ExpireDay
	if floor := EastDayNumber(now) - 1; base < floor {
		base = floor
	}
	next := ClampExpireDay(base + days)
	_, err = r.db(ctx).UserSubscription.UpdateOneID(id).SetExpireDay(next).SetExpiresAt(ExpireDayToExpiresAt(next)).Save(ctx)
	return m.UserID, 0, err
}
func (r *refundAdjustmentPGCards) ShortenSubscriptionWithReclaim(ctx context.Context, id int64, days int, _, now time.Time) (int64, float64, error) {
	m, err := r.db(ctx).UserSubscription.Query().Where(usersubscription.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		return 0, 0, err
	}
	next := m.ExpireDay - days
	if floor := EastDayNumber(now) - 1; next < floor {
		next = floor
	}
	_, err = r.db(ctx).UserSubscription.UpdateOneID(id).SetExpireDay(next).SetExpiresAt(ExpireDayToExpiresAt(next)).Save(ctx)
	return m.UserID, 0, err
}
func (r *refundAdjustmentPGCards) CloseSubscriptionWithReclaim(ctx context.Context, id int64, now time.Time, deleted bool) (int64, float64, error) {
	if deleted {
		return 0, 0, fmt.Errorf("fixture forbids deleting a refund card")
	}
	m, err := r.db(ctx).UserSubscription.Query().Where(usersubscription.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		return 0, 0, err
	}
	_, err = r.db(ctx).UserSubscription.UpdateOneID(id).SetStatus(SubscriptionStatusExpired).SetExpireDay(EastDayNumber(now) - 1).SetExpiresAt(now).SetTodayRemaining(0).Save(ctx)
	return m.UserID, 0, err
}
func (r *refundAdjustmentPGCards) UpdateStatus(ctx context.Context, id int64, status string) error {
	_, err := r.db(ctx).UserSubscription.UpdateOneID(id).SetStatus(status).Save(ctx)
	return err
}

type refundAdjustmentProvider struct {
	payment.Provider
	onRefund func()
	pending  bool
	calls    int
	queries  int
}

func (p *refundAdjustmentProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	p.calls++
	if p.onRefund != nil {
		p.onRefund()
	}
	if p.pending {
		return &payment.RefundResponse{Status: payment.ProviderStatusPending, RefundID: "refund-owned"}, nil
	}
	return nil, fmt.Errorf("mock provider final failure")
}
func (p *refundAdjustmentProvider) QueryRefund(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	p.queries++
	return &payment.RefundResponse{Status: payment.ProviderStatusFailed}, nil
}

func refundAdjustmentFixture(t *testing.T, expireDays int) (*dbent.Client, *PaymentService, *RefundPlan, *refundAdjustmentProvider) {
	t.Helper()
	ctx := context.Background()
	c, s, old := refundPendingPostgresFixture(t)
	today := TodayEastDayNumber()
	card, err := c.UserSubscription.Create().SetUserID(old.Order.UserID).SetStartsAt(time.Now()).SetExpiresAt(ExpireDayToExpiresAt(today + expireDays)).SetStatus(SubscriptionStatusActive).SetExpireDay(today + expireDays).SetStartDay(today - 10).SetDailyAmountUsd(10).SetTodayRemaining(4.5).SetTodayDay(today).SetDailyUsageUsd(2).SetWeeklyUsageUsd(3).SetMonthlyUsageUsd(4).Save(ctx)
	require.NoError(t, err)
	s.subscriptionSvc = NewSubscriptionService(nil, &refundAdjustmentPGCards{client: c}, nil, nil, nil, c, nil, nil)
	s.configService = NewPaymentConfigService(c, nil, nil)
	o, err := c.PaymentOrder.UpdateOneID(old.OrderID).SetOrderType(payment.OrderTypeSubscription).SetStatus(OrderStatusCompleted).SetSubscriptionDays(30).SetProviderSnapshot(map[string]any{"subscription": map[string]any{"daily_amount_usd": 10.0, "validity_days": 30.0, "intent": SubscriptionIntentRenew, "target_subscription_id": card.ID}}).Save(ctx)
	require.NoError(t, err)
	p, early, err := s.PrepareRefund(ctx, o.ID, 10, "refund old renewal", false, false)
	require.NoError(t, err)
	require.Nil(t, early)
	require.NotNil(t, p)
	provider := &refundAdjustmentProvider{}
	original := createPaymentProviderFromInstance
	createPaymentProviderFromInstance = func(string, string, map[string]string) (payment.Provider, error) { return provider, nil }
	t.Cleanup(func() { createPaymentProviderFromInstance = original })
	return c, s, p, provider
}

func refundAdjustmentPaidRenewal(t *testing.T, c *dbent.Client, s *PaymentService, p *RefundPlan, days int) {
	t.Helper()
	ctx := context.Background()
	o, err := c.PaymentOrder.Create().SetUserID(p.Order.UserID).SetUserEmail(p.Order.UserEmail).SetUserName("paid renewal").SetAmount(100).SetPayAmount(100).SetFeeRate(0).SetRechargeCode(uuid.NewString()).SetOutTradeNo(uuid.NewString()).SetPaymentType(payment.TypeStripe).SetPaymentTradeNo("paid-renewal").SetOrderType(payment.OrderTypeSubscription).SetStatus(OrderStatusPaid).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("api.example.com").SetSubscriptionDays(days).SetProviderSnapshot(map[string]any{"subscription": map[string]any{"daily_amount_usd": 10.0, "validity_days": float64(days), "intent": SubscriptionIntentRenew, "target_subscription_id": p.SubscriptionID}}).Save(ctx)
	require.NoError(t, err)
	lease, err := s.acquireSubscriptionFulfillmentLease(ctx, o)
	require.NoError(t, err)
	require.NoError(t, s.doSubLifecycle(ctx, o, SubscriptionIntentRenew, p.SubscriptionID, lease))
	got, err := c.PaymentOrder.Get(ctx, o.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, got.Status)
	count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(o.ID, 10)), paymentauditlog.ActionEQ("SUBSCRIPTION_SUCCESS")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func assertRefundAdjustmentCard(t *testing.T, c *dbent.Client, p *RefundPlan, days int) {
	t.Helper()
	m, err := c.UserSubscription.Get(context.Background(), p.SubscriptionID)
	require.NoError(t, err)
	require.Equal(t, TodayEastDayNumber()+days, m.ExpireDay)
	require.Equal(t, SubscriptionStatusActive, m.Status)
	require.InDelta(t, 4.5, m.TodayRemaining, 1e-9)
	require.Equal(t, 2.0, m.DailyUsageUsd)
	require.Equal(t, 3.0, m.WeeklyUsageUsd)
	require.Equal(t, 4.0, m.MonthlyUsageUsd)
	u, err := c.User.Get(context.Background(), p.Order.UserID)
	require.NoError(t, err)
	require.Zero(t, u.Balance)
}

func TestRefundSubscriptionAdjustmentPG_ImmediateFailureKeepsPaidRenewal(t *testing.T) {
	ctx := context.Background()
	c, s, p, provider := refundAdjustmentFixture(t, 40)
	provider.onRefund = func() { refundAdjustmentPaidRenewal(t, c, s, p, 30) }
	result, err := s.ExecuteRefund(ctx, p)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, provider.calls)
	assertRefundAdjustmentCard(t, c, p, 70)
	// Actual repeated compensation must not grant the removed days twice.
	require.True(t, s.RollbackRefund(ctx, p, fmt.Errorf("duplicate delivery")))
	assertRefundAdjustmentCard(t, c, p, 70)
}

func TestRefundSubscriptionAdjustmentPG_PendingFailureKeepsPaidRenewal(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%v", manual), func(t *testing.T) {
			ctx := context.Background()
			c, s, p, provider := refundAdjustmentFixture(t, 40)
			provider.pending = true
			_, err := c.ExecContext(ctx, `CREATE FUNCTION reject_sub_restore() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.expire_day > OLD.expire_day THEN RAISE EXCEPTION 'injected restoration SQL failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_sub_restore BEFORE UPDATE ON user_subscriptions FOR EACH ROW EXECUTE FUNCTION reject_sub_restore()`)
			require.NoError(t, err)
			result, err := s.ExecuteRefund(ctx, p)
			require.NoError(t, err)
			require.True(t, result.RefundPending)
			require.Contains(t, result.Warning, "rollback failed")
			detail, err := s.latestRefundPendingDetail(ctx, p.OrderID)
			require.NoError(t, err)
			require.False(t, detail.DeductionRollbackOK)
			_, err = c.ExecContext(ctx, `DROP TRIGGER reject_sub_restore ON user_subscriptions; DROP FUNCTION reject_sub_restore()`)
			require.NoError(t, err)
			refundAdjustmentPaidRenewal(t, c, s, p, 30)
			if manual {
				result, err = s.ResolvePendingRefund(ctx, p.OrderID, RefundResolutionFailed, "verified provider failure", "admin")
			} else {
				result, err = s.QueryAndFinalizeRefund(ctx, p.OrderID)
			}
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Equal(t, 1, provider.calls)
			assertRefundAdjustmentCard(t, c, p, 70)
			got, err := c.PaymentOrder.Get(ctx, p.OrderID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundFailed, got.Status)
		})
	}
}

func TestRefundSubscriptionAdjustmentPG_PrepareSnapshotDoesNotOverwriteRenewal(t *testing.T) {
	ctx := context.Background()
	c, s, p, _ := refundAdjustmentFixture(t, 40)
	refundAdjustmentPaidRenewal(t, c, s, p, 30)
	result, err := s.ExecuteRefund(ctx, p)
	require.NoError(t, err)
	require.False(t, result.Success)
	assertRefundAdjustmentCard(t, c, p, 70)
}

func TestRefundSubscriptionAdjustmentPG_NoInterleaveRestoresCard(t *testing.T) {
	c, s, p, _ := refundAdjustmentFixture(t, 40)
	result, err := s.ExecuteRefund(context.Background(), p)
	require.NoError(t, err)
	require.False(t, result.Success)
	assertRefundAdjustmentCard(t, c, p, 40)
}

func TestRefundSubscriptionAdjustmentPG_MaxExpiryAndAlreadyExpired(t *testing.T) {
	t.Run("maximum", func(t *testing.T) {
		maxDay := ClampExpireDay(int(^uint(0) >> 1))
		c, s, p, provider := refundAdjustmentFixture(t, maxDay-TodayEastDayNumber()-10)
		provider.onRefund = func() { refundAdjustmentPaidRenewal(t, c, s, p, 30) }
		result, err := s.ExecuteRefund(context.Background(), p)
		require.NoError(t, err)
		require.False(t, result.Success)
		assertRefundAdjustmentCard(t, c, p, maxDay-TodayEastDayNumber())
	})
	t.Run("already expired", func(t *testing.T) {
		ctx := context.Background()
		c, s, p, _ := refundAdjustmentFixture(t, 5)
		_, err := c.UserSubscription.UpdateOneID(p.SubscriptionID).SetStatus(SubscriptionStatusExpired).SetExpireDay(TodayEastDayNumber() - 5).SetExpiresAt(ExpireDayToExpiresAt(TodayEastDayNumber() - 5)).SetTodayRemaining(0).Save(ctx)
		require.NoError(t, err)
		result, err := s.ExecuteRefund(ctx, p)
		require.NoError(t, err)
		require.False(t, result.Success)
		m, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
		require.NoError(t, err)
		require.Equal(t, SubscriptionStatusExpired, m.Status)
		require.Equal(t, TodayEastDayNumber()-1, m.ExpireDay)
		require.Zero(t, m.TodayRemaining)
	})
}

func TestRefundSubscriptionAdjustmentPG_ActualClampAndCurrentValue(t *testing.T) {
	for _, rollover := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollover=%v", rollover), func(t *testing.T) {
			ctx := context.Background()
			c, s, p, provider := refundAdjustmentFixture(t, 5)
			provider.onRefund = func() {
				refundAdjustmentPaidRenewal(t, c, s, p, 10)
				if rollover {
					_, err := c.UserSubscription.UpdateOneID(p.SubscriptionID).SetTodayDay(TodayEastDayNumber() + 1).SetTodayRemaining(2).Save(ctx)
					require.NoError(t, err)
				}
			}
			result, err := s.ExecuteRefund(ctx, p)
			require.NoError(t, err)
			require.False(t, result.Success)
			m, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
			require.NoError(t, err)
			require.Equal(t, TodayEastDayNumber()+15, m.ExpireDay, "only the six actually removed days are restored, not thirty requested days")
			require.Equal(t, SubscriptionStatusActive, m.Status)
			if rollover {
				require.Equal(t, TodayEastDayNumber()+1, m.TodayDay)
				require.Equal(t, 2.0, m.TodayRemaining)
			} else {
				require.Equal(t, 4.5, m.TodayRemaining)
			}
			require.Equal(t, 2.0, m.DailyUsageUsd)
			require.Equal(t, 3.0, m.WeeklyUsageUsd)
			require.Equal(t, 4.0, m.MonthlyUsageUsd)
		})
	}
}

func TestRefundSubscriptionAdjustmentPG_IndependentRevocationIsNotRevived(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted=%v", deleted), func(t *testing.T) {
			ctx := context.Background()
			c, s, p, provider := refundAdjustmentFixture(t, 40)
			provider.onRefund = func() {
				q := c.UserSubscription.UpdateOneID(p.SubscriptionID)
				if deleted {
					q.SetDeletedAt(time.Now())
				} else {
					q.SetStatus(SubscriptionStatusExpired)
				}
				_, err := q.Save(ctx)
				require.NoError(t, err)
			}
			_, err := s.ExecuteRefund(ctx, p)
			require.Error(t, err)
			m, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
			require.NoError(t, err)
			require.Equal(t, TodayEastDayNumber()+10, m.ExpireDay)
			if deleted {
				require.NotNil(t, m.DeletedAt)
			} else {
				require.Equal(t, SubscriptionStatusExpired, m.Status)
			}
			count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_RESTORED_")).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}

func TestRefundSubscriptionAdjustmentPG_DeductionAuditFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	c, s, p, provider := refundAdjustmentFixture(t, 40)
	_, err := c.ExecContext(ctx, `CREATE FUNCTION reject_sub_deduct() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'REFUND_SUB_DEDUCT_%' THEN RAISE EXCEPTION 'deduction audit failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_sub_deduct BEFORE INSERT ON payment_audit_logs FOR EACH ROW EXECUTE FUNCTION reject_sub_deduct()`)
	require.NoError(t, err)
	_, err = s.ExecuteRefund(ctx, p)
	require.ErrorContains(t, err, "deduction audit failed")
	require.Zero(t, provider.calls)
	assertRefundAdjustmentCard(t, c, p, 40)
	o, err := c.PaymentOrder.Get(ctx, p.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, o.Status)
}

func TestRefundSubscriptionAdjustmentPG_FinalRecoveryAuditFailureKeepsPending(t *testing.T) {
	ctx := context.Background()
	c, s, p, provider := refundAdjustmentFixture(t, 40)
	provider.pending = true
	_, err := c.ExecContext(ctx, `CREATE FUNCTION reject_sub_restore() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.expire_day > OLD.expire_day THEN RAISE EXCEPTION 'hold compensation'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_sub_restore BEFORE UPDATE ON user_subscriptions FOR EACH ROW EXECUTE FUNCTION reject_sub_restore()`)
	require.NoError(t, err)
	result, err := s.ExecuteRefund(ctx, p)
	require.NoError(t, err)
	require.True(t, result.RefundPending)
	_, err = c.ExecContext(ctx, `DROP TRIGGER reject_sub_restore ON user_subscriptions; DROP FUNCTION reject_sub_restore()`)
	require.NoError(t, err)
	refundAdjustmentPaidRenewal(t, c, s, p, 30)
	_, err = c.ExecContext(ctx, `CREATE FUNCTION reject_recovered() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'REFUND_ROLLBACK_RECOVERED_%' THEN RAISE EXCEPTION 'final recovery audit failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_recovered BEFORE INSERT ON payment_audit_logs FOR EACH ROW EXECUTE FUNCTION reject_recovered()`)
	require.NoError(t, err)
	_, err = s.QueryAndFinalizeRefund(ctx, p.OrderID)
	require.Error(t, err)
	m, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
	require.NoError(t, err)
	require.Equal(t, TodayEastDayNumber()+40, m.ExpireDay)
	o, err := c.PaymentOrder.Get(ctx, p.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, o.Status)
	count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_RESTORED_")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	_, err = c.ExecContext(ctx, `DROP TRIGGER reject_recovered ON payment_audit_logs; DROP FUNCTION reject_recovered()`)
	require.NoError(t, err)
	result, err = s.QueryAndFinalizeRefund(ctx, p.OrderID)
	require.NoError(t, err)
	require.False(t, result.Success)
	assertRefundAdjustmentCard(t, c, p, 70)
}

func TestRefundSubscriptionAdjustmentPG_InvalidReferenceStopsBeforeProvider(t *testing.T) {
	for _, invalid := range []string{"zero", "negative", "missing", "other order", "other card"} {
		t.Run(invalid, func(t *testing.T) {
			ctx := context.Background()
			c, s, p, provider := refundAdjustmentFixture(t, 40)
			provider.pending = true
			_, err := c.ExecContext(ctx, `CREATE FUNCTION reject_sub_restore() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.expire_day > OLD.expire_day THEN RAISE EXCEPTION 'hold compensation'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_sub_restore BEFORE UPDATE ON user_subscriptions FOR EACH ROW EXECUTE FUNCTION reject_sub_restore()`)
			require.NoError(t, err)
			result, err := s.ExecuteRefund(ctx, p)
			require.NoError(t, err)
			require.True(t, result.RefundPending)
			_, err = c.ExecContext(ctx, `DROP TRIGGER reject_sub_restore ON user_subscriptions; DROP FUNCTION reject_sub_restore()`)
			require.NoError(t, err)
			pending, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_PENDING")).Order(paymentauditlog.ByID()).All(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, pending)
			entry := pending[len(pending)-1]
			var snapshot map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(entry.Detail), &snapshot))
			id := int64(9223372036854775807)
			switch invalid {
			case "zero":
				id = 0
			case "negative":
				id = -1
			case "other order", "other card":
				original, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_DEDUCT_")).Only(ctx)
				require.NoError(t, err)
				var adjustment map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(original.Detail), &adjustment))
				owner := uuid.NewString()
				// Keep the existing canonical audit field order. The loader must
				// reject ownership before this reference reaches any mutation.
				detail := strings.Replace(original.Detail, string(adjustment["owner"]), strconv.Quote(owner), 1)
				orderID := strconv.FormatInt(p.OrderID, 10)
				if invalid == "other order" {
					orderID = strconv.FormatInt(p.OrderID+1, 10)
				} else {
					detail = strings.Replace(detail, `"subscriptionID":`+strconv.FormatInt(p.SubscriptionID, 10), `"subscriptionID":`+strconv.FormatInt(p.SubscriptionID+1, 10), 1)
				}
				bad, err := c.PaymentAuditLog.Create().SetOrderID(orderID).SetAction("REFUND_SUB_DEDUCT_" + owner).SetOperator("admin").SetDetail(detail).Save(ctx)
				require.NoError(t, err)
				id = bad.ID
			}
			snapshot["subscriptionAdjustmentID"] = json.RawMessage(strconv.FormatInt(id, 10))
			body, err := json.Marshal(snapshot)
			require.NoError(t, err)
			_, err = c.PaymentAuditLog.UpdateOneID(entry.ID).SetDetail(string(body)).Save(ctx)
			require.NoError(t, err)
			_, err = s.QueryAndFinalizeRefund(ctx, p.OrderID)
			require.Error(t, err)
			require.Zero(t, provider.queries, "invalid ownership must be rejected before provider query")
			require.Equal(t, 1, provider.calls)
			card, err := c.UserSubscription.Get(ctx, p.SubscriptionID)
			require.NoError(t, err)
			require.Equal(t, TodayEastDayNumber()+10, card.ExpireDay)
			order, err := c.PaymentOrder.Get(ctx, p.OrderID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundPending, order.Status)
			count, err := c.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_RESTORED_")).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}
