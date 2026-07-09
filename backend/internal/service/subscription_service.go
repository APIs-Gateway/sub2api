package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/dgraph-io/ristretto"
	"golang.org/x/sync/singleflight"
)

// MaxExpiresAt is the maximum allowed expiration date (year 2099)
// This prevents time.Time JSON serialization errors (RFC 3339 requires year <= 9999)
var MaxExpiresAt = time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)

// MaxValidityDays is the maximum allowed validity days for subscriptions (100 years)
const MaxValidityDays = 36500

var (
	ErrSubscriptionNotFound      = infraerrors.NotFound("SUBSCRIPTION_NOT_FOUND", "subscription not found")
	ErrSubscriptionExpired       = infraerrors.Forbidden("SUBSCRIPTION_EXPIRED", "subscription has expired")
	ErrSubscriptionSuspended     = infraerrors.Forbidden("SUBSCRIPTION_SUSPENDED", "subscription is suspended")
	ErrSubscriptionAlreadyExists = infraerrors.Conflict("SUBSCRIPTION_ALREADY_EXISTS", "subscription already exists for this user and group")
	ErrActiveSubscriptionExists  = infraerrors.Conflict("ACTIVE_SUBSCRIPTION_EXISTS", "active subscription already exists; renew, refund, or change plan instead")
	ErrNoActiveSubscription      = infraerrors.NotFound("NO_ACTIVE_SUBSCRIPTION", "no active subscription to change")
	ErrChangePlanDailyLimit      = infraerrors.TooManyRequests("CHANGE_PLAN_DAILY_LIMIT", "plan can be changed at most once per natural day")
	// 转套餐降档赔钱（新档折价后 diff<0，即旧卡剩余价值 > 新档价）禁止——只允许持平/升档（diff≥0）。
	// 见 docs/billing-perday-redesign.md §7：差价走法币网关补，不退款（产品决策：禁止赔钱降档）。
	ErrChangePlanDowngradeNotAllowed = infraerrors.BadRequest("CHANGE_PLAN_DOWNGRADE_NOT_ALLOWED", "cannot change to a plan worth less than the current card's remaining value")
	ErrSubscriptionAssignConflict    = infraerrors.Conflict("SUBSCRIPTION_ASSIGN_CONFLICT", "subscription exists but request conflicts with existing assignment semantics")
	ErrGroupNotSubscriptionType      = infraerrors.BadRequest("GROUP_NOT_SUBSCRIPTION_TYPE", "group is not a subscription type")
	ErrInvalidDailyAmount            = infraerrors.BadRequest("INVALID_DAILY_AMOUNT", "subscription daily amount must be positive (provide daily_amount_usd, or a group with daily_limit_usd > 0)")
	ErrInvalidInput                  = infraerrors.BadRequest("INVALID_INPUT", "at least one of resetDaily, resetWeekly, or resetMonthly must be true")
	ErrDailyLimitExceeded            = infraerrors.TooManyRequests("DAILY_LIMIT_EXCEEDED", "daily usage limit exceeded")
	ErrWeeklyLimitExceeded           = infraerrors.TooManyRequests("WEEKLY_LIMIT_EXCEEDED", "weekly usage limit exceeded")
	ErrMonthlyLimitExceeded          = infraerrors.TooManyRequests("MONTHLY_LIMIT_EXCEEDED", "monthly usage limit exceeded")
	ErrSubscriptionNilInput          = infraerrors.BadRequest("SUBSCRIPTION_NIL_INPUT", "subscription input cannot be nil")
	ErrAdjustWouldExpire             = infraerrors.BadRequest("ADJUST_WOULD_EXPIRE", "adjustment would result in expired subscription (remaining days must be > 0)")
)

// SubscriptionService 订阅服务
type SubscriptionService struct {
	groupRepo            GroupRepository
	userSubRepo          UserSubscriptionRepository
	userRepo             UserRepository
	billingCacheService  *BillingCacheService
	authCacheInvalidator APIKeyAuthCacheInvalidator
	entClient            *dbent.Client
	settingService       *SettingService

	// L1 缓存：加速中间件热路径的订阅查询
	subCacheL1     *ristretto.Cache
	subCacheGroup  singleflight.Group
	subCacheTTL    time.Duration
	subCacheJitter int // 抖动百分比

	maintenanceQueue *SubscriptionMaintenanceQueue
}

// NewSubscriptionService 创建订阅服务
func NewSubscriptionService(groupRepo GroupRepository, userSubRepo UserSubscriptionRepository, userRepo UserRepository, billingCacheService *BillingCacheService, authCacheInvalidator APIKeyAuthCacheInvalidator, entClient *dbent.Client, settingService *SettingService, cfg *config.Config) *SubscriptionService {
	svc := &SubscriptionService{
		groupRepo:            groupRepo,
		userSubRepo:          userSubRepo,
		userRepo:             userRepo,
		billingCacheService:  billingCacheService,
		authCacheInvalidator: authCacheInvalidator,
		entClient:            entClient,
		settingService:       settingService,
	}
	svc.initSubCache(cfg)
	svc.initMaintenanceQueue(cfg)
	return svc
}

func (s *SubscriptionService) initMaintenanceQueue(cfg *config.Config) {
	if cfg == nil {
		return
	}
	mc := cfg.SubscriptionMaintenance
	if mc.WorkerCount <= 0 || mc.QueueSize <= 0 {
		return
	}
	s.maintenanceQueue = NewSubscriptionMaintenanceQueue(mc.WorkerCount, mc.QueueSize)
}

// Stop stops the maintenance worker pool.
func (s *SubscriptionService) Stop() {
	if s == nil {
		return
	}
	if s.maintenanceQueue != nil {
		s.maintenanceQueue.Stop()
	}
}

// initSubCache 初始化订阅 L1 缓存
func (s *SubscriptionService) initSubCache(cfg *config.Config) {
	if cfg == nil {
		return
	}
	sc := cfg.SubscriptionCache
	if sc.L1Size <= 0 || sc.L1TTLSeconds <= 0 {
		return
	}
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: int64(sc.L1Size) * 10,
		MaxCost:     int64(sc.L1Size),
		BufferItems: 64,
	})
	if err != nil {
		log.Printf("Warning: failed to init subscription L1 cache: %v", err)
		return
	}
	s.subCacheL1 = cache
	s.subCacheTTL = time.Duration(sc.L1TTLSeconds) * time.Second
	s.subCacheJitter = sc.JitterPercent
}

// subCacheKey 生成订阅缓存 key（热路径，避免 fmt.Sprintf 开销）
func subCacheKey(userID, groupID int64) string {
	return "sub:" + strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(groupID, 10)
}

// jitteredTTL 为 TTL 添加抖动，避免集中过期
func (s *SubscriptionService) jitteredTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 || s.subCacheJitter <= 0 {
		return ttl
	}
	pct := s.subCacheJitter
	if pct > 100 {
		pct = 100
	}
	delta := float64(pct) / 100
	factor := 1 - delta + rand.Float64()*(2*delta)
	if factor <= 0 {
		return ttl
	}
	return time.Duration(float64(ttl) * factor)
}

// InvalidateSubCache 失效指定用户+分组的订阅 L1 缓存
func (s *SubscriptionService) InvalidateSubCache(userID, groupID int64) {
	if s.subCacheL1 == nil {
		return
	}
	s.subCacheL1.Del(subCacheKey(userID, groupID))
}

