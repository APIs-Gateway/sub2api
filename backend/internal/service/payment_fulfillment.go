package service

import (
	"context"
	"encoding/json"
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
	if o.Status != OrderStatusPaid && o.Status != OrderStatusFailed {
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+o.Status)
	}
	c, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.StatusIn(OrderStatusPaid, OrderStatusFailed)).SetStatus(OrderStatusRecharging).Save(ctx)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	if c == 0 {
		return nil
	}
	if err := s.doBalance(ctx, o); err != nil {
		s.markFailed(ctx, oid, err)
		return err
	}
	return nil
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

func (s *PaymentService) doBalance(ctx context.Context, o *dbent.PaymentOrder) error {
	// Idempotency: check if redeem code already exists (from a previous partial run)
	existing, lookupErr := s.redeemService.GetByCode(ctx, o.RechargeCode)
	action := resolveRedeemAction(existing, lookupErr)

	switch action {
	case redeemActionSkipCompleted:
		if err := s.applyAffiliateRebateForOrder(ctx, o); err != nil {
			return err
		}
		// Code already created and redeemed — just mark completed
		return s.markCompleted(ctx, o, "RECHARGE_SUCCESS")
	case redeemActionCreate:
		rc := &RedeemCode{Code: o.RechargeCode, Type: RedeemTypeBalance, Value: o.Amount, Status: StatusUnused}
		if err := s.redeemService.CreateCode(ctx, rc); err != nil {
			return fmt.Errorf("create redeem code: %w", err)
		}
	case redeemActionRedeem:
		// Code exists but unused — skip creation, proceed to redeem
	}
	if _, err := s.redeemService.Redeem(ContextSkipRedeemAffiliate(ctx), o.UserID, o.RechargeCode); err != nil {
		return fmt.Errorf("redeem balance: %w", err)
	}
	if err := s.applyAffiliateRebateForOrder(ctx, o); err != nil {
		return err
	}
	return s.markCompleted(ctx, o, "RECHARGE_SUCCESS")
}

func (s *PaymentService) markCompleted(ctx context.Context, o *dbent.PaymentOrder, auditAction string) error {
	now := time.Now()
	_, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), paymentorder.StatusEQ(OrderStatusRecharging)).SetStatus(OrderStatusCompleted).SetCompletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	s.writeAuditLog(ctx, o.ID, auditAction, "system", map[string]any{
		"rechargeCode":   o.RechargeCode,
		"creditedAmount": o.Amount,
		"payAmount":      o.PayAmount,
	})
	s.dispatchPaymentFulfillmentNotification(o, auditAction)
	return nil
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
	if o.Status != OrderStatusPaid && o.Status != OrderStatusFailed {
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+o.Status)
	}
	// 套餐单带 subscription_group_id；自定义单无 group（P5e：可空），但二者都必有 subscription_days。
	// 自定义单的 D/T 由 provider_snapshot 提供，doSub 内再深校验（无快照无 group 直接失败）。
	if o.SubscriptionDays == nil {
		return infraerrors.BadRequest("INVALID_STATUS", "missing subscription info")
	}
	c, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.StatusIn(OrderStatusPaid, OrderStatusFailed)).SetStatus(OrderStatusRecharging).Save(ctx)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	if c == 0 {
		return nil
	}
	if err := s.doSub(ctx, o); err != nil {
		s.markFailed(ctx, oid, err)
		return err
	}
	return nil
}

