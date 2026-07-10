package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ErrOrderNotFound is returned by HandlePaymentNotification when the webhook
// references an out_trade_no that does not exist in our DB. Callers (webhook
// handlers) should treat this as a terminal, non-retryable condition and still
// respond with a 2xx success to the provider — otherwise the provider will keep
// retrying forever (e.g. when a foreign environment's webhook endpoint is
// misconfigured to point at us, or when our orders table has been wiped).
var ErrOrderNotFound = errors.New("payment order not found")

const (
	balanceFulfillmentLeaseDuration      = 5 * time.Minute
	subscriptionFulfillmentLeaseDuration = 5 * time.Minute
)

// balanceFulfillmentLease fences the balance-order worker that most recently
// claimed a RECHARGING order. Subscription fulfillment keeps its own flow.
type balanceFulfillmentLease struct {
	version time.Time
}

// subscriptionFulfillmentLease keeps subscription recovery independent from
// balance redemption while using the same five-minute updated_at fencing.
type subscriptionFulfillmentLease struct {
	version time.Time
}

// --- Payment Notification & Fulfillment ---

func (s *PaymentService) HandlePaymentNotification(ctx context.Context, n *payment.PaymentNotification, pk string) error {
	if n.Status != payment.NotificationStatusSuccess {
		return nil
	}
	// Look up order by out_trade_no (the external order ID we sent to the provider)
	order, err := s.entClient.PaymentOrder.Query().Where(paymentorder.OutTradeNo(n.OrderID)).Only(ctx)
	if err != nil {
		// Fallback only for true legacy "sub2_N" DB-ID payloads when the
		// current out_trade_no lookup genuinely did not find an order.
		if oid, ok := parseLegacyPaymentOrderID(n.OrderID, err); ok {
			order, getErr := s.entClient.PaymentOrder.Get(ctx, oid)
			if getErr != nil {
				slog.Error("legacy payment order not found", "orderID", oid, "error", getErr)
				return nil
			}
			return s.handlePaymentNotificationForOrder(ctx, order, n, pk)
		}
		if dbent.IsNotFound(err) {
			return fmt.Errorf("%w: out_trade_no=%s", ErrOrderNotFound, n.OrderID)
		}
		return fmt.Errorf("lookup order failed for out_trade_no %s: %w", n.OrderID, err)
	}
	return s.handlePaymentNotificationForOrder(ctx, order, n, pk)
}

func parseLegacyPaymentOrderID(orderID string, lookupErr error) (int64, bool) {
	if !dbent.IsNotFound(lookupErr) {
		return 0, false
	}
	orderID = strings.TrimSpace(orderID)
	if !strings.HasPrefix(orderID, orderIDPrefix) {
		return 0, false
	}
	trimmed := strings.TrimPrefix(orderID, orderIDPrefix)
	if trimmed == "" || trimmed == orderID {
		return 0, false
	}
	oid, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || oid <= 0 {
		return 0, false
	}
	return oid, true
}

func (s *PaymentService) handlePaymentNotificationForOrder(ctx context.Context, o *dbent.PaymentOrder, n *payment.PaymentNotification, pk string) error {
	if o == nil {
		return fmt.Errorf("payment order is missing")
	}
	verified, err := s.confirmNotificationAgainstUpstreamIfNeeded(ctx, o, n, pk)
	if err != nil {
		return err
	}
	if verified != nil {
		n = verified
	}
	return s.confirmPayment(ctx, o.ID, n.TradeNo, n.Amount, pk, n.Metadata)
}

func (s *PaymentService) confirmNotificationAgainstUpstreamIfNeeded(ctx context.Context, o *dbent.PaymentOrder, n *payment.PaymentNotification, pk string) (*payment.PaymentNotification, error) {
	if !paymentNotificationRequiresUpstreamConfirmation(pk) {
		return nil, nil
	}
	prov, err := s.getOrderProvider(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("load provider for payment confirmation: %w", err)
	}
	queryRef := paymentOrderQueryReference(o, prov)
	if queryRef == "" {
		return nil, fmt.Errorf("payment confirmation query reference is missing")
	}
	resp, err := prov.QueryOrder(ctx, queryRef)
	if err != nil {
		return nil, fmt.Errorf("query upstream before confirming payment: %w", err)
	}
	if resp == nil || resp.Status != payment.ProviderStatusPaid {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		s.writeAuditLog(ctx, o.ID, "PAYMENT_UPSTREAM_NOT_PAID", pk, map[string]any{
			"queryRef": queryRef,
			"status":   status,
			"tradeNo":  n.TradeNo,
		})
		return nil, fmt.Errorf("upstream payment is not paid")
	}
	if !isValidProviderAmount(resp.Amount) {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_UPSTREAM_INVALID_AMOUNT", pk, map[string]any{
			"expected": o.PayAmount,
			"paid":     resp.Amount,
			"tradeNo":  resp.TradeNo,
			"queryRef": queryRef,
		})
		return nil, fmt.Errorf("invalid upstream paid amount: %v", resp.Amount)
	}
	if math.Abs(resp.Amount-o.PayAmount) > paymentAmountToleranceForCurrency(PaymentOrderCurrency(o)) {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_UPSTREAM_AMOUNT_MISMATCH", pk, map[string]any{
			"expected": o.PayAmount,
			"paid":     resp.Amount,
			"tradeNo":  resp.TradeNo,
			"queryRef": queryRef,
		})
		return nil, fmt.Errorf("upstream amount mismatch: expected %s, got %s", strconv.FormatFloat(o.PayAmount, 'f', -1, 64), strconv.FormatFloat(resp.Amount, 'f', -1, 64))
	}
	if err := validateProviderSnapshotMetadata(o, prov.ProviderKey(), resp.Metadata); err != nil {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_UPSTREAM_METADATA_MISMATCH", pk, map[string]any{
			"detail":   err.Error(),
			"tradeNo":  resp.TradeNo,
			"queryRef": queryRef,
		})
		return nil, err
	}
	tradeNo := strings.TrimSpace(resp.TradeNo)
	if tradeNo == "" {
		tradeNo = strings.TrimSpace(n.TradeNo)
	}
	return &payment.PaymentNotification{
		TradeNo:  tradeNo,
		OrderID:  n.OrderID,
		Amount:   resp.Amount,
		Status:   n.Status,
		RawData:  n.RawData,
		Metadata: resp.Metadata,
	}, nil
}

