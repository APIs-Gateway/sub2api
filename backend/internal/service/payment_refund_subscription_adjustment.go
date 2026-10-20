package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/google/uuid"
)

// A durable deduction audit owns exactly one adjustment. Pending snapshots
// reference its database ID, never a caller supplied replacement adjustment.
type refundSubscriptionAdjustment struct {
	Owner           string    `json:"owner"`
	SubscriptionID  int64     `json:"subscriptionID"`
	BeforeExpireDay int       `json:"beforeExpireDay"`
	AfterExpireDay  int       `json:"afterExpireDay"`
	RemovedDays     int       `json:"removedDays"`
	BeforeStatus    string    `json:"beforeStatus"`
	AfterStatus     string    `json:"afterStatus"`
	RemovedToday    float64   `json:"removedToday"`
	TodayDay        int       `json:"todayDay"`
	DailyAmount     float64   `json:"dailyAmount"`
	AfterUpdatedAt  time.Time `json:"afterUpdatedAt"`
}

// Audit actions fit the existing 50-byte column without truncating ownership.
// The detail retains the full UUID; its action suffix encodes all 128 bits.
func refundSubscriptionAuditAction(prefix, owner string) string {
	id, err := uuid.Parse(owner)
	if err != nil {
		return ""
	}
	return prefix + base64.RawURLEncoding.EncodeToString(id[:])
}

func (s *PaymentService) withRefundSubscriptionTx(ctx context.Context, p *RefundPlan, fn func(context.Context) error) error {
	if dbent.TxFromContext(ctx) != nil {
		return fn(ctx)
	}
	tx, err := s.entClientForCtx(ctx).Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); s.invalidateRefundSettlementCaches(p) }()
	if err := fn(dbent.NewTxContext(ctx, tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PaymentService) lockRefundSubscriptionOrder(ctx context.Context, p *RefundPlan) error {
	if err := lockRefundUser(ctx, p.Order.UserID); err != nil {
		return err
	}
	q := s.entClientForCtx(ctx).PaymentOrder.Query().Where(paymentorder.IDEQ(p.OrderID), paymentorder.UserIDEQ(p.Order.UserID), paymentorder.StatusEQ(OrderStatusRefunding))
	if s.entClientForCtx(ctx).Driver().Dialect() != dialect.SQLite {
		q = q.ForUpdate()
	}
	_, err := q.Only(ctx)
	return err
}

// The original close/revoke primitives run under the same lock as the actual
// before/after measurement. PrepareRefund's earlier snapshot is not authority.
func (s *PaymentService) deductRefundSubscription(ctx context.Context, p *RefundPlan, subSvc *SubscriptionService) error {
	if subSvc == nil {
		return fmt.Errorf("subscription service unavailable")
	}
	if subSvc.entClient == nil { // Preserve the existing non-DB service contract.
		if isRenewSubscriptionRefundPlan(p) {
			return subSvc.revokeRenewalDaysForRefund(ctx, p.SubscriptionID, p.SubDaysToDeduct)
		}
		return subSvc.closeSubscriptionForRefund(ctx, p.SubscriptionID)
	}
	var adjustmentID int64
	err := s.withRefundSubscriptionTx(ctx, p, func(txCtx context.Context) error {
		if err := s.lockRefundSubscriptionOrder(txCtx, p); err != nil {
			return err
		}
		client := s.entClientForCtx(txCtx)
		q := client.UserSubscription.Query().Where(usersubscription.IDEQ(p.SubscriptionID), usersubscription.UserIDEQ(p.Order.UserID), usersubscription.DeletedAtIsNil())
		if client.Driver().Dialect() != dialect.SQLite {
			q = q.ForUpdate()
		}
		before, err := q.Only(txCtx)
		if dbent.IsNotFound(err) {
			return ErrSubscriptionNotFound
		}
		if err != nil {
			return err
		}
		if before.Status != SubscriptionStatusActive && before.Status != SubscriptionStatusExpired {
			return fmt.Errorf("refund subscription is revoked")
		}
		if isRenewSubscriptionRefundPlan(p) {
			err = subSvc.revokeRenewalDaysForRefund(txCtx, p.SubscriptionID, p.SubDaysToDeduct)
		} else {
			err = subSvc.closeSubscriptionForRefund(txCtx, p.SubscriptionID)
		}
		if err != nil {
			return err
		}
		after, err := client.UserSubscription.Get(txCtx, p.SubscriptionID)
		if err != nil {
			return err
		}
		a := refundSubscriptionAdjustment{
			Owner: uuid.NewString(), SubscriptionID: p.SubscriptionID,
			BeforeExpireDay: before.ExpireDay, AfterExpireDay: after.ExpireDay,
			RemovedDays:  max(0, before.ExpireDay-after.ExpireDay),
			BeforeStatus: before.Status, AfterStatus: after.Status,
			RemovedToday: math.Max(0, before.TodayRemaining-after.TodayRemaining),
			TodayDay:     before.TodayDay, DailyAmount: before.DailyAmountUsd,
			AfterUpdatedAt: after.UpdatedAt,
		}
		body, err := json.Marshal(a)
		if err != nil {
			return err
		}
		entry, err := client.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(p.OrderID, 10)).SetAction(refundSubscriptionAuditAction("REFUND_SUB_DEDUCT_", a.Owner)).SetOperator("admin").SetDetail(string(body)).Save(txCtx)
		if err != nil {
			return err
		}
		adjustmentID = entry.ID
		return nil
	})
	if err == nil {
		p.subscriptionAdjustmentID = adjustmentID
	}
	return err
}