// AssignSubscriptionInput 分配订阅输入
type AssignSubscriptionInput struct {
	UserID         int64
	GroupID        int64
	ValidityDays   int
	DailyAmountUSD float64 // per-day：每日额度 D（来自订单快照/plan）；0 时回退 group.daily_limit_usd
	// 周/月封顶（来自订单**冻结快照**）；>0 则覆盖 DeriveWindowCaps(D,T) 的派生值——spec §2 要求
	// W/M 与 D/T/u/price 一并冻结，发卡严格按快照、不按履约时派生系数重算。0（老单/直发）= 按 D/T 派生。
	WeeklyLimitUSD  float64
	MonthlyLimitUSD float64
	AssignedBy      int64
	Notes           string
}

// AssignSubscription 分配订阅给用户（不允许重复分配）
func (s *SubscriptionService) AssignSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	sub, _, err := s.assignSubscriptionWithReuse(ctx, input)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// AssignOrExtendSubscription 分配或续期订阅（用于兑换码等场景）
// 如果用户已有同分组的订阅：
//   - 未过期：从当前过期时间累加天数
//   - 已过期：从当前时间开始计算新的过期时间，并激活订阅
//
// 如果没有订阅：创建新订阅
func (s *SubscriptionService) AssignOrExtendSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	// per-day→三窗口 redesign（P5e）：订阅与 group 解耦，限额读卡级 *_limit_usd，订阅可在任意 group 下使用。
	//   - GroupID>0：历史按 group 分配/兑换/套餐单，仍校验来源 group 存在（写入卡作历史快照）。
	//   - GroupID==0：自定义 D+T 购买的无 group 卡（group_id 现已可空），必须自带每日额度 D
	//     （否则 resolveAssignDailyAmount 既无 D 又无 group 可回退 → ErrInvalidDailyAmount）。
	if input.GroupID > 0 {
		if _, err := s.groupRepo.GetByID(ctx, input.GroupID); err != nil {
			return nil, false, fmt.Errorf("group not found: %w", err)
		}
	} else if input.DailyAmountUSD <= 0 {
		return nil, false, infraerrors.BadRequest("INVALID_INPUT", "custom subscription requires daily_amount_usd")
	}

	// per-day：每次开通新建一张 per-day 卡（单卡模式由购买入口保证至多一张 active）。
	sub, err := s.createSubscription(ctx, input)
	if err != nil {
		return nil, false, err
	}

	// 失效订阅缓存
	s.InvalidateSubCache(input.UserID, input.GroupID)
	s.invalidateSubscriptionCacheAsync(input.UserID, input.GroupID)

	return sub, false, nil // 始终为新建
}