func paymentNotificationRequiresUpstreamConfirmation(providerKey string) bool {
	return strings.EqualFold(strings.TrimSpace(providerKey), payment.TypeEasyPay)
}

func (s *PaymentService) confirmPayment(ctx context.Context, oid int64, tradeNo string, paid float64, pk string, metadata map[string]string) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		slog.Error("order not found", "orderID", oid)
		return nil
	}
	instanceProviderKey := ""
	if inst, instErr := s.getOrderProviderInstance(ctx, o); instErr == nil && inst != nil {
		instanceProviderKey = inst.ProviderKey
	}
	expectedProviderKey := expectedNotificationProviderKeyForOrder(s.registry, o, instanceProviderKey)
	if expectedProviderKey != "" && strings.TrimSpace(pk) != "" && !strings.EqualFold(expectedProviderKey, strings.TrimSpace(pk)) {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_PROVIDER_MISMATCH", pk, map[string]any{
			"expectedProvider": expectedProviderKey,
			"actualProvider":   pk,
			"tradeNo":          tradeNo,
		})
		return fmt.Errorf("provider mismatch: expected %s, got %s", expectedProviderKey, pk)
	}
	if err := validateProviderNotificationMetadata(o, pk, metadata); err != nil {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_PROVIDER_METADATA_MISMATCH", pk, map[string]any{
			"detail":  err.Error(),
			"tradeNo": tradeNo,
		})
		return err
	}
	if !isValidProviderAmount(paid) {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_INVALID_AMOUNT", pk, map[string]any{
			"expected": o.PayAmount,
			"paid":     paid,
			"tradeNo":  tradeNo,
		})
		return fmt.Errorf("invalid paid amount from provider: %v", paid)
	}
	if math.Abs(paid-o.PayAmount) > paymentAmountToleranceForCurrency(PaymentOrderCurrency(o)) {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_AMOUNT_MISMATCH", pk, map[string]any{"expected": o.PayAmount, "paid": paid, "tradeNo": tradeNo})
		return fmt.Errorf("amount mismatch: expected %s, got %s", strconv.FormatFloat(o.PayAmount, 'f', -1, 64), strconv.FormatFloat(paid, 'f', -1, 64))
	}
	return s.toPaid(ctx, o, tradeNo, paid, pk)
}

func paymentAmountToleranceForCurrency(currency string) float64 {
	minorUnit := payment.CurrencyMinorUnit(currency)
	if minorUnit <= 2 {
		return amountToleranceCNY
	}
	return math.Pow10(-minorUnit) / 2
}

func isValidProviderAmount(amount float64) bool {
	return amount > 0 && !math.IsNaN(amount) && !math.IsInf(amount, 0)
}

func validateProviderNotificationMetadata(order *dbent.PaymentOrder, providerKey string, metadata map[string]string) error {
	return validateProviderSnapshotMetadata(order, providerKey, metadata)
}

func expectedNotificationProviderKey(registry *payment.Registry, orderPaymentType string, orderProviderKey string, instanceProviderKey string) string {
	if key := strings.TrimSpace(instanceProviderKey); key != "" {
		return key
	}
	if key := strings.TrimSpace(orderProviderKey); key != "" {
		return key
	}
	if registry != nil {
		if key := strings.TrimSpace(registry.GetProviderKey(payment.PaymentType(orderPaymentType))); key != "" {
			return key
		}
	}
	return strings.TrimSpace(orderPaymentType)
}

func (s *PaymentService) toPaid(ctx context.Context, o *dbent.PaymentOrder, tradeNo string, paid float64, pk string) error {
	previousStatus := o.Status
	now := time.Now()
	grace := now.Add(-paymentGraceMinutes * time.Minute)
	c, err := s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(o.ID),
		paymentorder.Or(
			paymentorder.StatusEQ(OrderStatusPending),
			paymentorder.StatusEQ(OrderStatusCancelled),
			paymentorder.And(
				paymentorder.StatusEQ(OrderStatusExpired),
				paymentorder.UpdatedAtGTE(grace),
			),
		),
	).SetStatus(OrderStatusPaid).SetPayAmount(paid).SetPaymentTradeNo(tradeNo).SetPaidAt(now).ClearFailedAt().ClearFailedReason().Save(ctx)
	if err != nil {
		return fmt.Errorf("update to PAID: %w", err)
	}
	if c == 0 {
		return s.alreadyProcessed(ctx, o)
	}
	if previousStatus == OrderStatusCancelled || previousStatus == OrderStatusExpired {
		slog.Info("order recovered from webhook payment success",
			"orderID", o.ID,
			"previousStatus", previousStatus,
			"tradeNo", tradeNo,
			"provider", pk,
		)
		s.writeAuditLog(ctx, o.ID, "ORDER_RECOVERED", pk, map[string]any{
			"previous_status": previousStatus,
			"tradeNo":         tradeNo,
			"paidAmount":      paid,
			"reason":          "webhook payment success received after order " + previousStatus,
		})
	}
	s.writeAuditLog(ctx, o.ID, "ORDER_PAID", pk, map[string]any{"tradeNo": tradeNo, "paidAmount": paid})
	return s.executeFulfillment(ctx, o.ID)
}

