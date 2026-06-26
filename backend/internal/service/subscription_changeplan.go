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
	OldSubscriptionID   int64
	NewSubscriptionID   int64
	OldRemainingValue   float64 // V = P_旧 × 剩余服务天数 / T_旧
	NewPlanPrice        float64 // P_新 = D_新 × T_新 × u(D_新)
	Diff                float64 // P_新 − V：>0 已从余额扣的补差价；<0 已退进余额的差价；0 持平
	NewCardTodayBalance float64 // 新卡当天套餐余额 = max(0, D_新 − 旧卡今日已用)
	NewExpireDay        int
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
	today := TodayEastDayNumber()
	now := time.Now()

	var result *ChangePlanResult
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

		// 每自然日最多转 1 次。
		if u.LastChangePlanDay == today {
			return ErrChangePlanDailyLimit
		}

		// 先惰性关掉「假 active」卡（expire_day<today 但 status 仍 active），与购买/续费的规格 §207
		// 步骤① 对齐。否则对已过期卡执行转套餐会按 V=0 全价"换新"；正确语义是已无生效卡 → 应购买新卡，
		// 故关掉假 active 后 GetActiveByUserID 返回空 → ErrNoActiveSubscription。
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
				return ErrNoActiveSubscription
			}
			return err
		}

		// 折价测算：先惰性跨天，取准确的旧卡今日已用与剩余天数。
		oldCard := oldSub.ToPerDayCard()
		oldCard.ResetIfNewDay(today)
		tOld := oldSub.TotalDays()
		pOld := cfg.Price(oldSub.DailyAmountUSD, tOld)
		quote := QuoteChangePlan(cfg, pOld, oldCard.RefundableDays(today), tOld, dNew, tNew, oldCard.TodaySpentFromPackage(today), today)

		// 补差价需余额足额，不静默扣成负数（欠费）。
		if quote.Diff > 0 && u.Balance < quote.Diff {
			return ErrInsufficientBalanceForChangePlan
		}

		// 关旧卡（per-day：status=expired、today_remaining=0、expire_day=today−1；价值不回钱包，已折进差额）。
		if _, _, err := s.userSubRepo.CloseSubscriptionWithReclaim(txCtx, oldSub.ID, now, false); err != nil {
			return fmt.Errorf("close old subscription: %w", err)
		}

		// 开新卡：start_day=today、expire_day=today+T_新−1、today_remaining=max(0,D_新−旧卡今日已用)；
		// 次日起惰性重置为 D_新。不写 users.balance（per-day 额度只存 today_remaining）。
		activatedAt := now
		newSub := &UserSubscription{
			UserID:          userID,
			GroupID:         newPlan.GroupID,
			StartsAt:        now,
			ExpiresAt:       ExpireDayToExpiresAt(quote.NewCardExpireDay),
			Status:          SubscriptionStatusActive,
			GrantedTotalUSD: dNew * float64(tNew),
			DailyAmountUSD:  dNew,
			DailySpentUSD:   0,
			DailySpentDay:   today,
			TodayRemaining:  quote.NewCardTodayBalance,
			TodayDay:        today,
			StartDay:        today,
			ExpireDay:       quote.NewCardExpireDay,
			OverdraftOn:     false,
			ActivatedAt:     &activatedAt,
			AssignedAt:      now,
			Notes:           fmt.Sprintf("转套餐 → plan %d", newPlanID),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := s.userSubRepo.Create(txCtx, newSub); err != nil {
			return fmt.Errorf("create new subscription: %w", err)
		}

		// 差额落账 + 限频戳一次写：AddBalance(-Diff)（Diff>0 扣补差价 / <0 退差价 / 0 不动），
		// 同时记 last_change_plan_day=today。
		if _, err := client.User.UpdateOneID(userID).
			AddBalance(-quote.Diff).
			SetLastChangePlanDay(today).
			Save(txCtx); err != nil {
			return fmt.Errorf("settle change-plan diff: %w", err)
		}

		result = &ChangePlanResult{
			OldSubscriptionID:   oldSub.ID,
			NewSubscriptionID:   newSub.ID,
			OldRemainingValue:   quote.OldRemainingValue,
			NewPlanPrice:        quote.NewPlanPrice,
			Diff:                quote.Diff,
			NewCardTodayBalance: quote.NewCardTodayBalance,
			NewExpireDay:        quote.NewCardExpireDay,
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.clearSubscriptionLockCache(userID)
	s.invalidateUserBalanceCacheAsync(userID)
	s.InvalidateSubCache(userID, newPlan.GroupID)
	return result, nil
}
