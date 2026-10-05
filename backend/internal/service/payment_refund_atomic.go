package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
)

// ErrRefundBalanceInsufficient means no debit was made. A refund not yet sent
// to its provider needs explicit force approval before accepting a shortfall.
var ErrRefundBalanceInsufficient = errors.New("wallet balance is insufficient for refund clawback")

type refundBalanceDeductor interface {
	DeductRefundBalance(context.Context, int64, float64, bool) (float64, error)
}

// The no-op update locks the user before the order and subscription card on
// every supported dialect. MySQL may report zero changed rows for a no-op;
// check existence in the same transaction instead of mistaking that for a
// missing user. The matched row remains write-locked until settlement ends.
func lockRefundUser(ctx context.Context, userID int64) error {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return fmt.Errorf("refund user lock requires an ambient transaction")
	}
	_, err := tx.Client().User.Update().Where(dbuser.IDEQ(userID), dbuser.DeletedAtIsNil()).AddBalance(0).Save(ctx)
	if err != nil {
		return err
	}
	exists, err := tx.Client().User.Query().Where(dbuser.IDEQ(userID), dbuser.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrUserNotFound
	}
	return nil
}

func (s *PaymentService) deductRefundBalance(ctx context.Context, userID int64, amount float64, allowPartial bool) (float64, error) {
	repo, ok := s.userRepo.(refundBalanceDeductor)
	if !ok {
		return 0, fmt.Errorf("user repository does not support atomic refund balance deduction")
	}
	return repo.DeductRefundBalance(ctx, userID, amount, allowPartial)
}

// Use the real repositories with the ambient Ent transaction, but postpone
// cache invalidation until that transaction has committed or rolled back.
func (s *PaymentService) refundSettlementSubscriptionService() *SubscriptionService {
	if s.subscriptionSvc == nil {
		return nil
	}
	original := s.subscriptionSvc
	return &SubscriptionService{
		groupRepo:      original.groupRepo,
		userSubRepo:    original.userSubRepo,
		userRepo:       original.userRepo,
		entClient:      original.entClient,
		settingService: original.settingService,
		now:            original.now,
	}
}

func (s *PaymentService) invalidateRefundSettlementCaches(plan *RefundPlan) {
	s.invalidateRefundWalletCache(plan.Order.UserID)
	if s.subscriptionSvc == nil || plan.SubscriptionID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := s.subscriptionSvc.userSubRepo.GetByID(ctx, plan.SubscriptionID)
	if err != nil || sub == nil {
		return
	}
	s.subscriptionSvc.clearSubscriptionLockCache(sub.UserID)
	s.subscriptionSvc.InvalidateSubCache(sub.UserID, sub.GroupID)
	s.subscriptionSvc.invalidateSubscriptionCacheAsync(sub.UserID, sub.GroupID)
	s.subscriptionSvc.invalidateUserBalanceCacheAsync(sub.UserID)
}

func (s *PaymentService) invalidateRefundWalletCache(userID int64) {
	var cache *BillingCacheService
	if s.subscriptionSvc != nil {
		cache = s.subscriptionSvc.billingCacheService
	}
	if cache == nil && s.redeemService != nil {
		cache = s.redeemService.billingCacheService
	}
	if cache == nil {
		return // Both services are absent/unconfigured; no billing cache exists.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cache.clearNoSubscriptionLockCache(userID)
	_ = cache.InvalidateUserBalance(ctx, userID)
}

func (s *PaymentService) writeRefundAuditStrict(ctx context.Context, orderID int64, action string, detail map[string]any) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshal refund audit: %w", err)
	}
	_, err = s.entClientForCtx(ctx).PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(orderID, 10)).SetAction(action).SetOperator("admin").SetDetail(string(encoded)).Save(ctx)
	if err != nil {
		return fmt.Errorf("write refund audit: %w", err)
	}
	return nil
}

// Historical snapshots may omit fields, but present financial fields must not
// silently change meaning through duplicate keys or JSON null coercion.
func decodeRefundPendingSnapshot(body string, detail *refundPendingAuditDetail) error {
	decoder := json.NewDecoder(bytes.NewBufferString(body))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return fmt.Errorf("expected object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return fmt.Errorf("duplicate or invalid snapshot field %v", token)
		}
		// encoding/json accepts case-insensitive aliases for struct fields.
		// Reject those aliases before decoding so they cannot overwrite a
		// canonical financial value or bypass the null check below.
		for _, canonical := range []string{"refundID", "deductionRollbackOK", "deductionType", "balanceToDeduct", "subDaysToDeduct", "subscriptionID", "gatewayBaseAmount", "gatewayAmount", "refundFeeRate", "refundFeeAmount", "refundAmount", "subDaysToRestore", "subExpireDayToRestore", "subTodayRemainingToRestore", "subTodayDayToRestore", "subscriptionAdjustmentID"} {
			if strings.EqualFold(key, canonical) && key != canonical {
				return fmt.Errorf("non-canonical financial snapshot field %s", key)
			}
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		switch key {
		case "deductionType", "deductionRollbackOK", "balanceToDeduct", "subDaysToDeduct", "subscriptionID", "refundAmount", "gatewayBaseAmount", "gatewayAmount", "refundFeeRate", "refundFeeAmount", "subDaysToRestore", "subExpireDayToRestore", "subTodayRemainingToRestore", "subTodayDayToRestore", "subscriptionAdjustmentID":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("null financial snapshot field %s", key)
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("invalid trailing snapshot data")
	}
	return json.Unmarshal([]byte(body), detail)
}