func (s *PaymentService) alreadyProcessed(ctx context.Context, o *dbent.PaymentOrder) error {
	cur, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
	if err != nil {
		return nil
	}
	switch cur.Status {
	case OrderStatusCompleted, OrderStatusRefunded:
		return nil
	case OrderStatusFailed:
		return s.executeFulfillment(ctx, o.ID)
	case OrderStatusPaid, OrderStatusRecharging:
		if cur.OrderType == payment.OrderTypeBalance {
			return s.ExecuteBalanceFulfillment(ctx, o.ID)
		}
		if cur.OrderType == payment.OrderTypeSubscription {
			return s.ExecuteSubscriptionFulfillment(ctx, o.ID)
		}
		return fmt.Errorf("order %d is being processed", o.ID)
	case OrderStatusExpired:
		slog.Warn("webhook payment success for expired order beyond grace period",
			"orderID", o.ID,
			"status", cur.Status,
			"updatedAt", cur.UpdatedAt,
		)
		s.writeAuditLog(ctx, o.ID, "PAYMENT_AFTER_EXPIRY", "system", map[string]any{
			"status":    cur.Status,
			"updatedAt": cur.UpdatedAt,
			"reason":    "payment arrived after expiry grace period",
		})
		return nil
	default:
		return nil
	}
}

func (s *PaymentService) executeFulfillment(ctx context.Context, oid int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return fmt.Errorf("get order: %w", err)
	}
	if o.OrderType == payment.OrderTypeSubscription {
		return s.ExecuteSubscriptionFulfillment(ctx, oid)
	}
	return s.ExecuteBalanceFulfillment(ctx, oid)
}

func (s *PaymentService) ExecuteBalanceFulfillment(ctx context.Context, oid int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.Status == OrderStatusCompleted {
		return nil
	}
	if psIsRefundStatus(o.Status) {
		return infraerrors.BadRequest("INVALID_STATUS", "refund-related order cannot fulfill")
	}
	if o.Status != OrderStatusPaid && o.Status != OrderStatusFailed && o.Status != OrderStatusRecharging {
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+o.Status)
	}
	lease, err := s.acquireBalanceFulfillmentLease(ctx, o)
	if err != nil {
		return err
	}
	if lease == nil {
		return nil
	}
	if err := s.doBalance(ctx, o, lease); err != nil {
		s.markBalanceFailedWithLease(ctx, oid, lease, err)
		return err
	}
	return nil
}

func (s *PaymentService) acquireBalanceFulfillmentLease(ctx context.Context, o *dbent.PaymentOrder) (*balanceFulfillmentLease, error) {
	if o == nil {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "nil payment order")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	staleBefore := now.Add(-balanceFulfillmentLeaseDuration)
	updated, err := s.entClient.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(o.ID),
			paymentorder.Or(
				paymentorder.StatusIn(OrderStatusPaid, OrderStatusFailed),
				paymentorder.And(
					paymentorder.StatusEQ(OrderStatusRecharging),
					paymentorder.UpdatedAtLTE(staleBefore),
				),
			),
		).
		SetStatus(OrderStatusRecharging).
		SetUpdatedAt(now).
		ClearFailedAt().
		ClearFailedReason().
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire balance fulfillment lease: %w", err)
	}
	if updated == 0 {
		current, getErr := s.entClient.PaymentOrder.Get(ctx, o.ID)
		if getErr != nil {
			return nil, fmt.Errorf("reload balance fulfillment lease: %w", getErr)
		}
		if current.Status == OrderStatusCompleted {
			return nil, nil
		}
		if current.Status == OrderStatusRecharging {
			return nil, infraerrors.Conflict("CONFLICT", "order is being processed")
		}
		return nil, infraerrors.Conflict("CONFLICT", "order status changed while acquiring balance fulfillment lease")
	}

	claimed, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
	if err != nil {
		return nil, fmt.Errorf("reload acquired balance fulfillment lease: %w", err)
	}
	if claimed.Status != OrderStatusRecharging {
		return nil, infraerrors.Conflict("CONFLICT", "balance fulfillment lease was lost")
	}
	return &balanceFulfillmentLease{version: claimed.UpdatedAt}, nil
}

// redeemAction represents the idempotency decision for balance fulfillment.
type redeemAction int

const (
	// redeemActionCreate: code does not exist — create it, then redeem.
	redeemActionCreate redeemAction = iota
	// redeemActionRedeem: code exists but is unused — skip creation, redeem only.
	redeemActionRedeem
	// redeemActionSkipCompleted: code exists and is already used — skip to mark completed.
	redeemActionSkipCompleted
)

// resolveRedeemAction decides the idempotency action based on an existing redeem code lookup.
// existing is the result of GetByCode; lookupErr is the error from that call.
func resolveRedeemAction(existing *RedeemCode, lookupErr error) redeemAction {
	if existing == nil || lookupErr != nil {
		return redeemActionCreate
	}
	if existing.IsUsed() {
		return redeemActionSkipCompleted
	}
	return redeemActionRedeem
}