func (s *SubscriptionService) withSubscriptionUpdateTx(ctx context.Context, fn func(context.Context) error) error {
	if s.entClient == nil {
		return fn(ctx)
	}

	// 已处于外层事务中（如兑换码流程在自己的 tx 内调用本服务）时，直接复用该事务，
	// 否则会在同一连接上嵌套开启新事务，导致死锁（尤其 SQLite 单写者）。
	if dbent.TxFromContext(ctx) != nil {
		return fn(ctx)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	txCtx := dbent.NewTxContext(ctx, tx)

	if err := fn(txCtx); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// createSubscription 创建新订阅（内部方法）
// resolveAssignDailyAmount 决定新卡的每日额度 D：input.DailyAmountUSD>0 优先（来自订单冻结快照/plan）；
// 否则回退所挂 group 的 daily_limit_usd（须 > 0）——存量/管理端按 group 直接分配的兼容路径。
// 二者都拿不到正数则返回 BadRequest，绝不建 D=0 的 active 卡。
func (s *SubscriptionService) resolveAssignDailyAmount(ctx context.Context, input *AssignSubscriptionInput) (float64, error) {
	if input.DailyAmountUSD > 0 {
		return input.DailyAmountUSD, nil
	}
	if input.GroupID > 0 {
		group, err := s.groupRepo.GetByID(ctx, input.GroupID)
		if err != nil {
			// 不要把 DB 抖动等内部错误吞成 INVALID_DAILY_AMOUNT，返回原始错误。
			return 0, fmt.Errorf("resolve daily amount: group lookup: %w", err)
		}
		if group != nil && group.DailyLimitUSD != nil && *group.DailyLimitUSD > 0 {
			return *group.DailyLimitUSD, nil
		}
	}
	return 0, ErrInvalidDailyAmount
}

func (s *SubscriptionService) createSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	s.clearSubscriptionLockCache(input.UserID)

	validityDays := input.ValidityDays
	if validityDays <= 0 {
		validityDays = 30
	}
	if validityDays > MaxValidityDays {
		validityDays = MaxValidityDays
	}

	// per-day：每日额度 D 必须 > 0，绝不建 0-D 的 active 卡（否则今日额度恒 0、卡形同废卡）。
	dailyAmount, err := s.resolveAssignDailyAmount(ctx, input)
	if err != nil {
		return nil, err
	}
	grantedTotal := dailyAmount * float64(validityDays)

	now := time.Now()

	// per-day 字段：新卡即按 per-day 模型初始化（与上方 burn-down 字段并存，切换后即生效）。
	// start_day/expire_day 为东八区绝对自然日序号；expire_day = 最后发放日（含）= start+T−1；
	// 当天即发放 D（today_remaining=D、today_day=start_day）。切换前 burn-down 不读这些字段，纯加性。
	startDay := EastDayNumber(now)
	expireDay := startDay + validityDays - 1
	// 超长有效期：先把 expire_day 夹到上限，再派生 expires_at——否则只 clamp expiresAt 会让
	// expire_day 与 expires_at 再次分裂（per-day/退款看 expire_day、旧 active/到期路径看 expires_at）。
	expireDay = ClampExpireDay(expireDay)
	// expires_at 从 expire_day 派生（次日 0 点），与自然日口径一致，供按 expires_at 判过期的旧路径。
	expiresAt := ExpireDayToExpiresAt(expireDay)

	activatedAt := now
	// 优先用订单冻结快照里的 W/M（spec §2：W/M 也冻结，发卡不按履约时派生系数重算）；
	// 快照缺省（老单/直发/redeem）时回退按 D/T 派生（派生系数为常量，结果与下单时一致）。
	weeklyLimit, monthlyLimit := DeriveWindowCaps(dailyAmount, validityDays)
	if input.WeeklyLimitUSD > 0 {
		weeklyLimit = input.WeeklyLimitUSD
	}
	if input.MonthlyLimitUSD > 0 {
		monthlyLimit = input.MonthlyLimitUSD
	}
	dailyWindowStart := timezone.StartOfDay(now)
	weeklyWindowStart := timezone.StartOfWeek(now)
	monthlyWindowStart := timezone.StartOfMonth(now)
	sub := &UserSubscription{
		UserID:             input.UserID,
		GroupID:            input.GroupID,
		StartsAt:           now,
		ExpiresAt:          expiresAt,
		Status:             SubscriptionStatusActive,
		GrantedTotalUSD:    grantedTotal,
		DailyAmountUSD:     dailyAmount,
		ConsumedUSD:        0,
		ClawedUSD:          0,
		LastClawbackDay:    0,
		DailySpentUSD:      0,
		DailySpentDay:      startDay,
		TodayRemaining:     dailyAmount,
		TodayDay:           startDay,
		StartDay:           startDay,
		ExpireDay:          expireDay,
		OverdraftOn:        false,
		DailyLimitUSD:      &dailyAmount,
		WeeklyLimitUSD:     &weeklyLimit,
		MonthlyLimitUSD:    &monthlyLimit,
		DailyUsageUSD:      0,
		WeeklyUsageUSD:     0,
		MonthlyUsageUSD:    0,
		DailyWindowStart:   &dailyWindowStart,
		WeeklyWindowStart:  &weeklyWindowStart,
		MonthlyWindowStart: &monthlyWindowStart,
		ActivatedAt:        &activatedAt,
		AssignedAt:         now,
		Notes:              input.Notes,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	// 只有当 AssignedBy > 0 时才设置（0 表示系统分配，如兑换码）
	if input.AssignedBy > 0 {
		sub.AssignedBy = &input.AssignedBy
	}

	// per-day 模型：套餐额度只存在卡的 today_remaining（每日发 D），**不再把 G 打进 users.balance**。
	// 否则套餐额度会同时存在于 today_remaining 与钱包，用户当天花完 D 后还能用这笔总额走钱包层、
	// 绕过 per-day 限速/透支（见 settlePerDaySubscription：balance 已是纯钱包）。GrantedTotalUSD 仅
	// 作历史/展示，不进账本。存量旧卡的 G 已在 balance 里，由上线前一次性「balance 解混」迁移取出。
	if err := s.withSubscriptionUpdateTx(ctx, func(txCtx context.Context) error {
		if err := s.enforceSingleActiveSubscription(txCtx, input.UserID, startDay); err != nil {
			return err
		}
		if err := s.userSubRepo.Create(txCtx, sub); err != nil {
			return err
		}
		return s.bumpUserConcurrencyForSubscription(txCtx, input.UserID, dailyAmount)
	}); err != nil {
		return nil, err
	}
	s.clearSubscriptionLockCache(input.UserID)

	// 开通即时改变可用余额，失效余额缓存
	s.invalidateUserBalanceCacheAsync(input.UserID)

	// 重新获取完整订阅信息（包含关联）
	return s.userSubRepo.GetByID(ctx, sub.ID)
}

func subscriptionConcurrencyForDailyAmount(dailyAmount float64) int {
	if dailyAmount <= 0 || math.IsNaN(dailyAmount) || math.IsInf(dailyAmount, 0) {
		return 0
	}
	return int(math.Ceil(dailyAmount / 10))
}

func (s *SubscriptionService) bumpUserConcurrencyForSubscription(ctx context.Context, userID int64, dailyAmount float64) error {
	target := subscriptionConcurrencyForDailyAmount(dailyAmount)
	if target <= 0 || s.userRepo == nil {
		return nil
	}
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.Concurrency >= target {
		return nil
	}
	if err := s.userRepo.UpdateConcurrency(ctx, userID, target-u.Concurrency); err != nil {
		return err
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	return nil
}

func (s *SubscriptionService) enforceSingleActiveSubscription(ctx context.Context, userID int64, today int) error {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return nil
	}
	client := tx.Client()
	lockRows := client.Driver() != nil && client.Driver().Dialect() == dialect.Postgres

	userQuery := client.User.Query().
		Where(user.IDEQ(userID), user.DeletedAtIsNil())
	if lockRows {
		userQuery = userQuery.ForUpdate()
	}
	if _, err := userQuery.OnlyID(ctx); err != nil {
		if dbent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
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
		Save(ctx); err != nil {
		return err
	}

	activeQuery := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.StatusEQ(SubscriptionStatusActive),
			usersubscription.DeletedAtIsNil(),
		).
		Order(
			dbent.Desc(usersubscription.FieldExpireDay),
			dbent.Desc(usersubscription.FieldID),
		)
	if lockRows {
		activeQuery = activeQuery.ForUpdate()
	}
	if _, err := activeQuery.OnlyID(ctx); err == nil {
		return ErrActiveSubscriptionExists
	} else if !dbent.IsNotFound(err) {
		return err
	}
	return nil
}

// lockUserAndPruneStaleForLifecycle 为续费/转套餐下单串行化：FOR UPDATE 锁 user 行 + 惰性关掉
// 假 active 卡（status='active' 但 expire_day<today）。**与 enforceSingleActiveSubscription 不同**:
// 不因「仍有生效卡」报错——续费/转套餐本就要求有生效卡。须在事务内调用（tx 来自 ctx）。
func (s *SubscriptionService) lockUserAndPruneStaleForLifecycle(ctx context.Context, userID int64, today int) error {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return nil
	}
	client := tx.Client()
	lockRows := client.Driver() != nil && client.Driver().Dialect() == dialect.Postgres

	userQuery := client.User.Query().Where(user.IDEQ(userID), user.DeletedAtIsNil())
	if lockRows {
		userQuery = userQuery.ForUpdate()
	}
	if _, err := userQuery.OnlyID(ctx); err != nil {
		if dbent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
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
		Save(ctx); err != nil {
		return err
	}
	return nil
}

// invalidateUserBalanceCacheAsync 异步失效用户余额缓存（与现有订阅缓存失效模式一致）。
func (s *SubscriptionService) invalidateUserBalanceCacheAsync(userID int64) {
	if s.billingCacheService == nil {
		return
	}
	s.billingCacheService.clearNoSubscriptionLockCache(userID)
	go func() {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.billingCacheService.InvalidateUserBalance(cacheCtx, userID)
	}()
}

func (s *SubscriptionService) clearSubscriptionLockCache(userID int64) {
	if s.billingCacheService == nil {
		return
	}
	s.billingCacheService.clearNoSubscriptionLockCache(userID)
}

func (s *SubscriptionService) invalidateSubscriptionCacheAsync(userID, groupID int64) {
	if s.billingCacheService == nil || userID <= 0 || groupID <= 0 {
		return
	}
	go func() {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.billingCacheService.InvalidateSubscription(cacheCtx, userID, groupID)
	}()
}

// BulkAssignSubscriptionInput 批量分配订阅输入
type BulkAssignSubscriptionInput struct {
	UserIDs        []int64
	GroupID        int64
	ValidityDays   int
	DailyAmountUSD float64 // per-day：每日额度 D；0 时回退 group.daily_limit_usd
	AssignedBy     int64
	Notes          string
}

// BulkAssignResult 批量分配结果
type BulkAssignResult struct {
	SuccessCount  int
	CreatedCount  int
	ReusedCount   int
	FailedCount   int
	Subscriptions []UserSubscription
	Errors        []string
	Statuses      map[int64]string
}

// BulkAssignSubscription 批量分配订阅
func (s *SubscriptionService) BulkAssignSubscription(ctx context.Context, input *BulkAssignSubscriptionInput) (*BulkAssignResult, error) {
	result := &BulkAssignResult{
		Subscriptions: make([]UserSubscription, 0),
		Errors:        make([]string, 0),
		Statuses:      make(map[int64]string),
	}

	// 请求级预校验：每日额度 D 对整批是同一份参数（input.DailyAmountUSD
	// 或历史 group 回退）。这类公共参数错误应循环前直接返回（handler 转 400），不要吞成每个用户
	// failed 再被 success 包装，导致前端误判请求成功。
	if _, err := s.resolveAssignDailyAmount(ctx, &AssignSubscriptionInput{GroupID: input.GroupID, DailyAmountUSD: input.DailyAmountUSD}); err != nil {
		return nil, err
	}

	for _, userID := range input.UserIDs {
		sub, reused, err := s.assignSubscriptionWithReuse(ctx, &AssignSubscriptionInput{
			UserID:         userID,
			GroupID:        input.GroupID,
			ValidityDays:   input.ValidityDays,
			DailyAmountUSD: input.DailyAmountUSD,
			AssignedBy:     input.AssignedBy,
			Notes:          input.Notes,
		})
		if err != nil {
			result.FailedCount++
			result.Errors = append(result.Errors, fmt.Sprintf("user %d: %v", userID, err))
			result.Statuses[userID] = "failed"
		} else {
			result.SuccessCount++
			result.Subscriptions = append(result.Subscriptions, *sub)
			if reused {
				result.ReusedCount++
				result.Statuses[userID] = "reused"
			} else {
				result.CreatedCount++
				result.Statuses[userID] = "created"
			}
		}
	}

	return result, nil
}

func (s *SubscriptionService) assignSubscriptionWithReuse(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	// per-day：订阅与 group 解耦，group 仅作历史快照。新分配入口可直接创建无 group 卡；
	// 若旧入口仍传 group，则仅校验来源 group 存在，并允许 D 从该 group 的 daily_limit_usd 兜底。
	if input.GroupID > 0 {
		if _, err := s.groupRepo.GetByID(ctx, input.GroupID); err != nil {
			return nil, false, fmt.Errorf("group not found: %w", err)
		}
	} else if input.DailyAmountUSD <= 0 {
		return nil, false, infraerrors.BadRequest("INVALID_INPUT", "custom subscription requires daily_amount_usd")
	}

	// per-day：每次分配新建一张 per-day 卡（单卡模式由入口保证至多一张 active）。
	sub, err := s.createSubscription(ctx, input)
	if err != nil {
		return nil, false, err
	}

	// 失效订阅缓存
	s.InvalidateSubCache(input.UserID, input.GroupID)
	s.invalidateSubscriptionCacheAsync(input.UserID, input.GroupID)

	return sub, false, nil
}

func detectAssignSemanticConflict(existing *UserSubscription, input *AssignSubscriptionInput) (string, bool) {
	if existing == nil || input == nil {
		return "", false
	}

	normalizedDays := normalizeAssignValidityDays(input.ValidityDays)
	if !existing.StartsAt.IsZero() {
		expectedExpiresAt := existing.StartsAt.AddDate(0, 0, normalizedDays)
		if expectedExpiresAt.After(MaxExpiresAt) {
			expectedExpiresAt = MaxExpiresAt
		}
		if !existing.ExpiresAt.Equal(expectedExpiresAt) {
			return "validity_days_mismatch", true
		}
	}

	existingNotes := strings.TrimSpace(existing.Notes)
	inputNotes := strings.TrimSpace(input.Notes)
	if existingNotes != inputNotes {
		return "notes_mismatch", true
	}

	return "", false
}

func normalizeAssignValidityDays(days int) int {
	if days <= 0 {
		days = 30
	}
	if days > MaxValidityDays {
		days = MaxValidityDays
	}
	return days
}

// RevokeSubscription 撤销订阅
func (s *SubscriptionService) RevokeSubscription(ctx context.Context, subscriptionID int64) error {
	// 先获取订阅信息用于失效缓存
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return err
	}
	s.clearSubscriptionLockCache(sub.UserID)

	// burn-down 模型：撤销时回收该卡未花的发放余额（行级 FOR UPDATE 内重算），再删除该行。
	if _, _, err := s.userSubRepo.CloseSubscriptionWithReclaim(ctx, subscriptionID, time.Now(), true); err != nil {
		return err
	}
	s.clearSubscriptionLockCache(sub.UserID)

	// 失效订阅缓存
	s.InvalidateSubCache(sub.UserID, sub.GroupID)
	if s.billingCacheService != nil {
		userID, groupID := sub.UserID, sub.GroupID
		go func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.billingCacheService.InvalidateSubscription(cacheCtx, userID, groupID)
		}()
	}
	// 回收改变了用户余额，失效余额缓存
	s.invalidateUserBalanceCacheAsync(sub.UserID)

	return nil
}