func (s *PaymentService) doSub(ctx context.Context, o *dbent.PaymentOrder) error {
	// 生命周期意图分发（per-day redesign §5/§7）：renew=延长目标卡、change_plan=关旧开新；
	// 二者参数(目标卡 ID + 新 D/T)由订单冻结快照提供，履约不重算。purchase(默认/老单)走下方建新卡逻辑。
	if intent, targetSubID := readSubscriptionIntent(o); intent != SubscriptionIntentPurchase {
		return s.doSubLifecycle(ctx, o, intent, targetSubID)
	}

	// 套餐单带 group（gid>0）；自定义单无 group（subscription_group_id NULL → gid=0，P5e）。
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
	d, t, hasSnapshot, snapErr := readSubscriptionSnapshotDT(o)
	if snapErr != nil {
		return snapErr
	}
	if hasSnapshot {
		dailyAmount = d
		days = t
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
		// 自定义单（无 group）必须有冻结快照提供 D/T；无快照无 group 无从发卡，直接失败（已付款会 markFailed）。
		return fmt.Errorf("custom subscription order %d missing pricing snapshot", o.ID)
	}
	// Idempotency: check audit log to see if subscription was already assigned.
	// Prevents double-extension on retry after markCompleted fails.
	if s.hasAuditLog(ctx, o.ID, "SUBSCRIPTION_SUCCESS") {
		slog.Info("subscription already assigned for order, skipping", "orderID", o.ID, "groupID", gid)
		return s.markCompleted(ctx, o, "SUBSCRIPTION_SUCCESS")
	}
	orderNote := fmt.Sprintf("payment order %d", o.ID)
	sub, _, err := s.subscriptionSvc.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{UserID: o.UserID, GroupID: gid, ValidityDays: days, DailyAmountUSD: dailyAmount, AssignedBy: 0, Notes: orderNote})
	if err != nil {
		return fmt.Errorf("assign subscription: %w", err)
	}
	if sub != nil {
		if err := s.writeSubscriptionIDToOrderSnapshot(ctx, o, sub.ID); err != nil {
			return fmt.Errorf("write subscription snapshot id: %w", err)
		}
	}
	return s.markCompleted(ctx, o, "SUBSCRIPTION_SUCCESS")
}

// doSubLifecycle 履约续费/转套餐订单（法币支付成功后）：按冻结快照的目标卡 ID + 新 D/T 执行，
// 不扣余额（差价/续费价已由网关收取）。幂等沿用 SUBSCRIPTION_SUCCESS 审计键（重放整笔跳过）。
func (s *PaymentService) doSubLifecycle(ctx context.Context, o *dbent.PaymentOrder, intent string, targetSubID int64) error {
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
	// 幂等：已发卡则跳过（防重放重复延长/重复换卡）。
	if s.hasAuditLog(ctx, o.ID, "SUBSCRIPTION_SUCCESS") {
		slog.Info("lifecycle subscription already applied for order, skipping", "orderID", o.ID, "intent", intent)
		return s.markCompleted(ctx, o, "SUBSCRIPTION_SUCCESS")
	}

	var newSubID int64
	switch intent {
	case SubscriptionIntentRenew:
		// 续费：t = 续费天数（快照 validity_days）；延长目标卡有效期，卡 ID 不变。
		sub, err := s.subscriptionSvc.ApplyRenewFromOrder(ctx, targetSubID, t)
		if err != nil {
			return fmt.Errorf("apply renew: %w", err)
		}
		if sub != nil {
			newSubID = sub.ID
		}
	case SubscriptionIntentChangePlan:
		// 转套餐：d/t = 新档 D/T；关旧卡、开新卡（新卡 ID 不同）。
		res, err := s.subscriptionSvc.ApplyChangePlanFromOrder(ctx, targetSubID, d, t)
		if err != nil {
			return fmt.Errorf("apply change plan: %w", err)
		}
		if res != nil {
			newSubID = res.NewSubscriptionID
		}
	default:
		return fmt.Errorf("unknown subscription intent %q for order %d", intent, o.ID)
	}

	if newSubID > 0 {
		if err := s.writeSubscriptionIDToOrderSnapshot(ctx, o, newSubID); err != nil {
			return fmt.Errorf("write subscription snapshot id: %w", err)
		}
	}
	return s.markCompleted(ctx, o, "SUBSCRIPTION_SUCCESS")
}

func (s *PaymentService) hasAuditLog(ctx context.Context, orderID int64, action string) bool {
	oid := strconv.FormatInt(orderID, 10)
	c, _ := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(oid), paymentauditlog.ActionEQ(action)).
		Limit(1).Count(ctx)
	return c > 0
}

