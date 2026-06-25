package service

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// subscriptionClawbackLeaderLockKey gates the periodic daily clawback so that
	// only one instance moves money per cycle in a multi-replica deployment;
	// otherwise N replicas would each clawback the same shortfall.
	subscriptionClawbackLeaderLockKey = "subscription:clawback:leader"
	// subscriptionClawbackLeaderLockTTL must exceed runOnce's worst-case runtime
	// (its 5m context timeout) so the lock never expires mid-run.
	subscriptionClawbackLeaderLockTTL = 6 * time.Minute
)

// SubscriptionClawbackService 周期性执行 burn-down 订阅的「每日清扣」。
//
// 模型：每张订阅是独立的 burn-down 账户，开通时把 G = D×天数 一次性打入用户余额。
// 规则：到该卡自己的第 N 个日历天（Asia/Shanghai），其剩余池子最多只能剩 G − N×D；
// 若消费落后（remaining > floor），把差额从订阅剩余与用户余额一并清扣（用不完作废）；
// 若消费领先（透支），不清扣。
//
// 实现：采用 1 分钟 ticker + 幂等对账（每张卡用 last_clawback_day 作游标，同一日历天只扣一次），
// 天然重启安全——即使错过 0 点，下次 tick 也会一次性补正；无事可做时空转。
type SubscriptionClawbackService struct {
	userSubRepo  UserSubscriptionRepository
	billingCache *BillingCacheService
	interval     time.Duration
	batchSize    int
	stopCh       chan struct{}
	stopOnce     sync.Once
	wg           sync.WaitGroup

	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string
}

func NewSubscriptionClawbackService(userSubRepo UserSubscriptionRepository, billingCache *BillingCacheService, interval time.Duration) *SubscriptionClawbackService {
	if interval <= 0 {
		interval = time.Minute
	}
	return &SubscriptionClawbackService{
		userSubRepo:  userSubRepo,
		billingCache: billingCache,
		interval:     interval,
		batchSize:    200,
		stopCh:       make(chan struct{}),
		instanceID:   uuid.NewString(),
	}
}

// SetLeaderLock injects the leader-lock cache and DB used to elect a single
// instance for the periodic daily clawback. When both are nil the job runs
// ungated (single-instance / test behavior).
func (s *SubscriptionClawbackService) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

// Start 已退役为 no-op：per-day 重构后 burn-down「每日清扣」不再运行。
// P4b 起结算改为 per-day 瀑布、不再维护 consumed_usd/clawed_usd，若清扣继续按 stale 账本运行会
// 异步误扣 users.balance（现已是纯钱包），与新账本冲突。service/wiring/runOnce 等待 P8 连同
// repo 清扣方法（ListActiveBurndownIDs/ClawbackSubscription）一并删除。
func (s *SubscriptionClawbackService) Start() {
	// no-op（清扣退役）
}

func (s *SubscriptionClawbackService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *SubscriptionClawbackService) runOnce() {
	// Multi-instance guard: only the leader performs the daily clawback per cycle,
	// otherwise every replica would clawback the same shortfall N×.
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 2*time.Second)
	release, ok := tryAcquireSingletonLeaderLock(lockCtx, s.lockCache, s.db, subscriptionClawbackLeaderLockKey, s.instanceID, subscriptionClawbackLeaderLockTTL)
	lockCancel()
	if !ok {
		return
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	now := time.Now()
	var afterID int64
	var clawedCount int
	var clawedTotal float64

	for {
		ids, err := s.userSubRepo.ListActiveBurndownIDs(ctx, afterID, s.batchSize)
		if err != nil {
			log.Printf("[SubscriptionClawback] list active subscriptions failed: %v", err)
			return
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			shortfall, err := s.userSubRepo.ClawbackSubscription(ctx, id, now)
			if err != nil {
				log.Printf("[SubscriptionClawback] clawback subscription %d failed: %v", id, err)
				continue
			}
			if shortfall > 0 {
				clawedCount++
				clawedTotal += shortfall
				s.invalidateUserBalanceForSub(ctx, id)
			}
		}
		afterID = ids[len(ids)-1]
		if len(ids) < s.batchSize {
			break
		}
	}

	if clawedCount > 0 {
		log.Printf("[SubscriptionClawback] clawed %d subscriptions, total $%.4f", clawedCount, clawedTotal)
	}
}

// invalidateUserBalanceForSub 清扣改变了用户余额，失效其余额缓存。
func (s *SubscriptionClawbackService) invalidateUserBalanceForSub(ctx context.Context, subID int64) {
	if s.billingCache == nil {
		return
	}
	sub, err := s.userSubRepo.GetByID(ctx, subID)
	if err != nil || sub == nil {
		return
	}
	_ = s.billingCache.InvalidateUserBalance(ctx, sub.UserID)
}