// closeSubscriptionForRefund closes a card for a subscription refund without
// deleting its row, so a gateway refund failure can restore it.
func (s *SubscriptionService) closeSubscriptionForRefund(ctx context.Context, subscriptionID int64) error {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return err
	}
	s.clearSubscriptionLockCache(sub.UserID)

	if _, _, err := s.userSubRepo.CloseSubscriptionWithReclaim(ctx, subscriptionID, time.Now(), false); err != nil {
		return err
	}
	s.clearSubscriptionLockCache(sub.UserID)

	s.InvalidateSubCache(sub.UserID, sub.GroupID)
	if s.billingCacheService != nil {
		userID, groupID := sub.UserID, sub.GroupID
		go func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.billingCacheService.InvalidateSubscription(cacheCtx, userID, groupID)
		}()
	}
	s.invalidateUserBalanceCacheAsync(sub.UserID)
	return nil
}

// revokeRenewalDaysForRefund removes only the days added by a renew order. It
// deliberately does not reset package limits or today's balance unless the
// shortened card has no service day left and must be closed.
func (s *SubscriptionService) revokeRenewalDaysForRefund(ctx context.Context, subscriptionID int64, days int) error {
	if days <= 0 {
		return infraerrors.BadRequest("INVALID_RENEW_DAYS", "renewal refund days must be positive")
	}
	if days > MaxValidityDays {
		days = MaxValidityDays
	}

	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return err
	}

	now := time.Now()
	today := EastDayNumber(now)
	newExpireDay := sub.ExpireDay - days
	if newExpireDay < today-1 {
		newExpireDay = today - 1
	}
	if newExpireDay < today {
		return s.closeSubscriptionForRefund(ctx, subscriptionID)
	}

	if _, _, err := s.userSubRepo.ShortenSubscriptionWithReclaim(ctx, subscriptionID, days, ExpireDayToExpiresAt(newExpireDay), now); err != nil {
		return err
	}
	if sub.Status == SubscriptionStatusExpired {
		if err := s.userSubRepo.UpdateStatus(ctx, subscriptionID, SubscriptionStatusActive); err != nil {
			return err
		}
	}
	s.clearSubscriptionLockCache(sub.UserID)
	s.InvalidateSubCache(sub.UserID, sub.GroupID)
	if s.billingCacheService != nil {
		userID, groupID := sub.UserID, sub.GroupID
		go func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.billingCacheService.InvalidateSubscription(cacheCtx, userID, groupID)
		}()
	}
	s.invalidateUserBalanceCacheAsync(sub.UserID)
	return nil
}