func (s *PaymentService) applyAffiliateRebateForOrder(ctx context.Context, o *dbent.PaymentOrder) error {
	if o == nil || o.OrderType != payment.OrderTypeBalance || o.Amount <= 0 {
		return nil
	}
	if s.affiliateService == nil {
		return nil
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": fmt.Sprintf("begin affiliate rebate tx: %v", err),
		})
		return fmt.Errorf("begin affiliate rebate tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	claimed, err := s.tryClaimAffiliateRebateAudit(txCtx, tx.Client(), o.ID, o.Amount)
	if err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("claim affiliate rebate audit: %w", err)
	}
	if !claimed {
		return nil
	}

	sourceOrderID := o.ID
	rebateAmount, err := s.affiliateService.AccrueInviteRebateForOrder(txCtx, o.UserID, o.Amount, &sourceOrderID)
	if err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("accrue affiliate rebate: %w", err)
	}

	if rebateAmount <= 0 {
		if err := s.updateClaimedAffiliateRebateAudit(txCtx, tx.Client(), o.ID, "AFFILIATE_REBATE_SKIPPED", map[string]any{
			"baseAmount": o.Amount,
			"reason":     "no inviter bound or rebate amount <= 0",
		}); err != nil {
			s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
				"error": err.Error(),
			})
			return fmt.Errorf("update affiliate rebate skipped audit: %w", err)
		}
		if err := tx.Commit(); err != nil {
			s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
				"error": fmt.Sprintf("commit affiliate rebate tx: %v", err),
			})
			return fmt.Errorf("commit affiliate rebate tx: %w", err)
		}
		return nil
	}

	if err := s.updateClaimedAffiliateRebateAudit(txCtx, tx.Client(), o.ID, "AFFILIATE_REBATE_APPLIED", map[string]any{
		"baseAmount":   o.Amount,
		"rebateAmount": rebateAmount,
	}); err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("update affiliate rebate applied audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": fmt.Sprintf("commit affiliate rebate tx: %v", err),
		})
		return fmt.Errorf("commit affiliate rebate tx: %w", err)
	}
	return nil
}

func (s *PaymentService) tryClaimAffiliateRebateAudit(ctx context.Context, client *dbent.Client, orderID int64, baseAmount float64) (bool, error) {
	if client == nil {
		return false, errors.New("nil payment client")
	}
	oid := strconv.FormatInt(orderID, 10)
	detail, _ := json.Marshal(map[string]any{
		"baseAmount": baseAmount,
		"status":     "reserved",
	})
	rows, err := client.QueryContext(ctx, `
INSERT INTO payment_audit_logs (order_id, action, detail, operator, created_at)
SELECT $1::text, 'AFFILIATE_REBATE_APPLIED', $2::text, 'system', NOW()
WHERE NOT EXISTS (
	SELECT 1
	FROM payment_audit_logs
	WHERE order_id = $1::text
	  AND action IN ('AFFILIATE_REBATE_APPLIED', 'AFFILIATE_REBATE_SKIPPED')
)
ON CONFLICT (order_id, action) DO NOTHING
RETURNING id`, oid, string(detail))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	var claimID int64
	if err := rows.Scan(&claimID); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PaymentService) updateClaimedAffiliateRebateAudit(ctx context.Context, client *dbent.Client, orderID int64, action string, detail map[string]any) error {
	if client == nil {
		return errors.New("nil payment client")
	}
	oid := strconv.FormatInt(orderID, 10)
	detailJSON, _ := json.Marshal(detail)
	updated, err := client.PaymentAuditLog.Update().
		Where(
			paymentauditlog.OrderIDEQ(oid),
			paymentauditlog.ActionEQ("AFFILIATE_REBATE_APPLIED"),
		).
		SetAction(action).
		SetDetail(string(detailJSON)).
		SetOperator("system").
		Save(ctx)
	if err != nil {
		return err
	}
	if updated == 0 {
		return errors.New("affiliate rebate claim log not found")
	}
	return nil
}

func (s *PaymentService) markFailed(ctx context.Context, oid int64, cause error) {
	now := time.Now()
	r := psErrMsg(cause)
	// Only mark FAILED if still in RECHARGING state — prevents overwriting
	// a COMPLETED order when markCompleted failed but fulfillment succeeded.
	c, e := s.entClient.PaymentOrder.Update().
		Where(paymentorder.IDEQ(oid), paymentorder.StatusEQ(OrderStatusRecharging)).
		SetStatus(OrderStatusFailed).SetFailedAt(now).SetFailedReason(r).Save(ctx)
	if e != nil {
		slog.Error("mark FAILED", "orderID", oid, "error", e)
	}
	if c > 0 {
		s.writeAuditLog(ctx, oid, "FULFILLMENT_FAILED", "system", map[string]any{"reason": r})
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
		return infraerrors.Conflict("CONFLICT", "order is being processed")
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
