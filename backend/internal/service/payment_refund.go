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

	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// --- Refund Flow ---

var createPaymentProviderFromInstance = provider.CreateProvider

// getOrderProviderInstance looks up the provider instance that processed this order.
// For legacy orders without provider_instance_id, it resolves only when the
// historical instance is uniquely identifiable from the stored order fields.
func (s *PaymentService) getOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	if s == nil || s.entClient == nil || o == nil {
		return nil, nil
	}

	if snapshot := psOrderProviderSnapshot(o); snapshot != nil {
		return s.resolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(psStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return s.resolveUniqueLegacyOrderProviderInstance(ctx, o)
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, nil
	}
	return s.entClient.PaymentProviderInstance.Get(ctx, instID)
}

// getRefundOrderProviderInstance resolves the provider instance for refund paths.
// Refunds must be pinned to an explicit historical binding, so legacy
// "best-effort" provider guessing is intentionally not allowed here.
func (s *PaymentService) getRefundOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	if s == nil || s.entClient == nil || o == nil {
		return nil, nil
	}

	if snapshot := psOrderProviderSnapshot(o); snapshot != nil {
		return s.resolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(psStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return nil, nil
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("order %d refund provider instance id is invalid: %s", o.ID, instIDStr)
	}
	inst, err := s.entClient.PaymentProviderInstance.Get(ctx, instID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, fmt.Errorf("order %d refund provider instance %s is missing", o.ID, instIDStr)
		}
		return nil, err
	}
	return inst, nil
}

func (s *PaymentService) resolveUniqueLegacyOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	paymentType := payment.GetBasePaymentType(strings.TrimSpace(o.PaymentType))
	providerKey := strings.TrimSpace(psStringValue(o.ProviderKey))
	if providerKey != "" {
		instances, err := s.entClient.PaymentProviderInstance.Query().
			Where(paymentproviderinstance.ProviderKeyEQ(providerKey)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		matched := psFilterLegacyOrderProviderInstances(paymentType, instances)
		if len(matched) == 1 {
			return matched[0], nil
		}
		return nil, nil
	}

	if paymentType == "" {
		return nil, nil
	}

	instances, err := s.entClient.PaymentProviderInstance.Query().
		All(ctx)
	if err != nil {
		return nil, err
	}

	matched := psFilterLegacyOrderProviderInstances(paymentType, instances)
	if len(matched) == 1 {
		return matched[0], nil
	}
	return nil, nil
}

func psFilterLegacyOrderProviderInstances(orderPaymentType string, instances []*dbent.PaymentProviderInstance) []*dbent.PaymentProviderInstance {
	if len(instances) == 0 {
		return nil
	}
	if strings.TrimSpace(orderPaymentType) == "" {
		return instances
	}
	var matched []*dbent.PaymentProviderInstance
	for _, inst := range instances {
		if psLegacyOrderMatchesInstance(orderPaymentType, inst) {
			matched = append(matched, inst)
		}
	}
	return matched
}

func psLegacyOrderMatchesInstance(orderPaymentType string, inst *dbent.PaymentProviderInstance) bool {
	if inst == nil {
		return false
	}

	baseType := payment.GetBasePaymentType(strings.TrimSpace(orderPaymentType))
	instanceProviderKey := strings.TrimSpace(inst.ProviderKey)
	if baseType == "" {
		return false
	}

	if baseType == payment.TypeStripe {
		return instanceProviderKey == payment.TypeStripe
	}
	if instanceProviderKey == payment.TypeStripe {
		return false
	}
	if instanceProviderKey == baseType {
		return true
	}
	return payment.InstanceSupportsType(inst.SupportedTypes, baseType)
}

func (s *PaymentService) RequestRefund(ctx context.Context, oid, uid int64, reason string) error {
	o, err := s.validateRefundRequest(ctx, oid, uid)
	if err != nil {
		return err
	}
	refundAmount := o.Amount
	switch o.OrderType {
	case payment.OrderTypeBalance:
		u, err := s.userRepo.GetByID(ctx, o.UserID)
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		if u.Balance < o.Amount {
			return infraerrors.BadRequest("BALANCE_NOT_ENOUGH", "refund amount exceeds balance")
		}
	case payment.OrderTypeSubscription:
		amount, calcErr := s.calculateSubscriptionRefundAmount(ctx, o)
		if calcErr != nil {
			return calcErr
		}
		if amount <= 0 {
			return infraerrors.BadRequest("NO_REFUNDABLE_DAYS", "subscription has no refundable days")
		}
		refundAmount = amount
	}
	nr := strings.TrimSpace(reason)
	now := time.Now()
	by := fmt.Sprintf("%d", uid)
	c, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.UserIDEQ(uid), paymentorder.StatusEQ(OrderStatusCompleted)).SetStatus(OrderStatusRefundRequested).SetRefundRequestedAt(now).SetRefundRequestReason(nr).SetRefundRequestedBy(by).SetRefundAmount(refundAmount).Save(ctx)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if c == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	s.writeAuditLog(ctx, oid, "REFUND_REQUESTED", fmt.Sprintf("user:%d", uid), map[string]any{"amount": refundAmount, "orderType": o.OrderType, "reason": nr})
	return nil
}