// restoreSubscriptionForRefund restores the exact card state captured before a
// refund deduction. It is intentionally narrower than ExtendSubscription:
// refund rollback must restore today's package balance as well as expire_day.
func (s *SubscriptionService) restoreSubscriptionForRefund(ctx context.Context, subscriptionID int64, expireDay int, todayRemaining float64, todayDay int) error {
	if expireDay <= 0 {
		return infraerrors.BadRequest("INVALID_EXPIRE_DAY", "subscription expire_day must be positive")
	}
	if todayRemaining < 0 {
		todayRemaining = 0
	}

	if s.entClient == nil {
		sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
		if err != nil {
			return err
		}
		sub.Status = SubscriptionStatusActive
		sub.ExpireDay = ClampExpireDay(expireDay)
		sub.ExpiresAt = ExpireDayToExpiresAt(sub.ExpireDay)
		sub.TodayRemaining = todayRemaining
		sub.TodayDay = todayDay
		if err := s.userSubRepo.Update(ctx, sub); err != nil {
			return err
		}
		s.clearSubscriptionLockCache(sub.UserID)
		s.InvalidateSubCache(sub.UserID, sub.GroupID)
		s.invalidateUserBalanceCacheAsync(sub.UserID)
		return nil
	}

	var userID, groupID int64
	newExpireDay := ClampExpireDay(expireDay)
	err := s.withSubscriptionUpdateTx(ctx, func(txCtx context.Context) error {
		tx := dbent.TxFromContext(txCtx)
		client := s.entClient
		if tx != nil {
			client = tx.Client()
		}

		query := client.UserSubscription.Query().
			Where(usersubscription.IDEQ(subscriptionID))
		if client.Driver() != nil && client.Driver().Dialect() == dialect.Postgres {
			query = query.ForUpdate()
		}
		sub, err := query.Only(txCtx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return ErrSubscriptionNotFound
			}
			return err
		}
		userID = sub.UserID
		groupID = entGroupIDValue(sub.GroupID)

		_, err = client.UserSubscription.UpdateOneID(subscriptionID).
			SetStatus(SubscriptionStatusActive).
			SetExpireDay(newExpireDay).
			SetExpiresAt(ExpireDayToExpiresAt(newExpireDay)).
			SetTodayRemaining(todayRemaining).
			SetTodayDay(todayDay).
			SetUpdatedAt(time.Now()).
			Save(txCtx)
		return err
	})
	if err != nil {
		return err
	}

	s.clearSubscriptionLockCache(userID)
	s.InvalidateSubCache(userID, groupID)
	if s.billingCacheService != nil {
		go func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.billingCacheService.InvalidateSubscription(cacheCtx, userID, groupID)
		}()
	}
	s.invalidateUserBalanceCacheAsync(userID)
	return nil
}

// ExtendSubscription 调整订阅时长（正数延长，负数缩短）
func (s *SubscriptionService) ExtendSubscription(ctx context.Context, subscriptionID int64, days int) (*UserSubscription, error) {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	s.clearSubscriptionLockCache(sub.UserID)

	// 限制调整天数范围
	if days > MaxValidityDays {
		days = MaxValidityDays
	}
	if days < -MaxValidityDays {
		days = -MaxValidityDays
	}

	now := time.Now()
	isExpired := !sub.ExpiresAt.After(now)

	// 如果订阅已过期，不允许负向调整
	if isExpired && days < 0 {
		return nil, infraerrors.BadRequest("CANNOT_SHORTEN_EXPIRED", "cannot shorten an expired subscription")
	}

	// 计算新的过期时间
	var newExpiresAt time.Time
	if isExpired {
		// 已过期：从当前时间开始增加天数
		newExpiresAt = now.AddDate(0, 0, days)
	} else {
		// 未过期：从原过期时间增加/减少天数
		newExpiresAt = sub.ExpiresAt.AddDate(0, 0, days)
	}

	if newExpiresAt.After(MaxExpiresAt) {
		newExpiresAt = MaxExpiresAt
	}

	// 检查新的过期时间必须大于当前时间
	if !newExpiresAt.After(now) {
		return nil, ErrAdjustWouldExpire
	}

	// per-day：仅调整卡的 expire_day（服务窗口），**不动 users.balance**（卡价值在 today_remaining）。
	//   days<0：缩短 → expire_day −= |days|（下限 today−1）；
	//   days>0：延长 → expire_day = clamp(max(原, today−1) + days)（按续费口径并夹到上限）；
	//   days==0：仅同步过期时间（一般不会发生）。
	switch {
	case days < 0:
		if _, _, err := s.userSubRepo.ShortenSubscriptionWithReclaim(ctx, subscriptionID, -days, newExpiresAt, now); err != nil {
			return nil, err
		}
	case days > 0:
		if _, _, err := s.userSubRepo.GrantSubscriptionDays(ctx, subscriptionID, days, newExpiresAt, now); err != nil {
			return nil, err
		}
	default:
		if err := s.userSubRepo.ExtendExpiry(ctx, subscriptionID, newExpiresAt); err != nil {
			return nil, err
		}
	}

	// 如果订阅已过期，恢复为active状态
	if sub.Status == SubscriptionStatusExpired {
		if err := s.userSubRepo.UpdateStatus(ctx, subscriptionID, SubscriptionStatusActive); err != nil {
			return nil, err
		}
	}
	s.clearSubscriptionLockCache(sub.UserID)

	// 失效订阅缓存
	s.InvalidateSubCache(sub.UserID, sub.GroupID)
	if s.billingCacheService != nil {
		userID, groupID := sub.UserID, sub.GroupID
		go func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.billingCacheService.InvalidateSubscription(cacheCtx, userID, groupID)
		}()
	}
	// 额度/余额已变动，失效余额缓存
	s.invalidateUserBalanceCacheAsync(sub.UserID)

	return s.userSubRepo.GetByID(ctx, subscriptionID)
}

// GetByID 根据ID获取订阅
func (s *SubscriptionService) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	return s.userSubRepo.GetByID(ctx, id)
}

// GetActiveUserSubscription 获取用户唯一生效订阅卡（per-day 单卡模式，不按 group 匹配）。
func (s *SubscriptionService) GetActiveUserSubscription(ctx context.Context, userID int64) (*UserSubscription, error) {
	return s.userSubRepo.GetActiveByUserID(ctx, userID)
}

// GetActiveSubscription 获取用户对特定分组的有效订阅
// 使用 L1 缓存 + singleflight 加速中间件热路径。
// 返回缓存对象的浅拷贝，调用方可安全修改字段而不会污染缓存或触发 data race。
func (s *SubscriptionService) GetActiveSubscription(ctx context.Context, userID, groupID int64) (*UserSubscription, error) {
	key := subCacheKey(userID, groupID)

	// L1 缓存命中：返回浅拷贝
	if s.subCacheL1 != nil {
		if v, ok := s.subCacheL1.Get(key); ok {
			if sub, ok := v.(*UserSubscription); ok {
				cp := *sub
				return &cp, nil
			}
		}
	}

	// singleflight 防止并发击穿
	value, err, _ := s.subCacheGroup.Do(key, func() (any, error) {
		sub, err := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, userID, groupID)
		if err != nil {
			return nil, err // 直接透传 repo 已翻译的错误（NotFound → ErrSubscriptionNotFound，其他错误原样返回）
		}
		// 写入 L1 缓存
		if s.subCacheL1 != nil {
			_ = s.subCacheL1.SetWithTTL(key, sub, 1, s.jitteredTTL(s.subCacheTTL))
		}
		return sub, nil
	})
	if err != nil {
		return nil, err
	}
	// singleflight 返回的也是缓存指针，需要浅拷贝
	sub, ok := value.(*UserSubscription)
	if !ok || sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	cp := *sub
	return &cp, nil
}