func (s *PaymentService) loadRefundSubscriptionAdjustment(ctx context.Context, orderID, adjustmentID, subscriptionID int64) (*refundSubscriptionAdjustment, error) {
	entry, err := s.entClientForCtx(ctx).PaymentAuditLog.Query().Where(paymentauditlog.IDEQ(adjustmentID), paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_DEDUCT_")).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("read refund subscription adjustment: %w", err)
	}
	var a refundSubscriptionAdjustment
	if err := json.Unmarshal([]byte(entry.Detail), &a); err != nil {
		return nil, fmt.Errorf("decode refund subscription adjustment: %w", err)
	}
	canonical, err := json.Marshal(a)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace([]byte(entry.Detail))) {
		return nil, fmt.Errorf("invalid noncanonical refund subscription adjustment")
	}
	if _, err := uuid.Parse(a.Owner); err != nil || entry.Action != refundSubscriptionAuditAction("REFUND_SUB_DEDUCT_", a.Owner) || a.SubscriptionID != subscriptionID || a.RemovedDays != max(0, a.BeforeExpireDay-a.AfterExpireDay) || a.AfterExpireDay <= 0 || a.RemovedToday < 0 || math.IsNaN(a.RemovedToday) || math.IsInf(a.RemovedToday, 0) || a.DailyAmount < 0 || math.IsNaN(a.DailyAmount) || math.IsInf(a.DailyAmount, 0) || a.AfterUpdatedAt.IsZero() {
		return nil, fmt.Errorf("invalid refund subscription adjustment")
	}
	return &a, nil
}

func (s *PaymentService) bindHeldRefundSubscriptionAdjustment(ctx context.Context, p *RefundPlan) error {
	entry, err := s.entClientForCtx(ctx).PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUB_DEDUCT_")).Order(paymentauditlog.ByID(sql.OrderDesc())).First(ctx)
	if dbent.IsNotFound(err) {
		return nil
	} // Legacy held deduction.
	if err != nil {
		return err
	}
	a, err := s.loadRefundSubscriptionAdjustment(ctx, p.OrderID, entry.ID, p.SubscriptionID)
	if err != nil {
		return err
	}
	restored, err := s.entClientForCtx(ctx).PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionEQ(refundSubscriptionAuditAction("REFUND_SUB_RESTORED_", a.Owner))).Exist(ctx)
	if err != nil {
		return err
	}
	if restored {
		return fmt.Errorf("held refund subscription adjustment already restored")
	}
	p.subscriptionAdjustmentID = entry.ID
	return nil
}