func (s *PaymentService) validateRefundRequest(ctx context.Context, oid, uid int64) (*dbent.PaymentOrder, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.UserID != uid {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission")
	}
	if o.OrderType != payment.OrderTypeBalance && o.OrderType != payment.OrderTypeSubscription {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "only balance and subscription orders can request refund")
	}
	if o.Status != OrderStatusCompleted {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only completed orders can request refund")
	}
	// Check provider instance allows user refund
	inst, err := s.getRefundOrderProviderInstance(ctx, o)
	if err != nil || inst == nil {
		return nil, infraerrors.Forbidden("USER_REFUND_DISABLED", "refund is not available for this order")
	}
	if !inst.AllowUserRefund {
		return nil, infraerrors.Forbidden("USER_REFUND_DISABLED", "user refund is not enabled for this provider")
	}
	return o, nil
}

func (s *PaymentService) PrepareRefund(ctx context.Context, oid int64, amt float64, reason string, force, deduct bool) (*RefundPlan, *RefundResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	// REFUND_PENDING is deliberately excluded: a gateway refund is already in
	// flight, so it must be settled via QueryAndFinalizeRefund instead of
	// submitting another (possibly different-amount) refund.
	ok := []string{OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundFailed}
	if !psSliceContains(ok, o.Status) {
		return nil, nil, infraerrors.BadRequest("INVALID_STATUS", "order status does not allow refund")
	}

	// Check provider instance allows admin refund
	inst, instErr := s.getRefundOrderProviderInstance(ctx, o)
	if instErr != nil {
		slog.Warn("refund: provider instance lookup failed", "orderID", oid, "error", instErr)
		return nil, nil, infraerrors.InternalServer("PROVIDER_LOOKUP_FAILED", "failed to look up payment provider for this order")
	}
	if inst == nil {
		// Legacy order without provider_instance_id — block refund
		return nil, nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not available for this order")
	}
	if !inst.RefundEnabled {
		return nil, nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not enabled for this provider")
	}
	if math.IsNaN(amt) || math.IsInf(amt, 0) {
		return nil, nil, infraerrors.BadRequest("INVALID_AMOUNT", "invalid refund amount")
	}
	orderCurrency := PaymentOrderCurrency(o)
	// A refund of this order already reached the gateway once (REFUND_PENDING
	// snapshot) and may still land. Retries must resubmit the identical
	// gateway request so the deterministic refund number / idempotency key
	// (derived from order id + gateway amount) dedups it at the gateway; a
	// different amount would mint a new refund number and could refund twice.
	prior := s.latestRefundPendingDetail(ctx, oid)
	lockedAmount := prior.lockedRefundAmount(o)
	if lockedAmount > 0 {
		if amt > 0 && math.Abs(amt-lockedAmount) > paymentAmountToleranceForCurrency(orderCurrency) {
			return nil, nil, infraerrors.BadRequest("REFUND_AMOUNT_LOCKED",
				fmt.Sprintf("a refund of %s for this order was already submitted to the gateway; retries must use the same amount", payment.FormatAmountForCurrency(lockedAmount, orderCurrency)))
		}
		amt = lockedAmount
	}
	if o.OrderType == payment.OrderTypeSubscription && !force && amt <= 0 {
		refundAmount, calcErr := s.calculateSubscriptionRefundAmount(ctx, o)
		if calcErr != nil {
			return nil, nil, calcErr
		}
		if refundAmount <= 0 {
			return nil, nil, infraerrors.BadRequest("NO_REFUNDABLE_DAYS", "subscription has no refundable days")
		}
		amt = refundAmount
	}
	if amt <= 0 {
		amt = o.Amount
	}
	if amt-o.Amount > paymentAmountToleranceForCurrency(orderCurrency) {
		return nil, nil, infraerrors.BadRequest("REFUND_AMOUNT_EXCEEDED", "refund amount exceeds recharge")
	}
	refundFeeRate := s.refundFeeRate(ctx)
	gatewayBase, refundFee, ga := calculateGatewayRefundBreakdown(o.Amount, o.PayAmount, amt, refundFeeRate, orderCurrency)
	if lockedAmount > 0 && prior.GatewayAmount > 0 {
		// Reuse the submitted gateway breakdown: the refund fee rate may have
		// changed since, which would change the gateway amount and thus the
		// refund number.
		gatewayBase, refundFee, ga, refundFeeRate = prior.GatewayBaseAmount, prior.RefundFeeAmount, prior.GatewayAmount, prior.RefundFeeRate
	}
	rr := strings.TrimSpace(reason)
	if rr == "" && o.RefundRequestReason != nil {
		rr = *o.RefundRequestReason
	}
	if rr == "" {
		rr = fmt.Sprintf("refund order:%d", o.ID)
	}
	p := &RefundPlan{
		OrderID:           oid,
		Order:             o,
		RefundAmount:      amt,
		GatewayBaseAmount: gatewayBase,
		RefundFeeRate:     refundFeeRate,
		RefundFeeAmount:   refundFee,
		GatewayAmount:     ga,
		Reason:            rr,
		Force:             force,
		DeductBalance:     deduct,
		DeductionType:     payment.DeductionTypeNone,
	}
	// 订阅订单退款必关卡(规格 §6/§8#20「退款即关卡,无条件」),不受 deduct_balance 开关控制:
	// 关卡计划须始终构建(prepDeduct 对订阅单设 DeductionType=Subscription + 关卡/还原天数),
	// 否则 deduct_balance=false 时 ExecuteRefund 的关卡门(DeductionType==Subscription)不成立、
	// 而 gwRefund 仍无条件原路退法币 → 用户拿到现金退款却仍持 active 卡继续用(资损)。
	if deduct || o.OrderType == payment.OrderTypeSubscription {
		if er := s.prepDeduct(ctx, o, p, force); er != nil {
			return nil, er, nil
		}
	}
	return p, nil, nil
}

