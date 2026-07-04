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

// ManualOverdraftResult 手动透支「借一天」结果（规格第 8 节「透支」）。
type ManualOverdraftResult struct {
	SubscriptionID            int64     `json:"subscription_id"`
	NewExpiresAt              time.Time `json:"new_expires_at"`
	NewExpireDay              int       `json:"new_expire_day"`
	MonthlyOverdraftRemaining int       `json:"monthly_overdraft_remaining"` // 本月剩余可透支次数
}

// ManualOverdraft 用户手动透支「借一天」（规格第 8 节，仅解日上限、用户级月度计数）：
// 锁 user 行（月度计数）+ active 卡行（与结算亦锁卡行串行），锁内惰性按东八区月重置计数 → 引擎校验+借天
// （daily_usage 清零刷新当日额度 + expires_at/expire_day 提前 1 天 + count++）→ 落库。周/月上限照样生效。
// 「每用户每自然月最多 5 次」由同一把锁内的「校验 count<5 → 自增」原子完成，并发不被突破。
//
// 幂等：本接口未持久化 idempotency_key 去重，但靠「先按自然日重置 → 要求 daily_usage≥D」天然挡连点重复借天
// （借后 daily_usage=0 → 二次调用即 ErrOverdraftDailyNotExhausted），故重复点击不会重复借天/重复扣额度。
func (s *SubscriptionService) ManualOverdraft(ctx context.Context, userID int64) (*ManualOverdraftResult, error) {
	if s.entClient == nil {
		return nil, fmt.Errorf("ent client not configured")
	}
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user is required")
	}

	var result *ManualOverdraftResult
	var groupID int64
	if err := s.withSubscriptionUpdateTx(ctx, func(txCtx context.Context) error {
		tx := dbent.TxFromContext(txCtx)
		client := tx.Client()
		lockRows := client.Driver() != nil && client.Driver().Dialect() == dialect.Postgres

		// 先锁 user 行（月度透支计数），与续费/转套餐/下单串行（统一锁序：user 行在前）。
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

		// LOCK-005：取锁后再算 now，避免阻塞在 FOR UPDATE 上跨东八区午夜用 stale now 误判自然边界/月份。
		now := time.Now()

		// 再锁 active 卡行（与结算 settle 亦锁卡行串行，避免「透支清零 daily_usage」与「结算自增」交错）。
		sub, err := s.userSubRepo.GetLatestActiveStatusForUpdate(txCtx, userID)
		if err != nil {
			if errors.Is(err, ErrSubscriptionNotFound) {
				return mapOverdraftErr(ErrOverdraftNoActiveCard)
			}
			return err
		}
		groupID = sub.GroupID

		// 惰性按东八区月重置月度计数，再进引擎判定/自增。
		wallet := WalletState{
			MonthlyOverdraftCount: u.MonthlyOverdraftCount,
			MonthlyOverdraftMonth: u.MonthlyOverdraftMonth,
		}
		wallet.ResetMonthIfNeeded(EastMonthKey(now))

		sw := sub.ToSubWindow()
		if err := ManualOverdraftWindow(&sw, &wallet, now); err != nil {
			return mapOverdraftErr(err)
		}

		// 回写卡：引擎可能惰性重置过窗口用量/起点；daily_usage 已清零；expires_at 借天 −1。
		sub.DailyUsageUSD = sw.DailyUsageUSD
		sub.WeeklyUsageUSD = sw.WeeklyUsageUSD
		sub.MonthlyUsageUSD = sw.MonthlyUsageUSD
		sub.DailyWindowStart = sw.DailyWindowStart
		sub.WeeklyWindowStart = sw.WeeklyWindowStart
		sub.MonthlyWindowStart = sw.MonthlyWindowStart
		sub.ExpiresAt = sw.ExpiresAt
		sub.ExpireDay = ExpiresAtToExpireDay(sw.ExpiresAt) // 与 expires_at 同步，避免 expire_day 与之分裂
		if err := s.userSubRepo.ApplyManualOverdraft(txCtx, sub); err != nil {
			return err
		}

		// 回写 user 月度计数（引擎已 ++ 且 wallet 可能跨月重置过月份）。
		if _, err := client.User.UpdateOneID(userID).
			SetMonthlyOverdraftCount(wallet.MonthlyOverdraftCount).
			SetMonthlyOverdraftMonth(wallet.MonthlyOverdraftMonth).
			Save(txCtx); err != nil {
			return fmt.Errorf("update overdraft count: %w", err)
		}

		remaining := MaxMonthlyOverdraftUses - wallet.MonthlyOverdraftCount
		if remaining < 0 {
			remaining = 0
		}
		result = &ManualOverdraftResult{
			SubscriptionID:            sub.ID,
			NewExpiresAt:              sub.ExpiresAt,
			NewExpireDay:              sub.ExpireDay,
			MonthlyOverdraftRemaining: remaining,
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.clearSubscriptionLockCache(userID)
	s.InvalidateSubCache(userID, groupID)
	s.invalidateSubscriptionCacheAsync(userID, groupID)
	return result, nil
}

// MonthlyOverdraftRemaining 返回用户本月剩余可手动透支次数（惰性按东八区月重置后），供前端预置灰按钮。
// 只读、不落库（真正的跨月归零落库在 ManualOverdraft 的锁内事务里）；entClient 未配或读不到则返回错误，
// 调用方（handler）据此不填 DTO 字段，前端按 null 处理。
func (s *SubscriptionService) MonthlyOverdraftRemaining(ctx context.Context, userID int64) (int, error) {
	if s.entClient == nil {
		return 0, fmt.Errorf("ent client not configured")
	}
	u, err := s.entClient.User.Get(ctx, userID)
	if err != nil {
		return 0, err
	}
	count := u.MonthlyOverdraftCount
	if u.MonthlyOverdraftMonth != CurrentEastMonthKey() {
		count = 0 // 跨月：本月计数视为 0（惰性，不落库）
	}
	remaining := MaxMonthlyOverdraftUses - count
	if remaining < 0 {
		remaining = 0
	}
	return remaining, nil
}

// mapOverdraftErr 把引擎透支错误映射为带前端错误码的应用错误（前端据 code 本地化文案）。
func mapOverdraftErr(err error) error {
	switch {
	case errors.Is(err, ErrOverdraftNoActiveCard):
		return infraerrors.NotFound("OVERDRAFT_NO_ACTIVE_CARD", "no active subscription to overdraft")
	case errors.Is(err, ErrOverdraftDailyNotExhausted):
		return infraerrors.BadRequest("OVERDRAFT_DAILY_NOT_EXHAUSTED", "today's allowance is not used up yet")
	case errors.Is(err, ErrOverdraftMonthlyLimit):
		return infraerrors.Conflict("OVERDRAFT_MONTHLY_LIMIT", "monthly overdraft limit reached")
	case errors.Is(err, ErrOverdraftNoFutureDay):
		return infraerrors.BadRequest("OVERDRAFT_NO_FUTURE_DAY", "no future day left to borrow")
	default:
		return err
	}
}
