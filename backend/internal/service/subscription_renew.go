package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/user"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// RenewResult 续费结果（规格第 5 节）。
type RenewResult struct {
	SubscriptionID int64
	AddedDays      int     // 本次续的自然天数 T'
	Price          float64 // 续费价 = cfg.Price(D, T')
	NewExpireDay   int     // 续后 expire_day = max(原, today−1) + T'（夹上限）
}

// RenewSubscription 续费当前生效卡（规格第 5 节）：同套餐续 T' 天、D 不变，只延长发放期、不叠加额度
// （额度每天发）。价格按续费时的公式价 cfg.Price(D, T') 重算，从其他余额扣（余额不足则拒；超余额的
// 「发起支付」路径属后续）。
//
// expire_day = max(原 expire_day, today−1) + T'：未到期从原到期日顺延（无缝）；已到期从今天起算 T' 天
// （中间断档不补）。复用 GrantSubscriptionDays（其内部即此口径并夹 MaxExpireDay）。换档（D 不同）应走转套餐。
func (s *SubscriptionService) RenewSubscription(ctx context.Context, userID, planID int64) (*RenewResult, error) {
	if s.entClient == nil {
		return nil, fmt.Errorf("ent client not configured")
	}
	if userID <= 0 || planID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user and plan are required")
	}

	plan, err := s.entClient.SubscriptionPlan.Get(ctx, planID)
	if err != nil || !plan.ForSale {
		return nil, infraerrors.NotFound("PLAN_NOT_AVAILABLE", "plan not found or not for sale")
	}
	if plan.DailyAmountUsd <= 0 {
		return nil, infraerrors.BadRequest("PLAN_DAILY_AMOUNT_INVALID", "plan daily amount is invalid")
	}
	cfg := DefaultSubscriptionPricingConfig()
	addDays := psComputeValidityDays(plan.ValidityDays, plan.ValidityUnit)
	if addDays <= 0 {
		return nil, infraerrors.BadRequest("PLAN_VALIDITY_INVALID", "plan validity days must be positive")
	}
	if addDays > MaxValidityDays {
		addDays = MaxValidityDays
	}
	today := TodayEastDayNumber()
	now := time.Now()

	var result *RenewResult
	var oldGroupID int64
	if err := s.withSubscriptionUpdateTx(ctx, func(txCtx context.Context) error {
		tx := dbent.TxFromContext(txCtx)
		client := tx.Client()
		lockRows := client.Driver() != nil && client.Driver().Dialect() == dialect.Postgres

		// 锁 user 行：余额读写原子，并与下单/转套餐串行化。
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

		// 当前生效卡。续费可作用于「惰性过期但 status 仍 active」的卡（GrantSubscriptionDays 会从今天
		// 起算），故此处不预先关假 active；仅当确无 active 卡时拒（应购买）。
		oldSub, err := s.userSubRepo.GetActiveByUserID(txCtx, userID)
		if err != nil {
			if errors.Is(err, ErrSubscriptionNotFound) {
				return ErrNoActiveSubscription
			}
			return err
		}
		oldGroupID = oldSub.GroupID

		// 续费 = 同套餐续、D 不变；D 不同应走转套餐。
		if d := plan.DailyAmountUsd - oldSub.DailyAmountUSD; d > 1e-9 || d < -1e-9 {
			return ErrRenewPlanMismatch
		}

		price := cfg.Price(oldSub.DailyAmountUSD, addDays)
		if u.Balance < price {
			return ErrInsufficientBalanceForRenew
		}

		// 扣续费价（不静默欠费）+ 延长发放期。GrantSubscriptionDays 复用本 tx、内部即续费口径并夹上限。
		if _, err := client.User.UpdateOneID(userID).AddBalance(-price).Save(txCtx); err != nil {
			return fmt.Errorf("deduct renew price: %w", err)
		}
		if _, _, err := s.userSubRepo.GrantSubscriptionDays(txCtx, oldSub.ID, addDays, now, now); err != nil {
			return fmt.Errorf("grant renew days: %w", err)
		}

		result = &RenewResult{
			SubscriptionID: oldSub.ID,
			AddedDays:      addDays,
			Price:          price,
			NewExpireDay:   RenewExpireDay(oldSub.ExpireDay, today, addDays),
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.clearSubscriptionLockCache(userID)
	s.invalidateUserBalanceCacheAsync(userID)
	s.InvalidateSubCache(userID, oldGroupID)
	s.invalidateSubscriptionCacheAsync(userID, oldGroupID)
	return result, nil
}