func (s *PaymentService) refundFeeRate(ctx context.Context) float64 {
	if s == nil || s.configService == nil || s.configService.settingRepo == nil {
		return 0
	}
	cfg, err := s.configService.GetPaymentConfig(ctx)
	if err != nil || cfg == nil {
		return 0
	}
	return cfg.RefundFeeRate
}

func (s *PaymentService) calculateSubscriptionRefundAmount(ctx context.Context, o *dbent.PaymentOrder) (float64, error) {
	sub, err := s.subscriptionForRefund(ctx, o)
	if err != nil {
		return 0, err
	}
	originalDays, err := subscriptionOrderOriginalDays(o)
	if err != nil {
		return 0, err
	}
	card := sub.ToPerDayCard()
	refundableDays := card.RefundableDays(TodayEastDayNumber())
	return RefundAmount(o.Amount, refundableDays, originalDays), nil
}

func (s *PaymentService) subscriptionForRefund(ctx context.Context, o *dbent.PaymentOrder) (*UserSubscription, error) {
	if s.subscriptionSvc == nil {
		return nil, fmt.Errorf("subscription service not configured")
	}
	if subID, ok := readRefundSubscriptionID(o); ok {
		sub, err := s.subscriptionSvc.GetByID(ctx, subID)
		if err != nil {
			return nil, err
		}
		if o != nil && sub.UserID != o.UserID {
			return nil, fmt.Errorf("subscription %d does not belong to order user %d", subID, o.UserID)
		}
		return sub, nil
	}
	return s.subscriptionSvc.GetActiveUserSubscription(ctx, o.UserID)
}

func readRefundSubscriptionID(o *dbent.PaymentOrder) (int64, bool) {
	intent, targetSubID := readSubscriptionIntent(o)
	if intent == SubscriptionIntentRenew && targetSubID > 0 {
		return targetSubID, true
	}
	return readSubscriptionSnapshotSubscriptionID(o)
}

func subscriptionOrderOriginalDays(o *dbent.PaymentOrder) (int, error) {
	_, days, present, err := readSubscriptionSnapshotDT(o)
	if err != nil {
		return 0, err
	}
	if present {
		return days, nil
	}
	if o != nil && o.SubscriptionDays != nil && *o.SubscriptionDays > 0 {
		return *o.SubscriptionDays, nil
	}
	return 0, fmt.Errorf("subscription order missing original validity days")
}

func (s *PaymentService) prepDeduct(ctx context.Context, o *dbent.PaymentOrder, p *RefundPlan, force bool) *RefundResult {
	if o.OrderType == payment.OrderTypeSubscription {
		p.DeductionType = payment.DeductionTypeSubscription
		if s.subscriptionSvc == nil {
			if !force {
				return &RefundResult{Success: false, Warning: "subscription service not configured, use force", RequireForce: true}
			}
			return nil
		}
		sub, err := s.subscriptionForRefund(ctx, o)
		if err == nil && sub != nil {
			today := TodayEastDayNumber()
			p.SubscriptionID = sub.ID
			p.SubExpireDayToRestore = sub.ExpireDay
			p.SubTodayRemainingToRestore = sub.TodayRemaining
			p.SubTodayDayToRestore = sub.TodayDay
			if intent, _ := readSubscriptionIntent(o); intent == SubscriptionIntentRenew {
				originalDays, daysErr := subscriptionOrderOriginalDays(o)
				if daysErr != nil {
					if !force {
						return &RefundResult{Success: false, Warning: daysErr.Error(), RequireForce: true}
					}
					originalDays = 0
				}
				p.SubDaysToDeduct = originalDays
				p.SubDaysToRestore = originalDays
			} else {
				card := sub.ToPerDayCard()
				p.SubDaysToDeduct = card.RefundableDays(today)
				// closeSubscriptionForRefund sets expire_day=today-1. Restoring the
				// original expire_day therefore needs refundableDays+1 days.
				p.SubDaysToRestore = p.SubDaysToDeduct + 1
			}
		} else if !force {
			return &RefundResult{Success: false, Warning: "cannot find active subscription for deduction, use force", RequireForce: true}
		}
		return nil
	}
	u, err := s.userRepo.GetByID(ctx, o.UserID)
	if err != nil {
		if !force {
			return &RefundResult{Success: false, Warning: "cannot fetch user balance, use force", RequireForce: true}
		}
		return nil
	}
	p.DeductionType = payment.DeductionTypeBalance
	// 充值余额一旦进钱包即为 token 额度、消费/透支掉的部分不是可原路退的法币(规格 §4 + 本会话用户决策:
	// 充值不算可退法币、只退套餐)。钱包尚可追回额 recoverable = max(0, min(退款额, 当前余额));余额已不足
	// 以全额追回(用户把充值额花了/透支为负)时,不静默原路退多 → 站点净亏已消费/欠费额(P2#11)。
	// 非 force 直接拒(口径同用户侧 validateRefundRequest 的 BALANCE_NOT_ENOUGH),要求人工确认;
	// force 仍只从钱包扣可追回部分(BalanceToDeduct 已夹到 recoverable,绝不为负)。
	recoverable := u.Balance
	if recoverable < 0 {
		recoverable = 0
	}
	if recoverable < p.RefundAmount && !force {
		return &RefundResult{
			Success:      false,
			Warning:      "wallet balance is insufficient to claw back this recharge in full; consumed/overdrawn credit is not refundable as fiat — use force to proceed",
			RequireForce: true,
		}
	}
	p.BalanceToDeduct = math.Min(p.RefundAmount, recoverable)
	return nil
}

