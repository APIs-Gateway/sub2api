package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/user"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ChangePlanOrderQuote 转套餐下单报价（只读，不改状态；规格第 7 节）。
// diff = P_新 − 旧卡剩余价值 V；diff<0=降档赔钱（调用方拒）、=0=持平（同步换卡）、>0=补差价（走网关）。
type ChangePlanOrderQuote struct {
	OldSubscriptionID int64   `json:"old_subscription_id"`
	DailyAmountUSD    float64 `json:"daily_amount_usd"`     // D_新
	ValidityDays      int     `json:"validity_days"`        // T_新
	WeeklyCapUSD      float64 `json:"weekly_cap_usd"`       // 派生 W
	MonthlyCapUSD     float64 `json:"monthly_cap_usd"`      // 派生 M
	NewPlanPrice      float64 `json:"new_plan_price"`       // P_新 = D_新×T_新×u(D_新)
	OldRemainingValue float64 `json:"old_remaining_value"`  // V = cfg.Price(D_旧, 旧卡剩余服务天数)
	Diff              float64 `json:"diff"`                 // P_新 − V（>0 走网关补差价；≤0 报价拒）
	UnitPrice         float64 `json:"unit_price"`           // u(D_新)
}

// QuoteChangePlanOrder 解析转套餐下单的权威参数（不锁、不改状态）。
// 校验：新档 D/T 走 QuoteSubscription（区间+整月）；每自然日最多转 1 次（撞则 ErrChangePlanDailyLimit）；
// 须有生效卡（否则 ErrNoActiveSubscription，应购买）。V 与退款同口径（含透支借天扣减）。
func (s *SubscriptionService) QuoteChangePlanOrder(ctx context.Context, userID int64, newDailyAmount float64, newValidityDays int) (*ChangePlanOrderQuote, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user is required")
	}
	quote, err := s.QuoteSubscription(newDailyAmount, newValidityDays)
	if err != nil {
		return nil, err
	}
	cfg := DefaultSubscriptionPricingConfig()
	today := TodayEastDayNumber()

	// 每自然日最多转 1 次（下单前置拦截，避免创建注定被履约拒的订单）。
	if s.entClient != nil {
		u, err := s.entClient.User.Query().Where(user.IDEQ(userID), user.DeletedAtIsNil()).Only(ctx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return nil, ErrUserNotFound
			}
			return nil, err
		}
		if u.LastChangePlanDay == today {
			return nil, ErrChangePlanDailyLimit
		}
	}

	oldSub, err := s.userSubRepo.GetActiveByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			return nil, ErrNoActiveSubscription
		}
		return nil, err
	}

	// 旧卡剩余价值 V（口径同退款，含透支借天扣减）：在卡副本上惰性跨天后取剩余天数。
	oldCard := oldSub.ToPerDayCard()
	oldCard.ResetIfNewDay(today)
	refundable := oldCard.RefundableDays(today)
	oldRemainingValue := cfg.Price(oldSub.DailyAmountUSD, refundable)

	diff := quote.Price - oldRemainingValue
	// 禁止赔钱降档（本会话用户决策，覆盖 spec §7 旧的「降档退差」）：diff<0 = 新档价低于旧卡剩余
	// 价值，换档会让用户净亏，报价层即拒（前端据此禁用/提示，order 层另有同码防御）。
	if diff < 0 {
		return nil, ErrChangePlanDowngradeNotAllowed
	}

	return &ChangePlanOrderQuote{
		OldSubscriptionID: oldSub.ID,
		DailyAmountUSD:    quote.DailyAmountUSD,
		ValidityDays:      quote.ValidityDays,
		WeeklyCapUSD:      quote.WeeklyCapUSD,
		MonthlyCapUSD:     quote.MonthlyCapUSD,
		NewPlanPrice:      quote.Price,
		OldRemainingValue: oldRemainingValue,
		Diff:              diff,
		UnitPrice:         quote.UnitPrice,
	}, nil
}

// ChangePlanResult 转套餐结果（规格第 7 节）。
type ChangePlanResult struct {
	OldSubscriptionID   int64   `json:"old_subscription_id"`
	NewSubscriptionID   int64   `json:"new_subscription_id"`
	OldRemainingValue   float64 `json:"old_remaining_value"`    // V = P_旧 × 剩余服务天数 / T_旧
	NewPlanPrice        float64 `json:"new_plan_price"`         // P_新 = D_新 × T_新 × u(D_新)
	Diff                float64 `json:"diff"`                   // P_新 − V：>0 已从余额扣的补差价；<0 已退进余额的差价；0 持平
	NewCardTodayBalance float64 `json:"new_card_today_balance"` // 新卡当天套餐余额 = max(0, D_新 − 旧卡今日已用)
	NewExpireDay        int     `json:"new_expire_day"`
}

