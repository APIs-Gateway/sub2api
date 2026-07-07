package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

// --- Refund Flow ---

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
	orderCurrency := PaymentOrderCurrency(o)
	if amt-o.Amount > paymentAmountToleranceForCurrency(orderCurrency) {
		return nil, nil, infraerrors.BadRequest("REFUND_AMOUNT_EXCEEDED", "refund amount exceeds recharge")
	}
	refundFeeRate := s.refundFeeRate(ctx)
	gatewayBase, refundFee, ga := calculateGatewayRefundBreakdown(o.Amount, o.PayAmount, amt, refundFeeRate, orderCurrency)
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
	if subID, ok := readSubscriptionSnapshotSubscriptionID(o); ok {
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
			card := sub.ToPerDayCard()
			p.SubDaysToDeduct = card.RefundableDays(today)
			// closeSubscriptionForRefund sets expire_day=today-1. Restoring the
			// original expire_day therefore needs refundableDays+1 days.
			p.SubDaysToRestore = p.SubDaysToDeduct + 1
			p.SubExpireDayToRestore = sub.ExpireDay
			p.SubTodayRemainingToRestore = sub.TodayRemaining
			p.SubTodayDayToRestore = sub.TodayDay
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
			if err := s.subscriptionSvc.closeSubscriptionForRefund(ctx, p.SubscriptionID); err != nil {
				s.restoreStatus(ctx, p)
				return nil, fmt.Errorf("close subscription for refund: %w", err)
			}
		} else {
			slog.Warn("skipping subscription deduction on retry (previous rollback failed)", "orderID", p.OrderID)
			p.SubDaysToDeduct = 0
			p.SubDaysToRestore = 0
		}
	}
	refundResp, err := s.gwRefund(ctx, p)
	if err != nil {
		return s.handleGwFail(ctx, p, err)
	}
	if refundProviderStatus(refundResp) == payment.ProviderStatusPending {
		return s.markRefundPending(ctx, p, refundResp)
	}
	return s.markRefundOk(ctx, p)
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

func refundProviderStatus(resp *payment.RefundResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.Status)
}

// getRefundProvider creates a provider using the order's original instance config.
// Delegates to getOrderProvider which handles instance lookup and fallback.
func (s *PaymentService) getRefundProvider(ctx context.Context, o *dbent.PaymentOrder) (payment.Provider, error) {
	if s.refundProviderOverride != nil {
		return s.refundProviderOverride, nil
	}
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
	now := time.Now()
	_, err := s.entClient.PaymentOrder.UpdateOneID(p.OrderID).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).
		SetForceRefund(p.Force).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund pending: %w", err)
	}
	s.writeAuditLog(ctx, p.OrderID, "REFUND_PENDING", "admin", map[string]any{
		"refundAmount":      p.RefundAmount,
		"gatewayBaseAmount": p.GatewayBaseAmount,
		"gatewayAmount":     p.GatewayAmount,
		"refundFeeRate":     p.RefundFeeRate,
		"refundFeeAmount":   p.RefundFeeAmount,
		"refundID":          strings.TrimSpace(resp.RefundID),
		"providerStatus":    strings.TrimSpace(resp.Status),
		"reason":            p.Reason,
		"deductionType":     p.DeductionType,
		"balanceDeducted":   p.BalanceToDeduct,
		"subscriptionID":    p.SubscriptionID,
		"subDaysDeducted":   p.SubDaysToDeduct,
		"subDaysToRestore":  p.SubDaysToRestore,
		"subExpireDay":      p.SubExpireDayToRestore,
		"subTodayRemaining": p.SubTodayRemainingToRestore,
		"subTodayDay":       p.SubTodayDayToRestore,
		"force":             p.Force,
	})
	return &RefundResult{
		Success:         true,
		Message:         "refund pending",
		BalanceDeducted: p.BalanceToDeduct,
		SubDaysDeducted: p.SubDaysToDeduct,
		GatewayAmount:   p.GatewayAmount,
		RefundFeeRate:   p.RefundFeeRate,
		RefundFeeAmount: p.RefundFeeAmount,
	}, nil
}