// ListUserSubscriptions 获取用户的所有订阅
func (s *SubscriptionService) ListUserSubscriptions(ctx context.Context, userID int64) ([]UserSubscription, error) {
	subs, err := s.userSubRepo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	normalizeExpiredWindows(subs)
	normalizeSubscriptionStatus(subs)
	return subs, nil
}

// ListActiveUserSubscriptions 获取用户的所有有效订阅
func (s *SubscriptionService) ListActiveUserSubscriptions(ctx context.Context, userID int64) ([]UserSubscription, error) {
	subs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	normalizeExpiredWindows(subs)
	return subs, nil
}

// ListGroupSubscriptions 获取分组的所有订阅
func (s *SubscriptionService) ListGroupSubscriptions(ctx context.Context, groupID int64, page, pageSize int) ([]UserSubscription, *pagination.PaginationResult, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	subs, pag, err := s.userSubRepo.ListByGroupID(ctx, groupID, params)
	if err != nil {
		return nil, nil, err
	}
	normalizeExpiredWindows(subs)
	normalizeSubscriptionStatus(subs)
	s.attachRefundInfo(ctx, subs)
	return subs, pag, nil
}

// List 获取所有订阅（分页，支持筛选和排序）
func (s *SubscriptionService) List(ctx context.Context, page, pageSize int, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	subs, pag, err := s.userSubRepo.List(ctx, params, userID, groupID, status, platform, sortBy, sortOrder)
	if err != nil {
		return nil, nil, err
	}
	normalizeExpiredWindows(subs)
	normalizeSubscriptionStatus(subs)
	s.attachRefundInfo(ctx, subs)
	return subs, pag, nil
}

func (s *SubscriptionService) attachRefundInfo(ctx context.Context, subs []UserSubscription) {
	if s == nil || s.entClient == nil || len(subs) == 0 {
		return
	}

	userIDs := make([]int64, 0, len(subs))
	subByID := make(map[int64]*UserSubscription, len(subs))
	seenUsers := make(map[int64]struct{}, len(subs))
	for i := range subs {
		sub := &subs[i]
		subByID[sub.ID] = sub
		if _, ok := seenUsers[sub.UserID]; ok {
			continue
		}
		seenUsers[sub.UserID] = struct{}{}
		userIDs = append(userIDs, sub.UserID)
	}
	if len(userIDs) == 0 {
		return
	}

	orders, err := s.entClient.PaymentOrder.Query().
		Where(
			paymentorder.UserIDIn(userIDs...),
			paymentorder.OrderTypeEQ(payment.OrderTypeSubscription),
			paymentorder.StatusIn(OrderStatusCompleted, OrderStatusRefundFailed),
		).
		Order(dbent.Desc(paymentorder.FieldID)).
		All(ctx)
	if err != nil {
		return
	}

	today := TodayEastDayNumber()
	for _, order := range orders {
		subID, ok := readSubscriptionSnapshotSubscriptionID(order)
		if !ok || subID <= 0 {
			continue
		}
		sub := subByID[subID]
		if sub == nil || sub.RefundOrderID != nil {
			continue
		}
		originalDays, err := subscriptionOrderOriginalDays(order)
		if err != nil {
			continue
		}
		card := sub.ToPerDayCard()
		refundable := RefundAmount(order.Amount, card.RefundableDays(today), originalDays)
		if refundable <= 0 {
			continue
		}
		orderID := order.ID
		orderAmount := order.Amount
		orderPay := order.PayAmount
		refundableAmount := math.Ceil(refundable*100) / 100
		sub.RefundOrderID = &orderID
		sub.RefundOrderStatus = order.Status
		sub.RefundOrderAmount = &orderAmount
		sub.RefundOrderPay = &orderPay
		sub.RefundableAmount = &refundableAmount
	}
}

// PopulateAdminRefundInfo enriches admin subscription DTO sources with the latest refundable
// paid subscription order. User-facing DTO mappers intentionally ignore these fields.
func (s *SubscriptionService) PopulateAdminRefundInfo(ctx context.Context, subs []UserSubscription) {
	s.attachRefundInfo(ctx, subs)
}

// normalizeExpiredWindows 将已过期窗口的数据清零（仅影响返回数据，不影响数据库）
// 这确保前端显示正确的当前窗口状态，而不是过期窗口的历史数据
func normalizeExpiredWindows(subs []UserSubscription) {
	for i := range subs {
		sub := &subs[i]
		// 日窗口过期：清零展示数据
		if sub.NeedsDailyReset() {
			sub.DailyWindowStart = nil
			sub.DailyUsageUSD = 0
		}
		// 周窗口过期：清零展示数据
		if sub.NeedsWeeklyReset() {
			sub.WeeklyWindowStart = nil
			sub.WeeklyUsageUSD = 0
		}
		// 月窗口过期：清零展示数据
		if sub.NeedsMonthlyReset() {
			sub.MonthlyWindowStart = nil
			sub.MonthlyUsageUSD = 0
		}
	}
}

// normalizeSubscriptionStatus 根据实际过期时间修正状态（仅影响返回数据，不影响数据库）
// 这确保前端显示正确的状态，即使定时任务尚未更新数据库
func normalizeSubscriptionStatus(subs []UserSubscription) {
	now := time.Now()
	for i := range subs {
		sub := &subs[i]
		if sub.Status == SubscriptionStatusActive && !sub.ExpiresAt.After(now) {
			sub.Status = SubscriptionStatusExpired
		}
	}
}

// startOfDay 返回给定时间所在日期的零点（保持原时区）
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// CheckAndActivateWindow 检查并激活窗口（首次使用时）
func (s *SubscriptionService) CheckAndActivateWindow(ctx context.Context, sub *UserSubscription) error {
	if sub.IsWindowActivated() {
		return nil
	}

	// 使用当天零点作为窗口起始时间
	windowStart := startOfDay(time.Now())
	return s.userSubRepo.ActivateWindows(ctx, sub.ID, windowStart)
}

// AdminResetQuota manually resets the daily, weekly, and/or monthly usage windows.
// Uses startOfDay(now) as the new window start, matching automatic resets.
func (s *SubscriptionService) AdminResetQuota(ctx context.Context, subscriptionID int64, resetDaily, resetWeekly, resetMonthly bool) (*UserSubscription, error) {
	if !resetDaily && !resetWeekly && !resetMonthly {
		return nil, ErrInvalidInput
	}
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	windowStart := startOfDay(time.Now())
	if resetDaily {
		if err := s.userSubRepo.ResetDailyUsage(ctx, sub.ID, windowStart); err != nil {
			return nil, err
		}
	}
	if resetWeekly {
		if err := s.userSubRepo.ResetWeeklyUsage(ctx, sub.ID, windowStart); err != nil {
			return nil, err
		}
	}
	if resetMonthly {
		if err := s.userSubRepo.ResetMonthlyUsage(ctx, sub.ID, windowStart); err != nil {
			return nil, err
		}
	}
	// Invalidate L1 ristretto cache. Ristretto's Del() is asynchronous by design,
	// so call Wait() immediately after to flush pending operations and guarantee
	// the deleted key is not returned on the very next Get() call.
	s.InvalidateSubCache(sub.UserID, sub.GroupID)
	if s.subCacheL1 != nil {
		s.subCacheL1.Wait()
	}
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateSubscription(ctx, sub.UserID, sub.GroupID)
	}
	// Return the refreshed subscription from DB
	return s.userSubRepo.GetByID(ctx, subscriptionID)
}