// getOrCreateBalanceRedeemCode tolerates the window where another worker
// creates this order's deterministic code after our initial lookup.
func (s *PaymentService) getOrCreateBalanceRedeemCode(ctx context.Context, o *dbent.PaymentOrder) (*RedeemCode, error) {
	existing, lookupErr := s.redeemService.GetByCode(ctx, o.RechargeCode)
	if resolveRedeemAction(existing, lookupErr) != redeemActionCreate {
		return existing, nil
	}

	created := &RedeemCode{Code: o.RechargeCode, Type: RedeemTypeBalance, Value: o.Amount, Status: StatusUnused}
	if err := s.redeemService.CreateCode(ctx, created); err == nil {
		return created, nil
	} else {
		// A stale worker can create the code after this worker's lookup. Re-read
		// it before failing so the current lease holder can finish the order.
		existing, lookupErr = s.redeemService.GetByCode(ctx, o.RechargeCode)
		if lookupErr == nil {
			return existing, nil
		}
		return nil, fmt.Errorf("create redeem code: %w", err)
	}
}

func (s *PaymentService) doBalance(ctx context.Context, o *dbent.PaymentOrder, lease *balanceFulfillmentLease) error {
	redeemCode, err := s.getOrCreateBalanceRedeemCode(ctx, o)
	if err != nil {
		return err
	}
	if redeemCode.IsUsed() {
		s.applyPointsEarnForOrder(ctx, o)
		// Code already created and redeemed — just mark completed
		return s.markBalanceCompletedWithLease(ctx, o, lease, "RECHARGE_SUCCESS")
	}
	if _, err := s.redeemService.Redeem(ContextSkipRedeemAffiliate(ctx), o.UserID, o.RechargeCode); err != nil {
		return fmt.Errorf("redeem balance: %w", err)
	}
	s.applyPointsEarnForOrder(ctx, o)
	return s.markBalanceCompletedWithLease(ctx, o, lease, "RECHARGE_SUCCESS")
}

func (s *PaymentService) markBalanceCompletedWithLease(ctx context.Context, o *dbent.PaymentOrder, lease *balanceFulfillmentLease, auditAction string) error {
	if lease == nil {
		return errors.New("missing balance fulfillment lease")
	}

	now := time.Now()
	updated, err := s.entClient.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(o.ID),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(lease.version),
		).
		SetStatus(OrderStatusCompleted).
		SetCompletedAt(now).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("mark balance completed: %w", err)
	}
	if updated == 0 {
		current, getErr := s.entClient.PaymentOrder.Get(ctx, o.ID)
		if getErr == nil && current.Status == OrderStatusCompleted {
			return nil
		}
		return infraerrors.Conflict("CONFLICT", "balance fulfillment lease was lost before completion")
	}

	s.writeAuditLog(ctx, o.ID, auditAction, "system", map[string]any{
		"rechargeCode":   o.RechargeCode,
		"creditedAmount": o.Amount,
		"payAmount":      o.PayAmount,
	})
	s.dispatchPaymentFulfillmentNotification(o, auditAction)
	return nil
}

func (s *PaymentService) markBalanceFailedWithLease(ctx context.Context, oid int64, lease *balanceFulfillmentLease, cause error) {
	if lease == nil {
		slog.Error("mark balance FAILED without fulfillment lease", "orderID", oid)
		return
	}

	now := time.Now()
	reason := psErrMsg(cause)
	updated, err := s.entClient.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(oid),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(lease.version),
		).
		SetStatus(OrderStatusFailed).
		SetFailedAt(now).
		SetFailedReason(reason).
		Save(ctx)
	if err != nil {
		slog.Error("mark balance FAILED", "orderID", oid, "error", err)
		return
	}
	if updated > 0 {
		s.writeAuditLog(ctx, oid, "FULFILLMENT_FAILED", "system", map[string]any{"reason": reason})
	}
}

func (s *PaymentService) dispatchPaymentFulfillmentNotification(o *dbent.PaymentOrder, auditAction string) {
	if s == nil || s.notificationEmailService == nil || o == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), emailSendTimeout)
		defer cancel()
		var err error
		switch auditAction {
		case "RECHARGE_SUCCESS":
			err = s.sendBalanceRechargeSuccessNotification(ctx, o)
		case "SUBSCRIPTION_SUCCESS":
			err = s.sendSubscriptionPurchaseSuccessNotification(ctx, o)
		default:
			return
		}
		if err != nil {
			slog.Warn("payment fulfillment notification email failed", "order_id", o.ID, "action", auditAction, "err", err.Error())
		}
	}()
}

func (s *PaymentService) sendBalanceRechargeSuccessNotification(ctx context.Context, o *dbent.PaymentOrder) error {
	currentBalance := ""
	if s.userRepo != nil {
		if user, err := s.userRepo.GetByID(ctx, o.UserID); err == nil && user != nil {
			currentBalance = fmt.Sprintf("%.2f", user.Balance)
		}
	}
	return s.notificationEmailService.Send(ctx, NotificationEmailSendInput{
		Event:          NotificationEmailEventBalanceRechargeSuccess,
		RecipientEmail: o.UserEmail,
		RecipientName:  firstNonEmpty(o.UserName, o.UserEmail),
		UserID:         o.UserID,
		SourceType:     "payment_order",
		SourceID:       strconv.FormatInt(o.ID, 10),
		Variables: map[string]string{
			"recharge_amount": fmt.Sprintf("%.2f", o.Amount),
			"current_balance": currentBalance,
			"order_id":        strconv.FormatInt(o.ID, 10),
		},
	})
}