func (s *PaymentService) QueryAndFinalizeRefund(ctx context.Context, orderID int64, refundID string) (*RefundResult, error) {
	refundID = strings.TrimSpace(refundID)
	if refundID == "" {
		return nil, infraerrors.BadRequest("INVALID_REFUND_ID", "refund_id is required")
	}
	order, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	switch order.Status {
	case OrderStatusRefunded, OrderStatusPartiallyRefunded:
		return &RefundResult{Success: true, Message: "refund already finalized"}, nil
	case OrderStatusRefundPending:
	default:
		return nil, infraerrors.BadRequest("INVALID_STATUS", "order is not waiting for refund finalization")
	}

	prov, err := s.getRefundProvider(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("get refund provider: %w", err)
	}
	queryProvider, ok := prov.(payment.RefundQueryProvider)
	if !ok {
		return nil, infraerrors.BadRequest("REFUND_QUERY_UNSUPPORTED", "refund query is not supported for this provider")
	}
	resp, err := queryProvider.QueryRefund(ctx, refundID)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("payment refund query response missing")
	}

	switch refundProviderStatus(resp) {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		refundAmount := order.RefundAmount
		if refundAmount <= 0 {
			refundAmount = order.Amount
		}
		reason := ""
		if order.RefundReason != nil {
			reason = strings.TrimSpace(*order.RefundReason)
		}
		if reason == "" {
			reason = fmt.Sprintf("refund order:%d", order.ID)
		}
		return s.markRefundOk(ctx, &RefundPlan{
			OrderID:       order.ID,
			Order:         order,
			RefundAmount:  refundAmount,
			GatewayAmount: refundAmount,
			Reason:        reason,
			Force:         order.ForceRefund,
		})
	case payment.ProviderStatusPending:
		s.writeAuditLog(ctx, order.ID, "REFUND_QUERY_PENDING", "admin", map[string]any{
			"refundID":       refundID,
			"providerStatus": strings.TrimSpace(resp.Status),
		})
		return &RefundResult{Success: true, Message: "refund pending"}, nil
	case payment.ProviderStatusFailed:
		rollbackPlan, hasRollbackPlan := s.pendingRefundRollbackPlan(ctx, order)
		rollbackOK := true
		if hasRollbackPlan {
			rollbackOK = s.RollbackRefund(ctx, rollbackPlan, fmt.Errorf("payment refund query returned failed"))
		}
		now := time.Now()
		_, _ = s.entClient.PaymentOrder.UpdateOneID(order.ID).
			SetStatus(OrderStatusRefundFailed).
			SetFailedAt(now).
			SetFailedReason("payment refund query returned failed").
			Save(ctx)
		action := "REFUND_QUERY_FAILED"
		if hasRollbackPlan && rollbackOK {
			action = "REFUND_QUERY_FAILED_ROLLED_BACK"
		}
		s.writeAuditLog(ctx, order.ID, action, "admin", map[string]any{
			"refundID":       refundID,
			"providerStatus": strings.TrimSpace(resp.Status),
			"rolledBack":     hasRollbackPlan && rollbackOK,
		})
		if hasRollbackPlan && !rollbackOK {
			return nil, infraerrors.InternalServer("REFUND_FAILED_ROLLBACK_FAILED", "payment refund query returned failed and local rollback failed")
		}
		return nil, infraerrors.InternalServer("REFUND_FAILED", "payment refund query returned failed")
	default:
		return nil, fmt.Errorf("payment refund query returned unknown status: %s", strings.TrimSpace(resp.Status))
	}
}

func (s *PaymentService) pendingRefundRollbackPlan(ctx context.Context, order *dbent.PaymentOrder) (*RefundPlan, bool) {
	if s == nil || order == nil || s.entClientForCtx(ctx) == nil {
		return nil, false
	}
	log, err := s.entClientForCtx(ctx).PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)),
			paymentauditlog.ActionEQ("REFUND_PENDING"),
		).
		Order(paymentauditlog.ByCreatedAt(entsql.OrderDesc())).
		First(ctx)
	if err != nil || log == nil {
		return nil, false
	}

	detail := log.Detail
	refundAmount := gjson.Get(detail, "refundAmount").Float()
	if refundAmount <= 0 {
		refundAmount = order.RefundAmount
	}
	if refundAmount <= 0 {
		refundAmount = order.Amount
	}
	reason := strings.TrimSpace(gjson.Get(detail, "reason").String())
	if reason == "" && order.RefundReason != nil {
		reason = strings.TrimSpace(*order.RefundReason)
	}
	if reason == "" {
		reason = fmt.Sprintf("refund order:%d", order.ID)
	}

	p := &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  refundAmount,
		GatewayAmount: gjson.Get(detail, "gatewayAmount").Float(),
		Reason:        reason,
		Force:         order.ForceRefund,
	}
	if p.GatewayAmount <= 0 {
		p.GatewayAmount = refundAmount
	}

	deductionType := strings.TrimSpace(gjson.Get(detail, "deductionType").String())
	balanceDeducted := gjson.Get(detail, "balanceDeducted").Float()
	subscriptionID := gjson.Get(detail, "subscriptionID").Int()
	switch {
	case deductionType == payment.DeductionTypeBalance || balanceDeducted > 0:
		if balanceDeducted <= 0 {
			return nil, false
		}
		p.DeductionType = payment.DeductionTypeBalance
		p.BalanceToDeduct = balanceDeducted
		return p, true
	case deductionType == payment.DeductionTypeSubscription || subscriptionID > 0:
		if subscriptionID <= 0 {
			return nil, false
		}
		p.DeductionType = payment.DeductionTypeSubscription
		p.SubscriptionID = subscriptionID
		p.SubDaysToDeduct = int(gjson.Get(detail, "subDaysDeducted").Int())
		p.SubDaysToRestore = int(gjson.Get(detail, "subDaysToRestore").Int())
		p.SubExpireDayToRestore = int(gjson.Get(detail, "subExpireDay").Int())
		p.SubTodayRemainingToRestore = gjson.Get(detail, "subTodayRemaining").Float()
		p.SubTodayDayToRestore = int(gjson.Get(detail, "subTodayDay").Int())
		return p, true
	default:
		return nil, false
	}
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
