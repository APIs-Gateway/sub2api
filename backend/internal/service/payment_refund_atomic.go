package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