func (s *PaymentService) sendSubscriptionPurchaseSuccessNotification(ctx context.Context, o *dbent.PaymentOrder) error {
	variables := map[string]string{
		"subscription_group": "Subscription",
		"subscription_days":  "",
		"expiry_time":        "",
		"order_id":           strconv.FormatInt(o.ID, 10),
	}
	if o.SubscriptionDays != nil {
		variables["subscription_days"] = strconv.Itoa(*o.SubscriptionDays)
	}
	if o.SubscriptionGroupID != nil {
		if s.groupRepo != nil {
			if group, err := s.groupRepo.GetByID(ctx, *o.SubscriptionGroupID); err == nil && group != nil && strings.TrimSpace(group.Name) != "" {
				variables["subscription_group"] = group.Name
			}
		}
		if s.subscriptionSvc != nil {
			if sub, err := s.subscriptionSvc.GetActiveSubscription(ctx, o.UserID, *o.SubscriptionGroupID); err == nil && sub != nil {
				variables["expiry_time"] = sub.ExpiresAt.Format("2006-01-02 15:04")
			}
		}
	}
	return s.notificationEmailService.Send(ctx, NotificationEmailSendInput{
		Event:          NotificationEmailEventSubscriptionPurchaseSuccess,
		RecipientEmail: o.UserEmail,
		RecipientName:  firstNonEmpty(o.UserName, o.UserEmail),
		UserID:         o.UserID,
		SourceType:     "payment_order",
		SourceID:       strconv.FormatInt(o.ID, 10),
		Variables:      variables,
	})
}

func (s *PaymentService) ExecuteSubscriptionFulfillment(ctx context.Context, oid int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.Status == OrderStatusCompleted {
		return nil
	}
	if psIsRefundStatus(o.Status) {
		return infraerrors.BadRequest("INVALID_STATUS", "refund-related order cannot fulfill")
	}
	if o.Status != OrderStatusPaid && o.Status != OrderStatusFailed && o.Status != OrderStatusRecharging {
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+o.Status)
	}
	// 订阅单带 subscription_group_id 和 subscription_days；D/T 由 provider_snapshot 提供，doSub 内再深校验。
	if o.SubscriptionDays == nil {
		return infraerrors.BadRequest("INVALID_STATUS", "missing subscription info")
	}
	lease, err := s.acquireSubscriptionFulfillmentLease(ctx, o)
	if err != nil {
		return err
	}
	if lease == nil {
		return nil
	}
	if err := s.doSub(ctx, o, lease); err != nil {
		s.markSubscriptionFailedWithLease(ctx, oid, lease, err)
		return err
	}
	return nil
}

func (s *PaymentService) acquireSubscriptionFulfillmentLease(ctx context.Context, o *dbent.PaymentOrder) (*subscriptionFulfillmentLease, error) {
	if o == nil {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "nil payment order")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	staleBefore := now.Add(-subscriptionFulfillmentLeaseDuration)
	updated, err := s.entClient.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(o.ID),
			paymentorder.Or(
				paymentorder.StatusIn(OrderStatusPaid, OrderStatusFailed),
				paymentorder.And(
					paymentorder.StatusEQ(OrderStatusRecharging),
					paymentorder.UpdatedAtLTE(staleBefore),
				),
			),
		).
		SetStatus(OrderStatusRecharging).
		SetUpdatedAt(now).
		ClearFailedAt().
		ClearFailedReason().
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire subscription fulfillment lease: %w", err)
	}
	if updated == 0 {
		current, getErr := s.entClient.PaymentOrder.Get(ctx, o.ID)
		if getErr != nil {
			return nil, fmt.Errorf("reload subscription fulfillment lease: %w", getErr)
		}
		if current.Status == OrderStatusCompleted {
			return nil, nil
		}
		if current.Status == OrderStatusRecharging {
			return nil, infraerrors.Conflict("CONFLICT", "order is being processed")
		}
		return nil, infraerrors.Conflict("CONFLICT", "order status changed while acquiring subscription fulfillment lease")
	}

	claimed, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
	if err != nil {
		return nil, fmt.Errorf("reload acquired subscription fulfillment lease: %w", err)
	}
	if claimed.Status != OrderStatusRecharging {
		return nil, infraerrors.Conflict("CONFLICT", "subscription fulfillment lease was lost")
	}
	return &subscriptionFulfillmentLease{version: claimed.UpdatedAt}, nil
}