// CheckAndResetWindows 检查并重置过期的窗口
func (s *SubscriptionService) CheckAndResetWindows(ctx context.Context, sub *UserSubscription) error {
	// 使用当天零点作为新窗口起始时间
	windowStart := startOfDay(time.Now())
	needsInvalidateCache := false

	// 日窗口重置（24小时）
	if sub.NeedsDailyReset() {
		if err := s.userSubRepo.ResetDailyUsage(ctx, sub.ID, windowStart); err != nil {
			return err
		}
		sub.DailyWindowStart = &windowStart
		sub.DailyUsageUSD = 0
		needsInvalidateCache = true
	}

	// 周窗口重置（7天）
	if sub.NeedsWeeklyReset() {
		if err := s.userSubRepo.ResetWeeklyUsage(ctx, sub.ID, windowStart); err != nil {
			return err
		}
		sub.WeeklyWindowStart = &windowStart
		sub.WeeklyUsageUSD = 0
		needsInvalidateCache = true
	}

	// 月窗口重置（30天）
	if sub.NeedsMonthlyReset() {
		if err := s.userSubRepo.ResetMonthlyUsage(ctx, sub.ID, windowStart); err != nil {
			return err
		}
		sub.MonthlyWindowStart = &windowStart
		sub.MonthlyUsageUSD = 0
		needsInvalidateCache = true
	}

	// 如果有窗口被重置，失效缓存以保持一致性
	if needsInvalidateCache {
		s.InvalidateSubCache(sub.UserID, sub.GroupID)
		if s.billingCacheService != nil {
			_ = s.billingCacheService.InvalidateSubscription(ctx, sub.UserID, sub.GroupID)
		}
	}

	return nil
}

// CheckUsageLimits 检查使用限额（返回错误如果超限）
// 用于中间件的快速预检查，additionalCost 通常为 0
func (s *SubscriptionService) CheckUsageLimits(ctx context.Context, sub *UserSubscription, group *Group, additionalCost float64) error {
	if !sub.CheckDailyLimit(group, additionalCost) {
		return ErrDailyLimitExceeded
	}
	if !sub.CheckWeeklyLimit(group, additionalCost) {
		return ErrWeeklyLimitExceeded
	}
	if !sub.CheckMonthlyLimit(group, additionalCost) {
		return ErrMonthlyLimitExceeded
	}
	return nil
}

// ValidateAndCheckLimits 合并验证+限额检查（中间件热路径专用）
// 仅做内存检查，不触发 DB 写入。窗口重置的 DB 写入由 DoWindowMaintenance 异步完成。
// 返回 needsMaintenance 表示是否需要异步执行窗口维护。
func (s *SubscriptionService) ValidateAndCheckLimits(sub *UserSubscription, group *Group) (needsMaintenance bool, err error) {
	// 1. 验证订阅状态
	if sub.Status == SubscriptionStatusExpired {
		return false, ErrSubscriptionExpired
	}
	if sub.Status == SubscriptionStatusSuspended {
		return false, ErrSubscriptionSuspended
	}
	if sub.IsExpired() {
		return false, ErrSubscriptionExpired
	}

	// 2. 内存中修正过期窗口的用量，确保 CheckUsageLimits 不会误拒绝用户
	//    实际的 DB 窗口重置由 DoWindowMaintenance 异步完成
	if sub.NeedsDailyReset() {
		sub.DailyUsageUSD = 0
		needsMaintenance = true
	}
	if sub.NeedsWeeklyReset() {
		sub.WeeklyUsageUSD = 0
		needsMaintenance = true
	}
	if sub.NeedsMonthlyReset() {
		sub.MonthlyUsageUSD = 0
		needsMaintenance = true
	}
	if !sub.IsWindowActivated() {
		needsMaintenance = true
	}

	// 3. 检查用量限额
	if !sub.CheckDailyLimit(group, 0) {
		return needsMaintenance, ErrDailyLimitExceeded
	}
	if !sub.CheckWeeklyLimit(group, 0) {
		return needsMaintenance, ErrWeeklyLimitExceeded
	}
	if !sub.CheckMonthlyLimit(group, 0) {
		return needsMaintenance, ErrMonthlyLimitExceeded
	}

	return needsMaintenance, nil
}

// DoWindowMaintenance 异步执行窗口维护（激活+重置）
// 使用独立 context，不受请求取消影响。
// 注意：此方法仅在 ValidateAndCheckLimits 返回 needsMaintenance=true 时调用，
// 而 IsExpired()=true 的订阅在 ValidateAndCheckLimits 中已被拦截返回错误，
// 因此进入此方法的订阅一定未过期，无需处理过期状态同步。
func (s *SubscriptionService) DoWindowMaintenance(sub *UserSubscription) {
	if s == nil {
		return
	}
	if s.maintenanceQueue != nil {
		err := s.maintenanceQueue.TryEnqueue(func() {
			s.doWindowMaintenance(sub)
		})
		if err != nil {
			log.Printf("Subscription maintenance enqueue failed: %v", err)
		}
		return
	}

	s.doWindowMaintenance(sub)
}

func (s *SubscriptionService) doWindowMaintenance(sub *UserSubscription) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 激活窗口（首次使用时）
	if !sub.IsWindowActivated() {
		if err := s.CheckAndActivateWindow(ctx, sub); err != nil {
			log.Printf("Failed to activate subscription windows: %v", err)
		}
	}

	// 重置过期窗口
	if err := s.CheckAndResetWindows(ctx, sub); err != nil {
		log.Printf("Failed to reset subscription windows: %v", err)
	}

	// 失效 L1 缓存，确保后续请求拿到更新后的数据
	s.InvalidateSubCache(sub.UserID, sub.GroupID)
}

// RecordUsage 记录使用量到订阅
func (s *SubscriptionService) RecordUsage(ctx context.Context, subscriptionID int64, costUSD float64) error {
	return s.userSubRepo.IncrementUsage(ctx, subscriptionID, costUSD)
}

// SubscriptionProgress 订阅进度
type SubscriptionProgress struct {
	ID            int64                `json:"id"`
	GroupName     string               `json:"group_name"`
	ExpiresAt     time.Time            `json:"expires_at"`
	ExpiresInDays int                  `json:"expires_in_days"`
	Daily         *UsageWindowProgress `json:"daily,omitempty"`
	Weekly        *UsageWindowProgress `json:"weekly,omitempty"`
	Monthly       *UsageWindowProgress `json:"monthly,omitempty"`
	// Burndown 为 burn-down 计费模型的进度视图（新模型）。
	Burndown *BurndownProgress `json:"burndown,omitempty"`
}