func (s *PaymentService) ExecuteRefund(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	c, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusIn(OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundFailed)).SetStatus(OrderStatusRefunding).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	if c == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		// Skip balance deduction on retry if previous attempt already deducted
		// but failed to roll back (REFUND_ROLLBACK_FAILED in audit log).
		if !s.hasAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED") {
			if err := s.userRepo.DeductBalance(ctx, p.Order.UserID, p.BalanceToDeduct); err != nil {
				s.restoreStatus(ctx, p)
				return nil, fmt.Errorf("deduction: %w", err)
			}
		} else {
			slog.Warn("skipping balance deduction on retry (previous rollback failed)", "orderID", p.OrderID)
			p.BalanceToDeduct = 0
		}
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubscriptionID > 0 {
		if !s.hasAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED") {
			var err error
			if isRenewSubscriptionRefundPlan(p) {
				err = s.subscriptionSvc.revokeRenewalDaysForRefund(ctx, p.SubscriptionID, p.SubDaysToDeduct)
			} else {
				err = s.subscriptionSvc.closeSubscriptionForRefund(ctx, p.SubscriptionID)
			}
			if err != nil {
				s.restoreStatus(ctx, p)
				return nil, fmt.Errorf("deduct subscription for refund: %w", err)
			}
		} else {
			slog.Warn("skipping subscription deduction on retry (previous rollback failed)", "orderID", p.OrderID)
			p.SubDaysToDeduct = 0
			p.SubDaysToRestore = 0
		}
	}
	resp, err := s.gwRefund(ctx, p)
	if err != nil {
		return s.handleGwFail(ctx, p, err)
	}
	return s.finishRefund(ctx, p, resp)
}

func isRenewSubscriptionRefundPlan(p *RefundPlan) bool {
	if p == nil || p.Order == nil || p.Order.OrderType != payment.OrderTypeSubscription {
		return false
	}
	intent, _ := readSubscriptionIntent(p.Order)
	return intent == SubscriptionIntentRenew
}