func (s *PaymentService) doSub(ctx context.Context, o *dbent.PaymentOrder, lease *subscriptionFulfillmentLease) error {
	// 生命周期意图分发（per-day redesign §5/§7）：renew=延长目标卡、change_plan=关旧开新；
	// 二者参数(目标卡 ID + 新 D/T)由订单冻结快照提供，履约不重算。purchase(默认/老单)走下方建新卡逻辑。
	if intent, targetSubID := readSubscriptionIntent(o); intent != SubscriptionIntentPurchase {
		return s.doSubLifecycle(ctx, o, intent, targetSubID, lease)
	}

	// 订阅单应带 group（gid>0），用于发卡归属。
	var gid int64
	if o.SubscriptionGroupID != nil {
		gid = *o.SubscriptionGroupID
	}
	days := 0
	if o.SubscriptionDays != nil {
		days = *o.SubscriptionDays
	}
	// per-day：严格按订单**冻结快照**发卡（D/T 不按回调时的当前公式/group 配置重算）；
	// 无快照的老套餐单回退 subscription_days + group.daily_limit_usd 兼容路径（dailyAmount=0 时
	// createSubscription 会回退 group）。
	var dailyAmount float64
	var weeklyLimit, monthlyLimit float64
	d, t, hasSnapshot, snapErr := readSubscriptionSnapshotDT(o)
	if snapErr != nil {
		return snapErr
	}
	if hasSnapshot {
		dailyAmount = d
		days = t
		// W/M 同样按冻结快照发卡（spec §2）；老单无 W/M 快照 → 0，createSubscription 回退按 D/T 派生。
		if w, m, wmOK := readSubscriptionSnapshotWM(o); wmOK {
			weeklyLimit = w
			monthlyLimit = m
		}
	}
	if gid > 0 {
		// 套餐/历史卡：校验来源 group 仍存在（无快照的老单还要求 active，保证可推导 D）。
		g, err := s.groupRepo.GetByID(ctx, gid)
		if err != nil || g == nil {
			return fmt.Errorf("group %d no longer exists", gid)
		}
		if !hasSnapshot && g.Status != payment.EntityStatusActive {
			return fmt.Errorf("group %d no longer exists or inactive", gid)
		}
	} else if !hasSnapshot {
		// 自定义单必须有冻结快照提供 D/T；无快照无从发卡，直接失败（已付款会 markFailed）。
		return fmt.Errorf("custom subscription order %d missing pricing snapshot", o.ID)
	}
	// Idempotency: check audit log to see if subscription was already assigned.
	// Prevents double-extension on retry after markCompleted fails.
	if s.hasAuditLog(ctx, o.ID, "SUBSCRIPTION_SUCCESS") {
		slog.Info("subscription already assigned for order, skipping", "orderID", o.ID, "groupID", gid)
		s.applyPointsEarnForOrder(ctx, o)
		return s.markSubscriptionCompletedWithLease(ctx, o, lease)
	}
	if lease == nil {
		return errors.New("missing subscription fulfillment lease")
	}
	if s.subscriptionSvc == nil || s.subscriptionSvc.userSubRepo == nil {
		return errors.New("subscription service is unavailable")
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin subscription fulfill tx: %w", err)
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	orderNote := fmt.Sprintf("payment order %d", o.ID)
	sub, err := s.findPaymentSubscriptionAssignment(txCtx, o, orderNote)
	if err != nil {
		return err
	}
	if sub == nil {
		sub, _, err = s.subscriptionSvc.AssignOrExtendSubscription(txCtx, &AssignSubscriptionInput{UserID: o.UserID, GroupID: gid, ValidityDays: days, DailyAmountUSD: dailyAmount, WeeklyLimitUSD: weeklyLimit, MonthlyLimitUSD: monthlyLimit, AssignedBy: 0, Notes: orderNote})
		if err != nil {
			return fmt.Errorf("assign subscription: %w", err)
		}
	} else {
		slog.Info("recovered existing subscription assignment for order", "orderID", o.ID, "subscriptionID", sub.ID)
	}
	completion := s.entClientForCtx(txCtx).PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(o.ID),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(lease.version),
		)
	c, err := completion.SetStatus(OrderStatusCompleted).SetCompletedAt(time.Now()).Save(txCtx)
	if err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	if c == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed during fulfillment")
	}
	if sub != nil {
		if err := s.writeSubscriptionIDToOrderSnapshot(txCtx, o, sub.ID); err != nil {
			return fmt.Errorf("write subscription snapshot id: %w", err)
		}
	}
	s.writeAuditLog(txCtx, o.ID, "SUBSCRIPTION_SUCCESS", "system", map[string]any{
		"rechargeCode":   o.RechargeCode,
		"creditedAmount": o.Amount,
		"payAmount":      o.PayAmount,
	})
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subscription fulfill: %w", err)
	}
	committed = true

	s.applyPointsEarnForOrder(ctx, o)
	s.dispatchPaymentFulfillmentNotification(o, "SUBSCRIPTION_SUCCESS")
	return nil
}

func (s *PaymentService) findPaymentSubscriptionAssignment(ctx context.Context, o *dbent.PaymentOrder, orderNote string) (*UserSubscription, error) {
	if s == nil || s.subscriptionSvc == nil || s.subscriptionSvc.userSubRepo == nil || o == nil {
		return nil, errors.New("subscription service is unavailable")
	}

	if subscriptionID := paymentSubscriptionIDFromSnapshot(o); subscriptionID > 0 {
		sub, err := s.subscriptionSvc.userSubRepo.GetByID(ctx, subscriptionID)
		if err != nil {
			return nil, fmt.Errorf("load snapshotted subscription %d: %w", subscriptionID, err)
		}
		matches, err := paymentSubscriptionMatchesFrozenOrder(o, sub)
		if err != nil {
			return nil, err
		}
		if !matches {
			return nil, fmt.Errorf("snapshotted subscription %d does not match payment order %d", subscriptionID, o.ID)
		}
		return sub, nil
	}

	subs, err := s.subscriptionSvc.userSubRepo.ListByUserID(ctx, o.UserID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions for assignment recovery: %w", err)
	}
	for i := range subs {
		if hasPaymentSubscriptionOrderNote(subs[i].Notes, orderNote) {
			matches, matchErr := paymentSubscriptionMatchesFrozenOrder(o, &subs[i])
			if matchErr != nil {
				return nil, matchErr
			}
			if !matches {
				return nil, fmt.Errorf("subscription %d has order note but does not match payment order %d", subs[i].ID, o.ID)
			}
			return &subs[i], nil
		}
	}
	return nil, nil
}

