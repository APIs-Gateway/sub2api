package service

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

const (
	// signupRewardSweepInterval 是后台扫描周期。奖励不是实时到账的东西，
	// 同时让「每轮扫一批」的数据库开销摊得很薄。
	signupRewardSweepInterval = 5 * time.Minute
	// signupRewardSweepBatchSize 是每批取多少个待判定的被邀请人。
	signupRewardSweepBatchSize = 200
	// signupRewardSweepMaxBatchesPerRun 限制单轮最多扫几批（200×10=2000 人），
	// 剩下的靠游标留到下一轮，不会因为待判定的人太多把一轮拖成长事务或长时间占着连接。
	signupRewardSweepMaxBatchesPerRun = 10
	// signupRewardSweepRunTimeout 是单轮的总超时。
	signupRewardSweepRunTimeout = 2 * time.Minute

	signupRewardSweepLeaderLockKey = "affiliate:signup_reward:sweep:leader"
	signupRewardSweepLeaderLockTTL = 3 * time.Minute
)

// AffiliateSignupRewardSweeper 周期性地给「累计实际消费已达门槛」的被邀请人的邀请人补发注册奖励。
//
// 选后台扫描而不是在请求结算时判断：结算路径是计费热路径，生产出过行锁排队、连接占满、
// 丢计费的事故，不能在那里加一次聚合查询或一把锁。扫描只读 usage_logs、不加锁，
// 每轮有批量和批数上限、每条查询有语句超时；多实例时只有拿到 leader 锁的那个实例在扫，
// 发放本身按被邀请人幂等。
type AffiliateSignupRewardSweeper struct {
	points   *PointsService
	interval time.Duration

	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string

	cursor int64 // 只在 runOnce 里读写；runOnce 在单个 goroutine 内串行执行

	// stopCtx 在 Stop() 里被取消，runOnce 的所有查询都挂在它下面，
	// 所以关停时正在进行的那一轮会被及时打断，而不是等它跑完或等超时。
	stopCtx    context.Context
	stopCancel context.CancelFunc
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
}

// NewAffiliateSignupRewardSweeper 构造扫描器（不启动）。
func NewAffiliateSignupRewardSweeper(points *PointsService, interval time.Duration) *AffiliateSignupRewardSweeper {
	ctx, cancel := context.WithCancel(context.Background())
	return &AffiliateSignupRewardSweeper{
		points:     points,
		interval:   interval,
		instanceID: uuid.NewString(),
		stopCtx:    ctx,
		stopCancel: cancel,
		stopCh:     make(chan struct{}),
	}
}

// SetLeaderLock 注入 leader 锁，多实例部署时只让一个实例扫描。两者都为 nil 时不加门（单实例 / 测试）。
func (s *AffiliateSignupRewardSweeper) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

// Start 启动后台循环。
func (s *AffiliateSignupRewardSweeper) Start() {
	if s == nil || s.points == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.safeRunOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop 停止后台循环，幂等。会先取消正在进行的那一轮，再等 goroutine 退出。
func (s *AffiliateSignupRewardSweeper) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.stopCancel()
		close(s.stopCh)
	})
	s.wg.Wait()
}

// safeRunOnce 给一轮扫描加 panic 保护：任何 panic 只记日志，不能把整个进程带崩，
// 下一个周期照常再来。
func (s *AffiliateSignupRewardSweeper) safeRunOnce() {
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.points", "[SignupRewardSweep] recovered from panic: %v", r)
		}
	}()
	s.runOnce()
}

func (s *AffiliateSignupRewardSweeper) runOnce() {
	ctx, cancel := context.WithTimeout(s.stopCtx, signupRewardSweepRunTimeout)
	defer cancel()

	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, signupRewardSweepLeaderLockKey, s.instanceID, signupRewardSweepLeaderLockTTL)
	if !ok {
		return
	}
	defer release()

	totalGranted := 0
	for i := 0; i < signupRewardSweepMaxBatchesPerRun; i++ {
		if ctx.Err() != nil {
			break
		}
		next, granted, err := s.points.SweepSignupRewards(ctx, s.cursor, signupRewardSweepBatchSize)
		totalGranted += granted
		if err != nil {
			logger.LegacyPrintf("service.points", "[SignupRewardSweep] sweep failed after cursor=%d: %v", s.cursor, err)
			// 名单查询 / 读设置失败：停这一轮，游标不动，下一轮从同一位置重试。
			// 单个被邀请人的求和 / 发放失败不走这里（SweepSignupRewards 内部已跳过并推进游标）。
			break
		}
		s.cursor = next
		if next == 0 {
			// 扫到末尾，游标已回绕；本轮到此为止，等下一个周期再从头看。
			break
		}
	}
	if totalGranted > 0 {
		logger.LegacyPrintf("service.points", "[SignupRewardSweep] granted %d signup reward(s)", totalGranted)
	}
}