func (s *PaymentService) gwRefund(ctx context.Context, p *RefundPlan) (*payment.RefundResponse, error) {
	if p.Order.PaymentTradeNo == "" {
		s.writeAuditLog(ctx, p.Order.ID, "REFUND_NO_TRADE_NO", "admin", map[string]any{"detail": "skipped"})
		return &payment.RefundResponse{Status: payment.ProviderStatusSuccess}, nil
	}

	// Use the exact provider instance that created this order, not a random one
	// from the registry. Each instance has its own merchant credentials.
	prov, err := s.getRefundProvider(ctx, p.Order)
	if err != nil {
		return nil, fmt.Errorf("get refund provider: %w", err)
	}
	if err := validateProviderSnapshotMetadata(p.Order, prov.ProviderKey(), providerMerchantIdentityMetadata(prov)); err != nil {
		s.writeAuditLog(ctx, p.Order.ID, "REFUND_PROVIDER_METADATA_MISMATCH", "admin", map[string]any{
			"detail": err.Error(),
		})
		return nil, err
	}
	resp, err := prov.Refund(ctx, payment.RefundRequest{
		TradeNo: p.Order.PaymentTradeNo,
		OrderID: p.Order.OutTradeNo,
		Amount:  formatGatewayRefundAmount(p.GatewayAmount, p.Order),
		Reason:  p.Reason,
	})
	if err != nil {
		if resp != nil && strings.TrimSpace(resp.Status) == payment.ProviderStatusPending {
			return resp, nil
		}
		return nil, err
	}
	if err := validateRefundProviderResponse(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func formatGatewayRefundAmount(amount float64, order *dbent.PaymentOrder) string {
	return payment.FormatAmountForCurrency(amount, PaymentOrderCurrency(order))
}

func validateRefundProviderResponse(resp *payment.RefundResponse) error {
	if resp == nil {
		return fmt.Errorf("payment refund response missing")
	}
	status := strings.TrimSpace(resp.Status)
	switch status {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded, payment.ProviderStatusPending:
		return nil
	case payment.ProviderStatusFailed:
		return fmt.Errorf("payment refund failed: status %s", status)
	default:
		return fmt.Errorf("payment refund returned unknown status: %s", status)
	}
}

func (s *PaymentService) finishRefund(ctx context.Context, p *RefundPlan, resp *payment.RefundResponse) (*RefundResult, error) {
	if err := validateRefundProviderResponse(resp); err != nil {
		return s.handleGwFail(ctx, p, err)
	}
	switch strings.TrimSpace(resp.Status) {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		return s.markRefundOk(ctx, p)
	case payment.ProviderStatusPending:
		return s.markRefundPending(ctx, p, resp)
	default:
		return s.handleGwFail(ctx, p, fmt.Errorf("payment refund returned unknown status: %s", strings.TrimSpace(resp.Status)))
	}
}

// QueryAndFinalizeRefund asks the gateway for the outcome of a REFUND_PENDING
// order and settles it locally.
//
// Fork adaptation: the deduction replayed on gateway success is the one
// captured in the REFUND_PENDING audit snapshot (admin's deduct choice,
// wallet clamp, subscription card and renew intent), applied with the fork's
// refund primitives (closeSubscriptionForRefund / revokeRenewalDaysForRefund),
// instead of upstream's ExtendSubscription(-days) replay. A status CAS
// (REFUND_PENDING -> REFUNDING / REFUND_FAILED) makes concurrent finalize calls
// settle the order at most once.
func (s *PaymentService) QueryAndFinalizeRefund(ctx context.Context, oid int64) (*RefundResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.Status != OrderStatusRefundPending {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only refund pending orders can be finalized")
	}

	prov, err := s.getRefundProvider(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("get refund provider: %w", err)
	}
	queryProvider, ok := prov.(payment.RefundQueryProvider)
	if !ok {
		return nil, infraerrors.BadRequest("REFUND_QUERY_UNSUPPORTED", "this payment provider does not support refund status query; please verify manually")
	}

	detail := s.latestRefundPendingDetail(ctx, oid)
	plan := s.refundFinalizePlan(ctx, o, detail)
	resp, err := queryProvider.QueryRefund(ctx, payment.RefundQueryRequest{
		TradeNo:  o.PaymentTradeNo,
		OrderID:  o.OutTradeNo,
		RefundID: detail.RefundID,
		// Must equal the amount sent to Refund(): providers derive the
		// deterministic refund request number from it.
		Amount: formatGatewayRefundAmount(plan.GatewayAmount, o),
	})
	if err != nil {
		return nil, fmt.Errorf("query refund: %w", err)
	}

	status := ""
	if resp != nil {
		status = strings.TrimSpace(resp.Status)
	}
	switch status {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		return s.finalizeRefundSucceeded(ctx, o, detail, plan)
	case payment.ProviderStatusFailed:
		// Only an explicit gateway failure releases the order for a retry.
		return s.finalizeRefundFailed(ctx, o, fmt.Errorf("payment refund failed: status %s", status))
	default:
		// Pending, missing or unrecognised status: the refund may still land,
		// so the order stays REFUND_PENDING (no retry possible) until the
		// gateway reports a final state or an admin resolves it manually.
		s.writeAuditLog(ctx, oid, "REFUND_QUERY_PENDING", "admin", map[string]any{"refundID": refundResponseID(resp), "status": status})
		warning := "gateway refund is still pending confirmation"
		if status != payment.ProviderStatusPending {
			warning = fmt.Sprintf("gateway refund status %q is not final; still pending confirmation", status)
		}
		return &RefundResult{Success: false, RefundPending: true, Warning: warning}, nil
	}
}

// finalizeRefundSucceeded settles a REFUND_PENDING order whose refund the
// gateway (or an admin, after manual verification) confirmed: status CAS
// REFUND_PENDING -> REFUNDING, replay the snapshot deduction, then mark the
// order refunded (points clawback included).
func (s *PaymentService) finalizeRefundSucceeded(ctx context.Context, o *dbent.PaymentOrder, detail refundPendingAuditDetail, plan *RefundPlan) (*RefundResult, error) {
	c, err := s.entClient.PaymentOrder.Update().
		Where(paymentorder.IDEQ(o.ID), paymentorder.StatusEQ(OrderStatusRefundPending)).
		SetStatus(OrderStatusRefunding).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	if c == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if !detail.DeductionRollbackOK {
		// The deduction taken before the gateway call was never rolled
		// back, so it is still in place: do not deduct twice.
		plan.BalanceToDeduct = 0
		plan.SubDaysToDeduct = 0
	} else if err := s.applyRefundFinalDeduction(ctx, plan); err != nil {
		_, _ = s.entClient.PaymentOrder.UpdateOneID(o.ID).SetStatus(OrderStatusRefundPending).Save(ctx)
		s.writeAuditLog(ctx, o.ID, "REFUND_FINALIZE_DEDUCTION_FAILED", "admin", map[string]any{"detail": psErrMsg(err)})
		return nil, err
	}
	return s.markRefundOk(ctx, plan)
}

// Manual resolutions accepted by ResolvePendingRefund.
const (
	RefundResolutionSucceeded = "succeeded"
	RefundResolutionFailed    = "failed"
)

// ResolvePendingRefund lets an admin settle a REFUND_PENDING order by hand
// after verifying the refund outcome in the gateway's merchant console. It is
// the exit for orders whose gateway query keeps erroring, keeps reporting a
// non-final status, or whose provider cannot be queried at all.
//
//   - succeeded: same settlement as a confirmed gateway query (snapshot
//     deduction replay + points clawback), guarded by the same status CAS.
//   - failed: REFUND_PENDING -> REFUND_FAILED (status CAS). A later retry is
//     pinned to the original amount by PrepareRefund, so if the original
//     refund did land after all the gateway dedups the resubmission.
//
// Every resolution is audited (REFUND_MANUAL_RESOLVED_<attempt>) with the
// operator.
func (s *PaymentService) ResolvePendingRefund(ctx context.Context, oid int64, outcome, note, operator string) (*RefundResult, error) {
	outcome = strings.ToLower(strings.TrimSpace(outcome))
	if outcome != RefundResolutionSucceeded && outcome != RefundResolutionFailed {
		return nil, infraerrors.BadRequest("INVALID_OUTCOME", "outcome must be succeeded or failed")
	}
	note = strings.TrimSpace(note)
	operator = strings.TrimSpace(operator)
	if operator == "" {
		operator = "admin"
	}
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.Status != OrderStatusRefundPending {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only refund pending orders can be resolved")
	}

	detail := s.latestRefundPendingDetail(ctx, oid)
	plan := s.refundFinalizePlan(ctx, o, detail)
	var (
		result *RefundResult
		rErr   error
	)
	if outcome == RefundResolutionSucceeded {
		result, rErr = s.finalizeRefundSucceeded(ctx, o, detail, plan)
	} else {
		reason := "refund manually marked failed by " + operator
		if note != "" {
			reason += ": " + note
		}
		result, rErr = s.finalizeRefundFailed(ctx, o, errors.New(reason))
	}
	if rErr != nil {
		return nil, rErr
	}
	// Attempt-suffixed action: payment_audit_logs is unique on (order_id,
	// action), and an order can be resolved by hand more than once (resolve
	// failed -> retry -> pending again -> resolve).
	s.writeAuditLog(ctx, oid, refundAttemptAuditAction("REFUND_MANUAL_RESOLVED"), operator, map[string]any{
		"outcome":       outcome,
		"note":          note,
		"refundAmount":  plan.RefundAmount,
		"gatewayAmount": plan.GatewayAmount,
	})
	return result, nil
}

// refundFinalizePlan rebuilds the refund plan of a REFUND_PENDING order from
// the snapshot written by markRefundPending. Orders without a snapshot fall
// back to the order-type default deduction (wallet for balance orders, card
// close / renew revoke for subscription orders).
func (s *PaymentService) refundFinalizePlan(ctx context.Context, o *dbent.PaymentOrder, d refundPendingAuditDetail) *RefundPlan {
	reason := strings.TrimSpace(psStringValue(o.RefundReason))
	if reason == "" {
		reason = fmt.Sprintf("refund order:%d", o.ID)
	}
	p := &RefundPlan{
		OrderID:           o.ID,
		Order:             o,
		RefundAmount:      o.RefundAmount,
		GatewayBaseAmount: d.GatewayBaseAmount,
		RefundFeeRate:     d.RefundFeeRate,
		RefundFeeAmount:   d.RefundFeeAmount,
		GatewayAmount:     d.GatewayAmount,
		Reason:            reason,
		Force:             o.ForceRefund,
		DeductionType:     d.DeductionType,
		BalanceToDeduct:   d.BalanceToDeduct,
		SubDaysToDeduct:   d.SubDaysToDeduct,
		SubscriptionID:    d.SubscriptionID,
	}
	if p.GatewayAmount <= 0 {
		p.RefundFeeRate = s.refundFeeRate(ctx)
		p.GatewayBaseAmount, p.RefundFeeAmount, p.GatewayAmount = calculateGatewayRefundBreakdown(o.Amount, o.PayAmount, o.RefundAmount, p.RefundFeeRate, PaymentOrderCurrency(o))
	}
	if !d.hasSnapshot {
		switch o.OrderType {
		case payment.OrderTypeBalance:
			p.DeductionType = payment.DeductionTypeBalance
			p.BalanceToDeduct = o.RefundAmount
		case payment.OrderTypeSubscription:
			p.DeductionType = payment.DeductionTypeSubscription
			if subID, ok := readRefundSubscriptionID(o); ok {
				p.SubscriptionID = subID
			}
			if isRenewSubscriptionRefundPlan(p) {
				if days, err := subscriptionOrderOriginalDays(o); err == nil {
					p.SubDaysToDeduct = days
				}
			}
		default:
			p.DeductionType = payment.DeductionTypeNone
		}
	}
	return p
}

// applyRefundFinalDeduction replays the planned deduction once the gateway has
// confirmed the refund. The wallet clawback is clamped to the current balance
// (never pushed negative), matching prepDeduct's force semantics; the shortfall
// is audited. Subscription cards are closed (purchase) or have the renewed
// days revoked (renew); a card that no longer exists is tolerated.
func (s *PaymentService) applyRefundFinalDeduction(ctx context.Context, p *RefundPlan) error {
	switch p.DeductionType {
	case payment.DeductionTypeBalance:
		planned := p.BalanceToDeduct
		if planned <= 0 {
			p.BalanceToDeduct = 0
			return nil
		}
		u, err := s.userRepo.GetByID(ctx, p.Order.UserID)
		if err != nil {
			return fmt.Errorf("load user balance: %w", err)
		}
		recoverable := math.Max(0, u.Balance)
		p.BalanceToDeduct = math.Min(planned, recoverable)
		if p.BalanceToDeduct < planned {
			s.writeAuditLog(ctx, p.OrderID, "REFUND_FINALIZE_BALANCE_SHORTFALL", "admin", map[string]any{
				"planned":  planned,
				"deducted": p.BalanceToDeduct,
			})
		}
		if p.BalanceToDeduct > 0 {
			if err := s.userRepo.DeductBalance(ctx, p.Order.UserID, p.BalanceToDeduct); err != nil {
				p.BalanceToDeduct = 0
				return fmt.Errorf("deduction: %w", err)
			}
		}
	case payment.DeductionTypeSubscription:
		if p.SubscriptionID <= 0 || s.subscriptionSvc == nil {
			p.SubDaysToDeduct = 0
			return nil
		}
		var err error
		if isRenewSubscriptionRefundPlan(p) {
			if p.SubDaysToDeduct <= 0 {
				// Nothing was planned to be revoked (e.g. a forced refund of
				// a renew order without recorded validity days): there is
				// nothing to deduct, so settle instead of erroring forever.
				p.SubDaysToDeduct = 0
				s.writeAuditLog(ctx, p.OrderID, "REFUND_FINALIZE_NO_RENEW_DAYS", "admin", map[string]any{"subscriptionID": p.SubscriptionID})
				return nil
			}
			err = s.subscriptionSvc.revokeRenewalDaysForRefund(ctx, p.SubscriptionID, p.SubDaysToDeduct)
		} else {
			err = s.subscriptionSvc.closeSubscriptionForRefund(ctx, p.SubscriptionID)
		}
		if err != nil {
			if errors.Is(err, ErrSubscriptionNotFound) {
				p.SubDaysToDeduct = 0
				return nil
			}
			return fmt.Errorf("deduct subscription for refund: %w", err)
		}
	}
	return nil
}

func (s *PaymentService) finalizeRefundFailed(ctx context.Context, o *dbent.PaymentOrder, gErr error) (*RefundResult, error) {
	now := time.Now()
	c, err := s.entClient.PaymentOrder.Update().
		Where(paymentorder.IDEQ(o.ID), paymentorder.StatusEQ(OrderStatusRefundPending)).
		SetStatus(OrderStatusRefundFailed).
		SetFailedAt(now).
		SetFailedReason(psErrMsg(gErr)).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund failed: %w", err)
	}
	if c == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	s.writeAuditLog(ctx, o.ID, "REFUND_FAILED", "admin", map[string]any{"detail": psErrMsg(gErr)})
	return &RefundResult{Success: false, Warning: "gateway refund failed: " + psErrMsg(gErr)}, nil
}

type refundPendingAuditDetail struct {
	RefundID            string  `json:"refundID"`
	DeductionRollbackOK bool    `json:"deductionRollbackOK"`
	DeductionType       string  `json:"deductionType"`
	BalanceToDeduct     float64 `json:"balanceToDeduct"`
	SubDaysToDeduct     int     `json:"subDaysToDeduct"`
	SubscriptionID      int64   `json:"subscriptionID"`
	GatewayBaseAmount   float64 `json:"gatewayBaseAmount"`
	GatewayAmount       float64 `json:"gatewayAmount"`
	RefundFeeRate       float64 `json:"refundFeeRate"`
	RefundFeeAmount     float64 `json:"refundFeeAmount"`
	RefundAmount        float64 `json:"refundAmount"`

	// found reports that a REFUND_PENDING audit entry exists at all, i.e. a
	// refund of this order was accepted by the gateway at least once.
	found       bool
	hasSnapshot bool
}

// lockedRefundAmount returns the refund amount a retry of this order must
// reuse, or 0 when no refund of the order ever reached REFUND_PENDING.
func (d refundPendingAuditDetail) lockedRefundAmount(o *dbent.PaymentOrder) float64 {
	if !d.found {
		return 0
	}
	if d.RefundAmount > 0 {
		return d.RefundAmount
	}
	if o != nil && o.RefundAmount > 0 {
		return o.RefundAmount
	}
	return 0
}

func (s *PaymentService) latestRefundPendingDetail(ctx context.Context, oid int64) refundPendingAuditDetail {
	detail := refundPendingAuditDetail{DeductionRollbackOK: true}
	logEntry, err := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Order(paymentauditlog.ByCreatedAt(sql.OrderDesc()), paymentauditlog.ByID(sql.OrderDesc())).
		First(ctx)
	if err != nil || logEntry == nil {
		return detail
	}
	detail.found = true
	_ = json.Unmarshal([]byte(logEntry.Detail), &detail)
	detail.RefundID = strings.TrimSpace(detail.RefundID)
	detail.DeductionType = strings.TrimSpace(detail.DeductionType)
	detail.hasSnapshot = detail.DeductionType != ""
	return detail
}

// getRefundProvider creates a provider using the order's original instance config.
// Delegates to getOrderProvider which handles instance lookup and fallback.
func (s *PaymentService) getRefundProvider(ctx context.Context, o *dbent.PaymentOrder) (payment.Provider, error) {
	inst, err := s.getRefundOrderProviderInstance(ctx, o)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, fmt.Errorf("refund provider instance is unavailable for order %d", o.ID)
	}
	return s.createProviderFromInstance(ctx, inst)
}