func paymentSubscriptionMatchesFrozenOrder(o *dbent.PaymentOrder, sub *UserSubscription) (bool, error) {
	if o == nil || sub == nil || sub.UserID != o.UserID {
		return false, nil
	}
	groupID := int64(0)
	if o.SubscriptionGroupID != nil {
		groupID = *o.SubscriptionGroupID
	}
	if sub.GroupID != groupID {
		return false, nil
	}

	dailyAmount, _, hasSnapshot, err := readSubscriptionSnapshotDT(o)
	if err != nil {
		return false, err
	}
	if !hasSnapshot {
		return true, nil
	}
	if math.Abs(sub.DailyAmountUSD-dailyAmount) > 1e-9 || sub.DailyLimitUSD == nil || math.Abs(*sub.DailyLimitUSD-dailyAmount) > 1e-9 {
		return false, nil
	}
	if weeklyLimit, monthlyLimit, ok := readSubscriptionSnapshotWM(o); ok {
		if sub.WeeklyLimitUSD == nil || sub.MonthlyLimitUSD == nil {
			return false, nil
		}
		if math.Abs(*sub.WeeklyLimitUSD-weeklyLimit) > 1e-9 || math.Abs(*sub.MonthlyLimitUSD-monthlyLimit) > 1e-9 {
			return false, nil
		}
	}
	return true, nil
}

func paymentSubscriptionIDFromSnapshot(o *dbent.PaymentOrder) int64 {
	if o == nil || o.ProviderSnapshot == nil {
		return 0
	}
	raw, ok := o.ProviderSnapshot[subscriptionSnapshotKey].(map[string]any)
	if !ok {
		return 0
	}
	id, _ := snapshotInt64(raw["subscription_id"])
	return id
}

func hasPaymentSubscriptionOrderNote(notes, orderNote string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(notes, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == orderNote {
			return true
		}
	}
	return false
}

// doSubLifecycle 履约续费/转套餐订单（法币支付成功后）：按冻结快照的目标卡 ID + 新 D/T 执行，
// 不扣余额（差价/续费价已由网关收取）。幂等沿用 SUBSCRIPTION_SUCCESS 审计键（重放整笔跳过）。
func (s *PaymentService) doSubLifecycle(ctx context.Context, o *dbent.PaymentOrder, intent string, targetSubID int64, leases ...*subscriptionFulfillmentLease) error {
	var lease *subscriptionFulfillmentLease
	if len(leases) > 0 {
		lease = leases[0]
	}
	d, t, hasSnapshot, snapErr := readSubscriptionSnapshotDT(o)
	if snapErr != nil {
		return snapErr
	}
	if !hasSnapshot {
		return fmt.Errorf("lifecycle subscription order %d missing pricing snapshot", o.ID)
	}
	if targetSubID <= 0 {
		return fmt.Errorf("lifecycle subscription order %d missing target subscription id", o.ID)
	}
	// 幂等快路：已发卡则只补完成状态（SUCCESS 审计已与 apply 同事务写入，见下）。
	if s.hasAuditLog(ctx, o.ID, "SUBSCRIPTION_SUCCESS") {
		slog.Info("lifecycle subscription already applied for order, skipping", "orderID", o.ID, "intent", intent)
		s.applyPointsEarnForOrder(ctx, o)
		return s.markSubscriptionCompletedWithLease(ctx, o, lease)
	}
	if lease == nil {
		return errors.New("missing subscription fulfillment lease")
	}

	// 原子履约（P2#6 根治「续费双倍延期 / 转套餐重复建卡」）：apply + 订单置完成 + SUCCESS 审计键
	// 必须在【同一事务】内提交。否则 apply 在自有事务先提交后、若 markCompleted 的状态更新瞬时报错
	// → 订单被 markFailed 置 FAILED → 管理员重试时 SUCCESS 审计仍缺 → 再 apply 一次 = 双倍发放（资损）。
	// 同事务后：崩溃在提交前 = 全回滚（重试干净重发）；提交成功 = apply 与幂等键同时落库（重试见键跳过）。
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin lifecycle fulfill tx: %w", err)
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var newSubID int64
	switch intent {
	case SubscriptionIntentRenew:
		// 续费：t = 续费天数（快照 validity_days）；延长目标卡有效期，卡 ID 不变。
		sub, applyErr := s.subscriptionSvc.ApplyRenewFromOrder(txCtx, targetSubID, t)
		if applyErr != nil {
			return fmt.Errorf("apply renew: %w", applyErr)
		}
		if sub != nil {
			newSubID = sub.ID
		}
	case SubscriptionIntentChangePlan:
		// 转套餐：d/t = 新档 D/T；关旧卡、开新卡（新卡 ID 不同）。
		res, applyErr := s.subscriptionSvc.ApplyChangePlanFromOrder(txCtx, targetSubID, d, t)
		if applyErr != nil {
			return fmt.Errorf("apply change plan: %w", applyErr)
		}
		if res != nil {
			newSubID = res.NewSubscriptionID
		}
	default:
		return fmt.Errorf("unknown subscription intent %q for order %d", intent, o.ID)
	}

	// 同事务：回写 subscription_id 到订单快照 + 订单置完成 + SUCCESS 审计键（entClientForCtx 自动用事务客户端）。
	completion := s.entClientForCtx(txCtx).PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(o.ID),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(lease.version),
		)
	c, err := completion.SetStatus(OrderStatusCompleted).SetCompletedAt(time.Now()).Save(txCtx)
	if err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	if c == 0 {
		// 订单已不在 recharging（被并发改动）→ 回滚 apply，避免与外层状态不一致。
		return infraerrors.Conflict("CONFLICT", "order status changed during fulfillment")
	}
	if newSubID > 0 {
		if err := s.writeSubscriptionIDToOrderSnapshot(txCtx, o, newSubID); err != nil {
			return fmt.Errorf("write subscription snapshot id: %w", err)
		}
	}
	s.writeAuditLog(txCtx, o.ID, "SUBSCRIPTION_SUCCESS", "system", map[string]any{
		"rechargeCode":   o.RechargeCode,
		"creditedAmount": o.Amount,
		"payAmount":      o.PayAmount,
		"intent":         intent,
	})
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit lifecycle fulfill: %w", err)
	}
	committed = true

	// 提交后才发通知（best-effort，不影响履约原子性）。
	s.applyPointsEarnForOrder(ctx, o)
	s.dispatchPaymentFulfillmentNotification(o, "SUBSCRIPTION_SUCCESS")
	return nil
}