// Restoration adds only this deduction's actual interval to the current card.
// A renewal may have committed since deduction; its expiry and usage stay live.
func (s *PaymentService) restoreRefundSubscriptionAdjustment(ctx context.Context, p *RefundPlan) error {
	return s.withRefundSubscriptionTx(ctx, p, func(txCtx context.Context) error {
		if err := lockRefundUser(txCtx, p.Order.UserID); err != nil {
			return err
		}
		a, err := s.loadRefundSubscriptionAdjustment(txCtx, p.OrderID, p.subscriptionAdjustmentID, p.SubscriptionID)
		if err != nil {
			return err
		}
		client := s.entClientForCtx(txCtx)
		restored, err := client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionEQ(refundSubscriptionAuditAction("REFUND_SUB_RESTORED_", a.Owner))).Exist(txCtx)
		if err != nil || restored {
			return err
		}
		if err := s.lockRefundSubscriptionOrder(txCtx, p); err != nil {
			return err
		}
		q := client.UserSubscription.Query().Where(usersubscription.IDEQ(p.SubscriptionID), usersubscription.UserIDEQ(p.Order.UserID), usersubscription.DeletedAtIsNil())
		if client.Driver().Dialect() != dialect.SQLite {
			q = q.ForUpdate()
		}
		current, err := q.Only(txCtx)
		if err != nil {
			return err
		}
		if current.Status != SubscriptionStatusActive && current.Status != SubscriptionStatusExpired {
			return fmt.Errorf("refund subscription was independently revoked")
		}
		// Only the exact expiry written by this deduction may be reactivated.
		// A later expiry/status writer cannot be silently undone.
		ownExpiry := current.Status == a.AfterStatus && current.ExpireDay == a.AfterExpireDay && current.UpdatedAt.Equal(a.AfterUpdatedAt)
		if current.Status == SubscriptionStatusExpired && !ownExpiry {
			return fmt.Errorf("expired refund subscription changed after deduction")
		}
		today := TodayEastDayNumber()
		newExpireDay := ClampExpireDay(current.ExpireDay + a.RemovedDays)
		status := current.Status
		if ownExpiry && a.BeforeStatus == SubscriptionStatusActive && newExpireDay >= today {
			other, err := client.UserSubscription.Query().Where(usersubscription.UserIDEQ(current.UserID), usersubscription.IDNEQ(current.ID), usersubscription.StatusEQ(SubscriptionStatusActive), usersubscription.ExpireDayGTE(today), usersubscription.DeletedAtIsNil()).Exist(txCtx)
			if err != nil {
				return err
			}
			if other {
				return fmt.Errorf("another active card prevents refund reactivation")
			}
			status = SubscriptionStatusActive
		}
		if newExpireDay < today {
			status = SubscriptionStatusExpired
		}
		valueRestored := 0.0
		update := client.UserSubscription.UpdateOneID(current.ID).SetExpireDay(newExpireDay).SetExpiresAt(ExpireDayToExpiresAt(newExpireDay)).SetStatus(status)
		if current.TodayDay == a.TodayDay && a.TodayDay == today && current.DailyAmountUsd == a.DailyAmount && a.RemovedToday > 0 {
			valueRestored = math.Max(0, math.Min(a.RemovedToday, current.DailyAmountUsd-current.TodayRemaining))
			update.SetTodayRemaining(current.TodayRemaining + valueRestored)
		}
		if _, err := update.Save(txCtx); err != nil {
			return err
		}
		if err := s.writeRefundAuditStrict(txCtx, p.OrderID, refundSubscriptionAuditAction("REFUND_SUB_RESTORED_", a.Owner), map[string]any{"adjustmentID": p.subscriptionAdjustmentID, "daysRestored": newExpireDay - current.ExpireDay, "todayRestored": valueRestored}); err != nil {
			return err
		}
		return s.writeRefundAuditStrict(txCtx, p.OrderID, refundSubscriptionAuditAction("REFUND_ROLLBACK_RECOVERED_", a.Owner), map[string]any{"subscriptionAdjustmentID": p.subscriptionAdjustmentID})
	})
}

// References are checked against the order-owned durable audit before any
// provider query or financial mutation. Missing references preserve legacy.
func (s *PaymentService) validateRefundSubscriptionAdjustment(ctx context.Context, orderID int64, d refundPendingAuditDetail) error {
	if d.SubscriptionAdjustmentID == 0 {
		return nil
	}
	if d.SubscriptionAdjustmentID < 0 || strings.TrimSpace(d.DeductionType) != "subscription" {
		return fmt.Errorf("invalid refund subscription adjustment reference")
	}
	_, err := s.loadRefundSubscriptionAdjustment(ctx, orderID, d.SubscriptionAdjustmentID, d.SubscriptionID)
	return err
}