// ApplyChangePlanFromOrder 履约转套餐（法币支付成功后由 doSub 调用，规格第 7 节）：关旧卡、立即开新卡
// （无 group 自定义卡 groupID=0，冻结 D_新/T_新，W/M 按 DeriveWindowCaps 派生），新卡三窗口用量**继承旧卡
// 当前用量**（堵当天/周/月换档重领），stamp last_change_plan_day=today。**不扣余额、不算差价**——补差价已
// 通过法币网关收取（见 docs/billing-perday-redesign.md §7）。幂等由 doSub 的 SUBSCRIPTION_SUCCESS 审计键保证。
//
// 单事务内：GetByID 取目标卡 → FOR UPDATE 锁 user 行 + 清假 active(lockUserAndPruneStaleForLifecycle)
// → 关旧卡 → 开新卡 → stamp last_change_plan_day，任一步失败全回滚。
func (s *SubscriptionService) ApplyChangePlanFromOrder(ctx context.Context, oldSubscriptionID int64, newDailyAmount float64, newValidityDays int) (*ChangePlanResult, error) {
	if s.entClient == nil {
		return nil, fmt.Errorf("ent client not configured")
	}
	if oldSubscriptionID <= 0 || newDailyAmount <= 0 || newValidityDays <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "old subscription and positive new D/T are required")
	}
	cfg := DefaultSubscriptionPricingConfig()
	dNew := newDailyAmount
	tNew := newValidityDays
	newPlanPrice := cfg.Price(dNew, tNew)
	weeklyLimit, monthlyLimit := DeriveWindowCaps(dNew, tNew)

	var result *ChangePlanResult
	var userID, oldGroupID int64
	if err := s.withSubscriptionUpdateTx(ctx, func(txCtx context.Context) error {
		// 取目标卡（下单时冻结的当前生效卡 ID）。
		oldSub, err := s.userSubRepo.GetByID(txCtx, oldSubscriptionID)
		if err != nil {
			if errors.Is(err, ErrSubscriptionNotFound) {
				return ErrSubscriptionNotFound
			}
			return err
		}
		userID = oldSub.UserID
		oldGroupID = oldSub.GroupID

		// LOCK-005：取锁后再按当前时间算 today/now。
		today := TodayEastDayNumber()
		now := time.Now()

		// 锁 user 行 + 清假 active 卡（与下单串行化；不报"已有卡"——转套餐本就要求有卡）。
		if err := s.lockUserAndPruneStaleForLifecycle(txCtx, userID, today); err != nil {
			return err
		}

		// 折价测算（仅供结果展示/审计；差价已在网关收取，不在此扣）。
		oldCard := oldSub.ToPerDayCard()
		oldTodaySpent := oldCard.TodaySpentFromPackage(today)
		oldCard.ResetIfNewDay(today)
		refundable := oldCard.RefundableDays(today)
		oldRemainingValue := cfg.Price(oldSub.DailyAmountUSD, refundable)
		newCardTodayBalance := ChangePlanNewCardTodayBalance(dNew, oldTodaySpent)
		newExpireDay := ClampExpireDay(today + tNew - 1)

		// 关旧卡（per-day：status=expired、today_remaining=0、expire_day=today−1；价值不回钱包）。
		if _, _, err := s.userSubRepo.CloseSubscriptionWithReclaim(txCtx, oldSub.ID, now, false); err != nil {
			return fmt.Errorf("close old subscription: %w", err)
		}

		// 开新卡：限额挂卡、新卡继承旧卡当前三窗口 usage/window_start，避免当天/本周/本月换档后重领已用额度
		// （spec §7）。新卡为无 group 自定义卡（groupID=0，限额读卡级）。不写 users.balance。
		activatedAt := now
		newSub := &UserSubscription{
			UserID:             userID,
			GroupID:            0,
			StartsAt:           now,
			ExpiresAt:          ExpireDayToExpiresAt(newExpireDay),
			Status:             SubscriptionStatusActive,
			GrantedTotalUSD:    dNew * float64(tNew),
			DailyAmountUSD:     dNew,
			DailySpentUSD:      0,
			DailySpentDay:      today,
			TodayRemaining:     newCardTodayBalance,
			TodayDay:           today,
			StartDay:           today,
			ExpireDay:          newExpireDay,
			OverdraftOn:        false,
			DailyLimitUSD:      &dNew,
			WeeklyLimitUSD:     &weeklyLimit,
			MonthlyLimitUSD:    &monthlyLimit,
			DailyUsageUSD:      oldSub.DailyUsageUSD,
			WeeklyUsageUSD:     oldSub.WeeklyUsageUSD,
			MonthlyUsageUSD:    oldSub.MonthlyUsageUSD,
			DailyWindowStart:   oldSub.DailyWindowStart,
			WeeklyWindowStart:  oldSub.WeeklyWindowStart,
			MonthlyWindowStart: oldSub.MonthlyWindowStart,
			ActivatedAt:        &activatedAt,
			AssignedAt:         now,
			Notes:              fmt.Sprintf("转套餐 → D=%.4f T=%d", dNew, tNew),
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if err := s.userSubRepo.Create(txCtx, newSub); err != nil {
			return fmt.Errorf("create new subscription: %w", err)
		}

		// 限频戳（无余额变动；差价已网关收取）。
		client := dbent.TxFromContext(txCtx).Client()
		if _, err := client.User.UpdateOneID(userID).SetLastChangePlanDay(today).Save(txCtx); err != nil {
			return fmt.Errorf("stamp change-plan day: %w", err)
		}

		result = &ChangePlanResult{
			OldSubscriptionID:   oldSub.ID,
			NewSubscriptionID:   newSub.ID,
			OldRemainingValue:   oldRemainingValue,
			NewPlanPrice:        newPlanPrice,
			Diff:                newPlanPrice - oldRemainingValue,
			NewCardTodayBalance: newCardTodayBalance,
			NewExpireDay:        newExpireDay,
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.clearSubscriptionLockCache(userID)
	s.invalidateUserBalanceCacheAsync(userID)
	s.InvalidateSubCache(userID, 0)
	s.invalidateSubscriptionCacheAsync(userID, 0)
	if oldGroupID != 0 {
		s.InvalidateSubCache(userID, oldGroupID)
		s.invalidateSubscriptionCacheAsync(userID, oldGroupID)
	}
	return result, nil
}