// BurndownProgress burn-down 订阅进度：展示「消费进度天 / 日历天」、剩余订阅余额等。
type BurndownProgress struct {
	GrantedTotalUSD float64 `json:"granted_total_usd"` // 发放总额 G = D×天数
	DailyAmountUSD  float64 `json:"daily_amount_usd"`  // 每日额度 D
	ConsumedUSD     float64 `json:"consumed_usd"`      // 累计消费
	ClawedUSD       float64 `json:"clawed_usd"`        // 累计被清扣
	RemainingUSD    float64 `json:"remaining_usd"`     // 剩余订阅余额
	ConsumptionDay  float64 `json:"consumption_day"`   // 消费进度天 = 累计消费/D（可超过日历天 = 已透支）
	CalendarDay     int     `json:"calendar_day"`      // 自激活起经过的日历天 N（Asia/Shanghai）
	TotalDays       int     `json:"total_days"`        // 总天数 = G/D
}

// UsageWindowProgress 使用窗口进度
type UsageWindowProgress struct {
	LimitUSD        float64   `json:"limit_usd"`
	UsedUSD         float64   `json:"used_usd"`
	RemainingUSD    float64   `json:"remaining_usd"`
	Percentage      float64   `json:"percentage"`
	WindowStart     time.Time `json:"window_start"`
	ResetsAt        time.Time `json:"resets_at"`
	ResetsInSeconds int64     `json:"resets_in_seconds"`
}

// GetSubscriptionProgress 获取订阅使用进度
func (s *SubscriptionService) GetSubscriptionProgress(ctx context.Context, subscriptionID int64) (*SubscriptionProgress, error) {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}

	return s.calculateProgress(sub), nil
}

// calculateProgress 根据已加载的订阅计算使用进度（纯内存计算，无 DB 查询）。
// 订阅已去分组化；历史卡若带 group 仅用于展示名称，额度全部读卡级字段。
func (s *SubscriptionService) calculateProgress(sub *UserSubscription) *SubscriptionProgress {
	groupName := "All groups"
	if sub.Group != nil && strings.TrimSpace(sub.Group.Name) != "" {
		groupName = sub.Group.Name
	}
	progress := &SubscriptionProgress{
		ID:            sub.ID,
		GroupName:     groupName,
		ExpiresAt:     sub.ExpiresAt,
		ExpiresInDays: sub.DaysRemaining(),
	}

	// burn-down 进度视图（新模型）：开通即发放总额，按消费进度天/日历天展示。
	if sub.GrantedTotalUSD > 0 || sub.DailyAmountUSD > 0 {
		now := time.Now()
		progress.Burndown = &BurndownProgress{
			GrantedTotalUSD: sub.GrantedTotalUSD,
			DailyAmountUSD:  sub.DailyAmountUSD,
			ConsumedUSD:     sub.ConsumedUSD,
			ClawedUSD:       sub.ClawedUSD,
			RemainingUSD:    sub.RemainingUSD(),
			ConsumptionDay:  sub.ConsumptionDay(),
			CalendarDay:     sub.CalendarDayAt(now),
			TotalDays:       sub.TotalDays(),
		}
	}

	window := sub.ToSubWindow()
	now := time.Now()
	window.ResetWindows(now)

	// 日进度：限额挂卡，不再读取 group.daily_limit_usd。
	if window.DailyLimitUSD > 0 && window.DailyWindowStart != nil {
		limit := window.DailyLimitUSD
		resetsAt := window.DailyWindowStart.Add(24 * time.Hour)
		progress.Daily = &UsageWindowProgress{
			LimitUSD:        limit,
			UsedUSD:         window.DailyUsageUSD,
			RemainingUSD:    limit - window.DailyUsageUSD,
			Percentage:      (window.DailyUsageUSD / limit) * 100,
			WindowStart:     *window.DailyWindowStart,
			ResetsAt:        resetsAt,
			ResetsInSeconds: int64(time.Until(resetsAt).Seconds()),
		}
		if progress.Daily.RemainingUSD < 0 {
			progress.Daily.RemainingUSD = 0
		}
		if progress.Daily.Percentage > 100 {
			progress.Daily.Percentage = 100
		}
		if progress.Daily.ResetsInSeconds < 0 {
			progress.Daily.ResetsInSeconds = 0
		}
	}

	// 周进度：限额挂卡，不再读取 group.weekly_limit_usd。
	if window.WeeklyLimitUSD > 0 && window.WeeklyWindowStart != nil {
		limit := window.WeeklyLimitUSD
		resetsAt := window.WeeklyWindowStart.Add(7 * 24 * time.Hour)
		progress.Weekly = &UsageWindowProgress{
			LimitUSD:        limit,
			UsedUSD:         window.WeeklyUsageUSD,
			RemainingUSD:    limit - window.WeeklyUsageUSD,
			Percentage:      (window.WeeklyUsageUSD / limit) * 100,
			WindowStart:     *window.WeeklyWindowStart,
			ResetsAt:        resetsAt,
			ResetsInSeconds: int64(time.Until(resetsAt).Seconds()),
		}
		if progress.Weekly.RemainingUSD < 0 {
			progress.Weekly.RemainingUSD = 0
		}
		if progress.Weekly.Percentage > 100 {
			progress.Weekly.Percentage = 100
		}
		if progress.Weekly.ResetsInSeconds < 0 {
			progress.Weekly.ResetsInSeconds = 0
		}
	}

	// 月进度：限额挂卡，不再读取 group.monthly_limit_usd。
	if window.MonthlyLimitUSD > 0 && window.MonthlyWindowStart != nil {
		limit := window.MonthlyLimitUSD
		resetsAt := window.MonthlyWindowStart.AddDate(0, 1, 0)
		progress.Monthly = &UsageWindowProgress{
			LimitUSD:        limit,
			UsedUSD:         window.MonthlyUsageUSD,
			RemainingUSD:    limit - window.MonthlyUsageUSD,
			Percentage:      (window.MonthlyUsageUSD / limit) * 100,
			WindowStart:     *window.MonthlyWindowStart,
			ResetsAt:        resetsAt,
			ResetsInSeconds: int64(time.Until(resetsAt).Seconds()),
		}
		if progress.Monthly.RemainingUSD < 0 {
			progress.Monthly.RemainingUSD = 0
		}
		if progress.Monthly.Percentage > 100 {
			progress.Monthly.Percentage = 100
		}
		if progress.Monthly.ResetsInSeconds < 0 {
			progress.Monthly.ResetsInSeconds = 0
		}
	}

	return progress
}

// GetUserSubscriptionsWithProgress 获取用户所有订阅及进度
func (s *SubscriptionService) GetUserSubscriptionsWithProgress(ctx context.Context, userID int64) ([]SubscriptionProgress, error) {
	// ListActiveByUserID 已使用 .WithGroup() eager-load Group 关联，1 次查询获取所有数据
	subs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	progresses := make([]SubscriptionProgress, 0, len(subs))
	for i := range subs {
		sub := &subs[i]
		progresses = append(progresses, *s.calculateProgress(sub))
	}

	return progresses, nil
}

// ValidateSubscription 验证订阅是否有效
func (s *SubscriptionService) ValidateSubscription(ctx context.Context, sub *UserSubscription) error {
	if sub.Status == SubscriptionStatusExpired {
		return ErrSubscriptionExpired
	}
	if sub.Status == SubscriptionStatusSuspended {
		return ErrSubscriptionSuspended
	}
	if sub.IsExpired() {
		// 更新状态
		_ = s.userSubRepo.UpdateStatus(ctx, sub.ID, SubscriptionStatusExpired)
		return ErrSubscriptionExpired
	}
	return nil
}
