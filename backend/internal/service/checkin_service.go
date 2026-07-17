package service

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// 签到相关错误。
var (
	// ErrCheckinDisabled 表示签到功能未开启。
	ErrCheckinDisabled = errors.New("checkin disabled")
	// ErrCheckinAlreadyClaimed 表示当日基础签到已领取。
	ErrCheckinAlreadyClaimed = errors.New("checkin already claimed today")
	// ErrCheckinNoBonus 表示当前没有可领取的额外签到（消费未达阈值或已领完）。
	ErrCheckinNoBonus = errors.New("no bonus checkin available")
	// ErrCheckinNotActiveEnough 表示当日 Token 用量未达到基础签到门槛（本日不够活跃）。
	ErrCheckinNotActiveEnough = errors.New("not active enough today")
)

// checkinZone 固定为 Asia/Shanghai（UTC+8，中国无夏令时，等价于固定时区，
// 避免静态二进制缺少 tzdata 时 time.LoadLocation 失败）。
var checkinZone = time.FixedZone("CST", 8*3600)

// checkinNow 便于测试覆盖。
var checkinNow = time.Now

// CheckinConfig 是签到功能的管理员可配置项。
type CheckinConfig struct {
	Enabled       bool    `json:"enabled"`
	AmountMin     float64 `json:"amount_min"`      // 单次随机奖励下限（USD）
	AmountMax     float64 `json:"amount_max"`      // 单次随机奖励上限（USD）
	SpendPerExtra float64 `json:"spend_per_extra"` // 每累计消费满该金额（USD）解锁一次额外签到；0=不开放额外签到
	MinTokens     int64   `json:"min_tokens"`      // 基础签到所需"当日"最低 Token 用量（input+output+cache 合计）；<=0=不设门槛
}

func (c *CheckinConfig) normalize() {
	if c.AmountMin < 0 {
		c.AmountMin = 0
	}
	if c.AmountMax < 0 {
		c.AmountMax = 0
	}
	if c.AmountMax < c.AmountMin {
		c.AmountMax = c.AmountMin
	}
	if c.SpendPerExtra < 0 {
		c.SpendPerExtra = 0
	}
	if c.MinTokens < 0 {
		c.MinTokens = 0
	}
}

// GetCheckinConfig 读取签到配置（缺失键回退默认值，并 normalize）。
func (s *SettingService) GetCheckinConfig(ctx context.Context) CheckinConfig {
	vals, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyCheckinEnabled,
		SettingKeyCheckinAmountMin,
		SettingKeyCheckinAmountMax,
		SettingKeyCheckinSpendPerExtra,
		SettingKeyCheckinMinTokens,
	})
	if err != nil || vals == nil {
		vals = map[string]string{}
	}
	cfg := CheckinConfig{
		Enabled:       vals[SettingKeyCheckinEnabled] == "true",
		AmountMin:     parseFloatDefault(vals[SettingKeyCheckinAmountMin], CheckinAmountMinDefault),
		AmountMax:     parseFloatDefault(vals[SettingKeyCheckinAmountMax], CheckinAmountMaxDefault),
		SpendPerExtra: parseFloatDefault(vals[SettingKeyCheckinSpendPerExtra], CheckinSpendPerExtraDefault),
		MinTokens:     parseInt64Default(vals[SettingKeyCheckinMinTokens], CheckinMinTokensDefault),
	}
	cfg.normalize()
	return cfg
}

// UpdateCheckinConfig 持久化签到配置并触发缓存失效回调。
func (s *SettingService) UpdateCheckinConfig(ctx context.Context, cfg CheckinConfig) error {
	cfg.normalize()
	updates := map[string]string{
		SettingKeyCheckinEnabled:       strconv.FormatBool(cfg.Enabled),
		SettingKeyCheckinAmountMin:     strconv.FormatFloat(cfg.AmountMin, 'f', 4, 64),
		SettingKeyCheckinAmountMax:     strconv.FormatFloat(cfg.AmountMax, 'f', 4, 64),
		SettingKeyCheckinSpendPerExtra: strconv.FormatFloat(cfg.SpendPerExtra, 'f', 4, 64),
		SettingKeyCheckinMinTokens:     strconv.FormatInt(cfg.MinTokens, 10),
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return err
	}
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return nil
}

func parseFloatDefault(raw string, def float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return def
	}
	return v
}

func parseInt64Default(raw string, def int64) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return def
	}
	return v
}

