package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// RenewOrderQuote 续费下单报价（只读，不改任何状态；规格第 5 节）：
// D 取自用户唯一生效卡，续 T'(整月)天，价 = cfg.Price(D, T')（恒 >0，走法币网关收全价）。
type RenewOrderQuote struct {
	SubscriptionID int64   `json:"subscription_id"`
	DailyAmountUSD float64 `json:"daily_amount_usd"`
	AddedDays      int     `json:"added_days"`
	Price          float64 `json:"price"`
	UnitPrice      float64 `json:"unit_price"`
	GroupID        int64   `json:"group_id"`
}

// QuoteRenewOrder 解析续费下单的权威参数（不锁、不改状态）。无生效卡 → ErrNoActiveSubscription（应购买）。
// 续费天数须整月（TStep 倍数）且在 [TMin,TMax]；D 取自当前卡（沿用历史值，不校验区间）。
func (s *SubscriptionService) QuoteRenewOrder(ctx context.Context, userID int64, validityDays int) (*RenewOrderQuote, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user is required")
	}
	cfg := DefaultSubscriptionPricingConfig()
	if validityDays < cfg.TMin || validityDays > cfg.TMax || (cfg.TStep > 0 && validityDays%cfg.TStep != 0) {
		return nil, infraerrors.BadRequest("INVALID_SUBSCRIPTION_PARAMS",
			fmt.Sprintf("续费天数须为 %d 的整数倍且在 [%d,%d]", cfg.TStep, cfg.TMin, cfg.TMax))
	}
	addDays := validityDays
	if addDays > MaxValidityDays {
		addDays = MaxValidityDays
	}
	// 续费可作用于「惰性过期但 status 仍 active」的最近卡（履约 GrantSubscriptionDays 会从今天起算）。
	sub, err := s.userSubRepo.GetLatestActiveStatusByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			return nil, ErrNoActiveSubscription
		}
		return nil, err
	}
	return &RenewOrderQuote{
		SubscriptionID: sub.ID,
		DailyAmountUSD: sub.DailyAmountUSD,
		AddedDays:      addDays,
		Price:          cfg.Price(sub.DailyAmountUSD, addDays),
		UnitPrice:      cfg.UnitPrice(sub.DailyAmountUSD),
		GroupID:        sub.GroupID,
	}, nil
}

// ApplyRenewFromOrder 履约续费（法币支付成功后由 doSub 调用）：把目标卡有效期延长 addDays
// （三窗口口径：只延 expires_at/expire_day，不动 D/W/M 与 usage/window_start）。**不扣任何余额**
// （续费价已通过法币网关收取，见 docs/billing-perday-redesign.md §5）。幂等由 doSub 的
// SUBSCRIPTION_SUCCESS 审计键保证。expire_day = max(原, today−1) + addDays（GrantSubscriptionDays 内即此口径并夹上限）。
func (s *SubscriptionService) ApplyRenewFromOrder(ctx context.Context, subscriptionID int64, addDays int) (*UserSubscription, error) {
	if s.entClient == nil {
		return nil, fmt.Errorf("ent client not configured")
	}
	if subscriptionID <= 0 || addDays <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "subscription and positive addDays are required")
	}
	if addDays > MaxValidityDays {
		addDays = MaxValidityDays
	}

	var userID, groupID int64
	if err := s.withSubscriptionUpdateTx(ctx, func(txCtx context.Context) error {
		now := time.Now()
		sub, err := s.userSubRepo.GetByID(txCtx, subscriptionID)
		if err != nil {
			if errors.Is(err, ErrSubscriptionNotFound) {
				return ErrSubscriptionNotFound
			}
			return err
		}
		userID = sub.UserID
		groupID = sub.GroupID
		if _, _, err := s.userSubRepo.GrantSubscriptionDays(txCtx, subscriptionID, addDays, now, now); err != nil {
			return fmt.Errorf("grant renew days: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.clearSubscriptionLockCache(userID)
	s.invalidateUserBalanceCacheAsync(userID)
	s.InvalidateSubCache(userID, groupID)
	s.invalidateSubscriptionCacheAsync(userID, groupID)
	return s.userSubRepo.GetByID(ctx, subscriptionID)
}