func (s *PaymentService) hasAuditLog(ctx context.Context, orderID int64, action string) bool {
	oid := strconv.FormatInt(orderID, 10)
	c, _ := s.entClientForCtx(ctx).PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(oid), paymentauditlog.ActionEQ(action)).
		Limit(1).Count(ctx)
	return c > 0
}

// applyPointsEarnForOrder 邀请返利积分制（issue #11）earning 钩子：被邀请人法币付款成功（充值单 +
// 套餐单）→ 邀请人返积分。**最佳努力非阻断**：自吞错误（仅写审计），绝不向上抛 → earning 失败不会
// 触发 markFailed、不污染已发出的余额/卡。幂等由 points 流水 partial-unique 保证（重放不重复返）。
func (s *PaymentService) applyPointsEarnForOrder(ctx context.Context, o *dbent.PaymentOrder) {
	if s == nil || s.pointsService == nil || o == nil || o.Amount <= 0 {
		return
	}
	pts, err := s.pointsService.AccrueEarnForOrder(ctx, o)
	if err != nil {
		s.writeAuditLog(ctx, o.ID, "POINTS_EARN_FAILED", "system", map[string]any{"error": err.Error()})
		return
	}
	if pts > 0 {
		baseAmount, _ := pointsEarnBaseAmountForOrder(o)
		s.writeAuditLog(ctx, o.ID, "POINTS_EARNED", "system", map[string]any{"points": pts, "baseAmount": baseAmount})
	}
}

func (s *PaymentService) markSubscriptionCompletedWithLease(ctx context.Context, o *dbent.PaymentOrder, lease *subscriptionFulfillmentLease) error {
	if lease == nil {
		return errors.New("missing subscription fulfillment lease")
	}

	updated, err := s.entClient.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(o.ID),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(lease.version),
		).
		SetStatus(OrderStatusCompleted).
		SetCompletedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("mark subscription completed: %w", err)
	}
	if updated == 0 {
		current, getErr := s.entClient.PaymentOrder.Get(ctx, o.ID)
		if getErr == nil && current.Status == OrderStatusCompleted {
			return nil
		}
		return infraerrors.Conflict("CONFLICT", "subscription fulfillment lease was lost before completion")
	}
	return nil
}

func (s *PaymentService) markSubscriptionFailedWithLease(ctx context.Context, oid int64, lease *subscriptionFulfillmentLease, cause error) {
	if lease == nil {
		slog.Error("mark subscription FAILED without fulfillment lease", "orderID", oid)
		return
	}

	now := time.Now()
	reason := psErrMsg(cause)
	updated, err := s.entClient.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(oid),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(lease.version),
		).
		SetStatus(OrderStatusFailed).
		SetFailedAt(now).
		SetFailedReason(reason).
		Save(ctx)
	if err != nil {
		slog.Error("mark subscription FAILED", "orderID", oid, "error", err)
		return
	}
	if updated > 0 {
		s.writeAuditLog(ctx, oid, "FULFILLMENT_FAILED", "system", map[string]any{"reason": reason})
	}
}

func (s *PaymentService) RetryFulfillment(ctx context.Context, oid int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.PaidAt == nil {
		return infraerrors.BadRequest("INVALID_STATUS", "order is not paid")
	}
	if psIsRefundStatus(o.Status) {
		return infraerrors.BadRequest("INVALID_STATUS", "refund-related order cannot retry")
	}
	if o.Status == OrderStatusRecharging {
		switch o.OrderType {
		case payment.OrderTypeBalance:
			err = s.ExecuteBalanceFulfillment(ctx, oid)
		case payment.OrderTypeSubscription:
			err = s.ExecuteSubscriptionFulfillment(ctx, oid)
		default:
			return infraerrors.Conflict("CONFLICT", "order is being processed")
		}
		if err != nil {
			return err
		}
		s.writeAuditLog(ctx, oid, "RECHARGE_RETRY", "admin", map[string]any{"detail": "admin manual retry"})
		return nil
	}
	if o.Status == OrderStatusCompleted {
		return infraerrors.BadRequest("INVALID_STATUS", "order already completed")
	}
	if o.Status != OrderStatusFailed && o.Status != OrderStatusPaid {
		return infraerrors.BadRequest("INVALID_STATUS", "only paid and failed orders can retry")
	}
	_, err = s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.StatusIn(OrderStatusFailed, OrderStatusPaid)).SetStatus(OrderStatusPaid).ClearFailedAt().ClearFailedReason().Save(ctx)
	if err != nil {
		return fmt.Errorf("reset for retry: %w", err)
	}
	s.writeAuditLog(ctx, oid, "RECHARGE_RETRY", "admin", map[string]any{"detail": "admin manual retry"})
	return s.executeFulfillment(ctx, oid)
}
