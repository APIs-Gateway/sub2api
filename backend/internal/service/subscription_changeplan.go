package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

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

// ChangeSubscriptionPlan 把用户当前生效卡转成另一档套餐（规格第 7 节）：旧卡按剩余服务天数折出
// 剩余价值 V 抵扣新套餐，多退少补；关旧卡、立即开新卡；新卡当天套餐余额扣掉「旧卡今日已用」防套利；
// 每自然日最多转 1 次。
//
// P_旧/T_旧 取自卡本身（公式价 cfg.Price(D_旧, T_旧)、T_旧 = TotalDays()=round(G/D)），与 §7 公式
// P=D×T×u(D) 一致，无需反查购买订单。整个流程在单事务内原子完成：FOR UPDATE 锁 user 行 → 限频判定
// → 折价测算 → 关旧卡 → 开新卡 → 差额落账（AddBalance(-Diff)）+ 限频戳，任一步失败全回滚。
func (s *SubscriptionService) ChangeSubscriptionPlan(ctx context.Context, userID, newPlanID int64) (*ChangePlanResult, error) {
	if s.entClient == nil {
		return nil, fmt.Errorf("ent client not configured")
	}
	if userID <= 0 || newPlanID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user and target plan are required")
	}

	// 目标套餐校验（与下单口径一致：ForSale + D>0 + 所挂 group 存在且 active）。
	newPlan, err := s.entClient.SubscriptionPlan.Get(ctx, newPlanID)
	if err != nil || !newPlan.ForSale {
		return nil, infraerrors.NotFound("PLAN_NOT_AVAILABLE", "target plan not found or not for sale")
	}
	if newPlan.DailyAmountUsd <= 0 {
		return nil, infraerrors.BadRequest("PLAN_DAILY_AMOUNT_INVALID", "target plan daily amount is invalid")
	}
	if grp, gerr := s.groupRepo.GetByID(ctx, newPlan.GroupID); gerr != nil || grp == nil || grp.Status != StatusActive {
		return nil, infraerrors.NotFound("GROUP_NOT_FOUND", "target subscription group is no longer available")
	}

	cfg := DefaultSubscriptionPricingConfig()
	dNew := newPlan.DailyAmountUsd
	tNew := psComputeValidityDays(newPlan.ValidityDays, newPlan.ValidityUnit)
	// 防御：存量脏 plan 或直接 DB 写入可能给出 validity_days<=0 → tNew<=0 → P_新=0、expire_day=today-1
	// 的废卡（甚至给用户退差价）。入口校验 tNew>0 并夹到上限（与 createSubscription 同口径）。
	if tNew <= 0 {
		return nil, infraerrors.BadRequest("PLAN_VALIDITY_INVALID", "target plan validity days must be positive")
	}
	if tNew > MaxValidityDays {
		tNew = MaxValidityDays
	}

	var result *ChangePlanResult
	var oldGroupID int64
	staleGroupIDs := make(map[int64]struct{})
	var noActiveAfterPrune bool
	if err := s.withSubscriptionUpdateTx(ctx, func(txCtx context.Context) error {
		tx := dbent.TxFromContext(txCtx)
		client := tx.Client()
		lockRows := client.Driver() != nil && client.Driver().Dialect() == dialect.Postgres

		// 锁 user 行：限频判定 + 余额读写原子，并与下单/购买的 enforceSingleActiveSubscription 串行化。
		uq := client.User.Query().Where(user.IDEQ(userID), user.DeletedAtIsNil())
		if lockRows {
			uq = uq.ForUpdate()
		}
		u, err := uq.Only(txCtx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return ErrUserNotFound
			}
			return err
		}

		// LOCK-005：取锁后再按当前时间算 today/now，避免阻塞在 FOR UPDATE 上跨东八区午夜用 stale today
		// （会误判限频/折价/expire 口径）。
		today := TodayEastDayNumber()
		now := time.Now()

		// 每自然日最多转 1 次。
		if u.LastChangePlanDay == today {
			return ErrChangePlanDailyLimit
		}

		// 先惰性关掉「假 active」卡（expire_day<today 但 status 仍 active），与购买/续费的规格 §207
		// 步骤① 对齐。否则对已过期卡执行转套餐会按 V=0 全价"换新"；正确语义是已无生效卡 → 应购买新卡，
		// 故关掉假 active 后 GetActiveByUserID 返回空 → ErrNoActiveSubscription。
		staleQuery := client.UserSubscription.Query().
			Where(
				usersubscription.UserIDEQ(userID),
				usersubscription.StatusEQ(SubscriptionStatusActive),
				usersubscription.DeletedAtIsNil(),
				usersubscription.ExpireDayLT(today),
			)
		if lockRows {
			staleQuery = staleQuery.ForUpdate()
		}
		staleSubs, err := staleQuery.All(txCtx)
		if err != nil {
			return err
		}
		for _, sub := range staleSubs {
			if gid := entGroupIDValue(sub.GroupID); gid != 0 {
				staleGroupIDs[gid] = struct{}{}
			}
		}
		if _, err := client.UserSubscription.Update().
			Where(
				usersubscription.UserIDEQ(userID),
				usersubscription.StatusEQ(SubscriptionStatusActive),
				usersubscription.DeletedAtIsNil(),
				usersubscription.ExpireDayLT(today),
			).
			SetStatus(SubscriptionStatusExpired).
			SetTodayRemaining(0).
			SetUpdatedAt(time.Now()).
			Save(txCtx); err != nil {
			return err
		}

		// 当前生效卡（per-day 单卡）。
		oldSub, err := s.userSubRepo.GetActiveByUserID(txCtx, userID)
		if err != nil {
			if errors.Is(err, ErrSubscriptionNotFound) {
				noActiveAfterPrune = true
				return nil
			}
			return err
		}

		oldGroupID = oldSub.GroupID

		// 折价测算：旧卡今日已用要先从原始快照读取。存量/迁移卡可能只有 today_remaining
		// 已对齐今天、daily_spent_day 仍未对齐；若先 ResetIfNewDay，会把 daily_spent_usd 清 0，
		// 导致转套餐当天重复领取 D。
		oldCard := oldSub.ToPerDayCard()
		oldTodaySpent := oldCard.TodaySpentFromPackage(today)
		// 再惰性跨天，取准确的剩余天数。
		oldCard.ResetIfNewDay(today)
		refundable := oldCard.RefundableDays(today)

		// 旧卡剩余价值 V 用 renew-stable 形式直接算：
		//   V = P_旧 × refundable / T_旧 = cfg.Price(D_旧,T_旧) × refundable / T_旧 = cfg.Price(D_旧, refundable)
		// T_旧 代数消去——避免依赖脆弱的 TotalDays()=round(G/D)：续费会增大 G 使 T_旧 变成累计天数、
		// 进而 V 被低估、用户转套餐被多收差价。直接按公式价折剩余天数即可（与 §7 P=D×T×u(D) 一致）。
		oldRemainingValue := cfg.Price(oldSub.DailyAmountUSD, refundable)
		newPlanPrice := cfg.Price(dNew, tNew)
		diff := newPlanPrice - oldRemainingValue
		newCardTodayBalance := ChangePlanNewCardTodayBalance(dNew, oldTodaySpent)
		newExpireDay := ClampExpireDay(today + tNew - 1)

		// 补差价需余额足额，不静默扣成负数（欠费）。
		if diff > 0 && u.Balance < diff {
			return ErrInsufficientBalanceForChangePlan
		}

		// 关旧卡（per-day：status=expired、today_remaining=0、expire_day=today−1；价值不回钱包，已折进差额）。
		if _, _, err := s.userSubRepo.CloseSubscriptionWithReclaim(txCtx, oldSub.ID, now, false); err != nil {
			return fmt.Errorf("close old subscription: %w", err)
		}

		// 开新卡：start_day=today、expire_day=today+T_新−1、today_remaining=max(0,D_新−旧卡今日已用)；
		// 三窗口模型下，限额挂卡且新卡继承旧卡当前 usage/window_start，避免当天/本周/本月换档后
		// 重新领取已用额度（spec §7）。不写 users.balance。
		activatedAt := now
		weeklyLimit, monthlyLimit := DeriveWindowCaps(dNew, tNew)
		newSub := &UserSubscription{
			UserID:             userID,
			GroupID:            newPlan.GroupID,
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
			Notes:              fmt.Sprintf("转套餐 → plan %d", newPlanID),
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if err := s.userSubRepo.Create(txCtx, newSub); err != nil {
			return fmt.Errorf("create new subscription: %w", err)
		}

		// 差额落账 + 限频戳一次写：AddBalance(-Diff)（Diff>0 扣补差价 / <0 退差价 / 0 不动），
		// 同时记 last_change_plan_day=today。
		if _, err := client.User.UpdateOneID(userID).
			AddBalance(-diff).
			SetLastChangePlanDay(today).
			Save(txCtx); err != nil {
			return fmt.Errorf("settle change-plan diff: %w", err)
		}

		result = &ChangePlanResult{
			OldSubscriptionID:   oldSub.ID,
			NewSubscriptionID:   newSub.ID,
			OldRemainingValue:   oldRemainingValue,
			NewPlanPrice:        newPlanPrice,
			Diff:                diff,
			NewCardTodayBalance: newCardTodayBalance,
			NewExpireDay:        newExpireDay,
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if noActiveAfterPrune {
		s.clearSubscriptionLockCache(userID)
		s.invalidateUserBalanceCacheAsync(userID)
		for groupID := range staleGroupIDs {
			s.InvalidateSubCache(userID, groupID)
			s.invalidateSubscriptionCacheAsync(userID, groupID)
		}
		return nil, ErrNoActiveSubscription
	}

	s.clearSubscriptionLockCache(userID)
	s.invalidateUserBalanceCacheAsync(userID)
	s.InvalidateSubCache(userID, newPlan.GroupID)
	s.invalidateSubscriptionCacheAsync(userID, newPlan.GroupID)
	// 旧卡所属 group 的订阅缓存也要失效，否则 /v1/usage、通知、GetSubscriptionStatus(user, oldGroup)
	// 等路径短时间内仍可能看到旧组 active。
	if oldGroupID != 0 && oldGroupID != newPlan.GroupID {
		s.InvalidateSubCache(userID, oldGroupID)
		s.invalidateSubscriptionCacheAsync(userID, oldGroupID)
	}
	return result, nil
}