// CheckinRepository 抽象签到记录的持久化与原子领取。
type CheckinRepository interface {
	// HasDailyCheckin 返回该用户在指定自然日是否已完成基础签到。
	HasDailyCheckin(ctx context.Context, userID int64, date string) (bool, error)
	// CountBonusOnDate 返回该用户在指定自然日已领取的额外签到次数。
	CountBonusOnDate(ctx context.Context, userID int64, date string) (int, error)
	// ClaimDaily 原子地写入一条 daily 记录并把 amount 加到用户余额。
	// 若当日 daily 已存在则返回 ErrCheckinAlreadyClaimed。
	ClaimDaily(ctx context.Context, userID int64, date string, amount float64) (float64, error)
	// ClaimBonus 原子地写入一条 bonus 记录并把 amount 加到用户余额；
	// 锁内复核当日 bonus 数 < maxBonus，否则返回 ErrCheckinNoBonus，防止并发超领。
	ClaimBonus(ctx context.Context, userID int64, date string, amount float64, maxBonus int) (float64, error)
}

// CheckinService 实现每日签到与按消费解锁的额外签到。
// usage 依赖已导出的 UsageLogRepository（含 GetUserStatsAggregated），
// 以便 wire 直接用 NewUsageLogRepository 的返回值满足依赖（重生成不再断）。
type CheckinService struct {
	repo            CheckinRepository
	settings        *SettingService
	usage           UsageLogRepository
	billingCache    *BillingCacheService
	authInvalidator APIKeyAuthCacheInvalidator
}

// NewCheckinService 构造签到服务。
func NewCheckinService(
	repo CheckinRepository,
	settings *SettingService,
	usage UsageLogRepository,
	billingCache *BillingCacheService,
	authInvalidator APIKeyAuthCacheInvalidator,
) *CheckinService {
	return &CheckinService{
		repo:            repo,
		settings:        settings,
		usage:           usage,
		billingCache:    billingCache,
		authInvalidator: authInvalidator,
	}
}

// CheckinStatus 是签到卡片所需的全部状态。
type CheckinStatus struct {
	Enabled           bool    `json:"enabled"`
	AmountMin         float64 `json:"amount_min"`
	AmountMax         float64 `json:"amount_max"`
	DailyClaimed      bool    `json:"daily_claimed"`
	DailyAvailable    bool    `json:"daily_available"`
	BonusAvailable    int     `json:"bonus_available"`
	BonusEarnedToday  int     `json:"bonus_earned_today"`
	BonusClaimedToday int     `json:"bonus_claimed_today"`
	SpendPerExtra     float64 `json:"spend_per_extra"`
	TodaySpend        float64 `json:"today_spend"`
	SpendToNextBonus  float64 `json:"spend_to_next_bonus"`
	CanClaim          bool    `json:"can_claim"`
	NextResetAt       string  `json:"next_reset_at"`
	MinTokens         int64   `json:"min_tokens"`   // 基础签到所需当日最低 Token 用量；0=不设门槛
	TodayTokens       int64   `json:"today_tokens"` // 用户当日已用 Token（input+output+cache）
	TokensMet         bool    `json:"tokens_met"`   // 当日 Token 用量是否已达到基础签到门槛
}

// CheckinClaimResult 是一次领取的结果，附带刷新后的状态。
type CheckinClaimResult struct {
	Type    string         `json:"type"` // daily | bonus
	Amount  float64        `json:"amount"`
	Balance float64        `json:"balance"`
	Status  *CheckinStatus `json:"status"`
}