func (s *PaymentService) handleGwFail(ctx context.Context, p *RefundPlan, gErr error) (*RefundResult, error) {
	if s.RollbackRefund(ctx, p, gErr) {
		s.restoreStatus(ctx, p)
		s.writeAuditLog(ctx, p.OrderID, refundAttemptAuditAction("REFUND_GATEWAY_FAILED"), "admin", map[string]any{"detail": psErrMsg(gErr)})
		return &RefundResult{Success: false, Warning: "gateway failed: " + psErrMsg(gErr) + ", rolled back"}, nil
	}
	now := time.Now()
	_, _ = s.entClient.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(OrderStatusRefundFailed).SetFailedAt(now).SetFailedReason(psErrMsg(gErr)).Save(ctx)
	s.writeAuditLog(ctx, p.OrderID, "REFUND_FAILED", "admin", map[string]any{"detail": psErrMsg(gErr)})
	return nil, infraerrors.InternalServer("REFUND_FAILED", psErrMsg(gErr))
}

func refundAttemptAuditAction(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()%1_000_000_000)
}

func (s *PaymentService) markRefundOk(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	fs := OrderStatusRefunded
	if p.RefundAmount < p.Order.Amount {
		fs = OrderStatusPartiallyRefunded
	}
	now := time.Now()
	_, err := s.entClient.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(fs).SetRefundAmount(p.RefundAmount).SetRefundReason(p.Reason).SetRefundAt(now).SetForceRefund(p.Force).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund: %w", err)
	}
	s.writeAuditLog(ctx, p.OrderID, "REFUND_SUCCESS", "admin", map[string]any{
		"refundAmount":      p.RefundAmount,
		"gatewayBaseAmount": p.GatewayBaseAmount,
		"gatewayAmount":     p.GatewayAmount,
		"refundFeeRate":     p.RefundFeeRate,
		"refundFeeAmount":   p.RefundFeeAmount,
		"reason":            p.Reason,
		"balanceDeducted":   p.BalanceToDeduct,
		"force":             p.Force,
	})
	// 邀请返利积分制（issue #11）clawback 唯一挂点：退款最终落单成功后，按实退比例撤回邀请人积分。
	s.applyPointsClawbackForOrder(ctx, p.Order.ID, p.RefundAmount, p.Order.Amount)
	return &RefundResult{
		Success:         true,
		BalanceDeducted: p.BalanceToDeduct,
		SubDaysDeducted: p.SubDaysToDeduct,
		GatewayAmount:   p.GatewayAmount,
		RefundFeeRate:   p.RefundFeeRate,
		RefundFeeAmount: p.RefundFeeAmount,
	}, nil
}

// applyPointsClawbackForOrder 退款撤回积分（仅由 markRefundOk 调用）。**最佳努力非阻断**：自吞错误
// （仅写审计），不影响退款落单。按实退比例 floor 撤、可转负、一单一撤幂等（详见 PointsService）。
func (s *PaymentService) applyPointsClawbackForOrder(ctx context.Context, orderID int64, refundAmount, originalAmount float64) {
	if s == nil || s.pointsService == nil || orderID <= 0 || originalAmount <= 0 {
		return
	}
	clawed, err := s.pointsService.ClawbackForOrder(ctx, orderID, refundAmount, originalAmount)
	if err != nil {
		s.writeAuditLog(ctx, orderID, "POINTS_CLAWBACK_FAILED", "system", map[string]any{"error": err.Error()})
		return
	}
	if clawed > 0 {
		s.writeAuditLog(ctx, orderID, "POINTS_CLAWBACK_APPLIED", "system", map[string]any{"points": clawed, "refundAmount": refundAmount})
	}
}

func (s *PaymentService) markRefundPending(ctx context.Context, p *RefundPlan, resp *payment.RefundResponse) (*RefundResult, error) {
	balanceDeducted := p.BalanceToDeduct
	subDaysDeducted := p.SubDaysToDeduct
	rollbackOK := s.RollbackRefund(ctx, p, nil)
	if rollbackOK {
		p.BalanceToDeduct = 0
		p.SubDaysToDeduct = 0
	}

	_, err := s.entClient.PaymentOrder.UpdateOneID(p.OrderID).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).
		ClearRefundAt().
		SetForceRefund(p.Force).
		ClearFailedAt().
		ClearFailedReason().
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund pending: %w", err)
	}

	// Snapshot of the planned deduction: QueryAndFinalizeRefund replays it
	// once the gateway confirms the refund.
	detail := map[string]any{
		"deductionType":       p.DeductionType,
		"balanceToDeduct":     balanceDeducted,
		"subDaysToDeduct":     subDaysDeducted,
		"subscriptionID":      p.SubscriptionID,
		"gatewayBaseAmount":   p.GatewayBaseAmount,
		"gatewayAmount":       p.GatewayAmount,
		"refundFeeRate":       p.RefundFeeRate,
		"refundFeeAmount":     p.RefundFeeAmount,
		"refundID":            refundResponseID(resp),
		"refundAmount":        p.RefundAmount,
		"reason":              p.Reason,
		"force":               p.Force,
		"balanceDeducted":     p.BalanceToDeduct,
		"subDaysDeducted":     p.SubDaysToDeduct,
		"balanceRolledBack":   balanceDeducted,
		"subDaysRolledBack":   subDaysDeducted,
		"deductionRollbackOK": rollbackOK,
	}
	s.writeAuditLog(ctx, p.OrderID, "REFUND_PENDING", "admin", detail)

	warning := "gateway refund is pending confirmation"
	if !rollbackOK {
		warning += "; refund deduction rollback failed"
	}
	return &RefundResult{Success: false, RefundPending: true, Warning: warning}, nil
}