// GetStatus 计算用户当前签到状态。
func (s *CheckinService) GetStatus(ctx context.Context, userID int64) (*CheckinStatus, error) {
	cfg := s.settings.GetCheckinConfig(ctx)
	st := &CheckinStatus{
		Enabled:       cfg.Enabled,
		AmountMin:     cfg.AmountMin,
		AmountMax:     cfg.AmountMax,
		SpendPerExtra: cfg.SpendPerExtra,
	}
	if !cfg.Enabled {
		return st, nil
	}
	date, dayStart := checkinToday(checkinNow())
	st.NextResetAt = dayStart.Add(24 * time.Hour).Format(time.RFC3339)

	hasDaily, err := s.repo.HasDailyCheckin(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	st.DailyClaimed = hasDaily

	spend, tokens := s.todayUsage(ctx, userID, dayStart)
	st.TodaySpend = checkinRound4(spend)

	// 基础签到活跃度门槛：当日 Token 用量需达到 MinTokens（0=不限制）。
	st.MinTokens = cfg.MinTokens
	st.TodayTokens = tokens
	st.TokensMet = cfg.MinTokens <= 0 || tokens >= cfg.MinTokens
	st.DailyAvailable = !hasDaily && st.TokensMet

	if cfg.SpendPerExtra > 0 {
		earned := checkinEarnedBonus(spend, cfg.SpendPerExtra)
		claimed, err := s.repo.CountBonusOnDate(ctx, userID, date)
		if err != nil {
			return nil, err
		}
		st.BonusEarnedToday = earned
		st.BonusClaimedToday = claimed
		if avail := earned - claimed; avail > 0 {
			st.BonusAvailable = avail
		}
		// 距下一次额外签到还需消费：下一档阈值 − 已消费。
		rem := float64(earned+1)*cfg.SpendPerExtra - spend
		if rem < 0 {
			rem = 0
		}
		st.SpendToNextBonus = checkinRound4(rem)
	}
	st.CanClaim = st.DailyAvailable || st.BonusAvailable > 0
	return st, nil
}

// Claim 领取下一个可用签到：优先基础签到（需当日活跃度达标），否则一次额外签到。
func (s *CheckinService) Claim(ctx context.Context, userID int64) (*CheckinClaimResult, error) {
	cfg := s.settings.GetCheckinConfig(ctx)
	if !cfg.Enabled {
		return nil, ErrCheckinDisabled
	}
	date, dayStart := checkinToday(checkinNow())

	hasDaily, err := s.repo.HasDailyCheckin(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	spend, tokens := s.todayUsage(ctx, userID, dayStart)
	tokensMet := cfg.MinTokens <= 0 || tokens >= cfg.MinTokens

	amount := checkinRandomAmount(cfg)

	// 1) 基础签到：当日未领 且 当日 Token 用量达到门槛。
	if !hasDaily && tokensMet {
		balance, err := s.repo.ClaimDaily(ctx, userID, date, amount)
		if err != nil {
			return nil, err
		}
		return s.finishClaim(ctx, userID, "daily", amount, balance)
	}

	// 2) 额外签到：按当日消费解锁（不受活跃度门槛限制）。
	if cfg.SpendPerExtra > 0 {
		earned := checkinEarnedBonus(spend, cfg.SpendPerExtra)
		if earned > 0 {
			claimed, err := s.repo.CountBonusOnDate(ctx, userID, date)
			if err != nil {
				return nil, err
			}
			if claimed < earned {
				// maxBonus=earned：repo 在锁内复核，绝不超过当前应得数。
				balance, err := s.repo.ClaimBonus(ctx, userID, date, amount, earned)
				if err != nil {
					return nil, err
				}
				return s.finishClaim(ctx, userID, "bonus", amount, balance)
			}
		}
	}

	// 3) 无可领：区分"当日活跃度不足"（基础签到被门槛拦住）与"已领完/无额外"。
	if !hasDaily && !tokensMet {
		return nil, ErrCheckinNotActiveEnough
	}
	return nil, ErrCheckinNoBonus
}

// finishClaim 在一次成功领取后失效余额缓存并返回携带最新状态的结果。
func (s *CheckinService) finishClaim(ctx context.Context, userID int64, ctype string, amount, balance float64) (*CheckinClaimResult, error) {
	s.invalidateBalanceCaches(ctx, userID)
	status, statusErr := s.GetStatus(ctx, userID)
	if statusErr != nil {
		status = nil
	}
	return &CheckinClaimResult{Type: ctype, Amount: amount, Balance: balance, Status: status}, nil
}

// todayUsage 返回用户当日（dayStart 至今）累计消费（USD）与 Token 用量（input+output+cache 合计）。
func (s *CheckinService) todayUsage(ctx context.Context, userID int64, dayStart time.Time) (spend float64, tokens int64) {
	stats, err := s.usage.GetUserStatsAggregated(ctx, userID, dayStart, checkinNow())
	if err != nil || stats == nil {
		return 0, 0
	}
	if stats.TotalActualCost > 0 {
		spend = stats.TotalActualCost
	}
	if stats.TotalTokens > 0 {
		tokens = stats.TotalTokens
	}
	return spend, tokens
}

func (s *CheckinService) invalidateBalanceCaches(ctx context.Context, userID int64) {
	if s.authInvalidator != nil {
		s.authInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCache == nil {
		return
	}
	// 签到成功后前端会立即刷新用户信息。这里若异步删缓存，刷新请求可能先
	// 命中旧余额，直到重新登录才会再次拉取；因此必须在响应返回前完成失效。
	cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.billingCache.InvalidateUserBalance(cacheCtx, userID)
}

// checkinToday 返回 Asia/Shanghai 自然日字符串(YYYY-MM-DD)与该日 00:00 的绝对时刻。
func checkinToday(now time.Time) (date string, dayStart time.Time) {
	local := now.In(checkinZone)
	y, m, d := local.Date()
	dayStart = time.Date(y, m, d, 0, 0, 0, 0, checkinZone)
	return local.Format("2006-01-02"), dayStart
}

func checkinRandomAmount(cfg CheckinConfig) float64 {
	if cfg.AmountMax <= cfg.AmountMin {
		return checkinRound4(cfg.AmountMin)
	}
	v := cfg.AmountMin + rand.Float64()*(cfg.AmountMax-cfg.AmountMin)
	return checkinRound4(v)
}

func checkinRound4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

// checkinEarnedBonus 计算当日按消费应得的额外签到次数。
// 用 floor + 微小 epsilon 容差，消除 0.3/0.1=2.9999… 这类浮点边界下溢导致的少发。
func checkinEarnedBonus(spend, spendPerExtra float64) int {
	if spendPerExtra <= 0 || spend <= 0 {
		return 0
	}
	return int(math.Floor(spend/spendPerExtra + 1e-9))
}