func refundResponseID(resp *payment.RefundResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.RefundID)
}

func (s *PaymentService) RollbackRefund(ctx context.Context, p *RefundPlan, gErr error) bool {
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		if err := s.userRepo.UpdateBalance(ctx, p.Order.UserID, p.BalanceToDeduct); err != nil {
			slog.Error("[CRITICAL] rollback failed", "orderID", p.OrderID, "amount", p.BalanceToDeduct, "error", err)
			s.writeAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED", "admin", map[string]any{"gatewayError": psErrMsg(gErr), "rollbackError": psErrMsg(err), "balanceDeducted": p.BalanceToDeduct})
			return false
		}
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubscriptionID > 0 {
		restoreDays := p.SubDaysToRestore
		if restoreDays <= 0 {
			restoreDays = p.SubDaysToDeduct
		}
		if restoreDays <= 0 {
			restoreDays = 1
		}
		var err error
		if p.SubExpireDayToRestore > 0 {
			err = s.subscriptionSvc.restoreSubscriptionForRefund(ctx, p.SubscriptionID, p.SubExpireDayToRestore, p.SubTodayRemainingToRestore, p.SubTodayDayToRestore)
		} else {
			_, err = s.subscriptionSvc.ExtendSubscription(ctx, p.SubscriptionID, restoreDays)
		}
		if err != nil {
			slog.Error("[CRITICAL] subscription rollback failed", "orderID", p.OrderID, "subID", p.SubscriptionID, "days", p.SubDaysToDeduct, "error", err)
			s.writeAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED", "admin", map[string]any{"gatewayError": psErrMsg(gErr), "rollbackError": psErrMsg(err), "subDaysDeducted": p.SubDaysToDeduct, "subDaysRestored": restoreDays, "subExpireDayRestored": p.SubExpireDayToRestore})
			return false
		}
	}
	return true
}

func (s *PaymentService) restoreStatus(ctx context.Context, p *RefundPlan) {
	rs := OrderStatusCompleted
	if p.Order.Status == OrderStatusRefundRequested {
		rs = OrderStatusRefundRequested
	}
	_, _ = s.entClient.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(rs).Save(ctx)
}
