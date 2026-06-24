package service

import (
	"math"
	"time"
)

// MaxSubscriptionOverdraftUses 是本卡累计可「往后预支」的天数硬上限。
// 透支 = 把后续天的额度提前花掉（计费周期前移），单日最多突破到 (1+可预支天数)×D；
// 每天突破当日 D 的天数累加进 total_overdraft_count（求和），累计达到本上限后自动关闭本卡透支
// （max_overdraft_days→NULL），用户端不能再次开启。
// 注意：计量单位是「天」，且同一天内多笔只按当天达到的最高预支天数计，不会按请求数瞬间烧光。
const MaxSubscriptionOverdraftUses = 5

type UserSubscription struct {
	ID      int64
	UserID  int64
	GroupID int64

	StartsAt  time.Time
	ExpiresAt time.Time
	Status    string

	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time

	DailyUsageUSD   float64
	WeeklyUsageUSD  float64
	MonthlyUsageUSD float64

	// Burn-down 计费模型字段
	GrantedTotalUSD     float64    // G = D × days，开通时一次性发放总额
	DailyAmountUSD      float64    // D，开通时对 group.daily_limit_usd 的快照
	ConsumedUSD         float64    // 本卡累计消费（单调递增）
	ClawedUSD           float64    // 本卡累计被清扣（单调递增）
	LastClawbackDay     int        // 已对账到的最高日历天 N
	MaxOverdraftDays    *int       // 本卡用户自设的最多往后透支天数；nil = 透支关闭；用户在「我的订阅」自助设置
	TotalOverdraftCount int        // 本卡累计预支天数（求和、封顶 MaxSubscriptionOverdraftUses）；达上限后自动关闭透支
	DailySpentUSD       float64    // 当前 burn-down 日内已消费额度（配合 DailySpentDay 实现每日限速、不跨天结转）
	DailySpentDay       int        // DailySpentUSD 对应的日历天 N；读写时若 ≠ 当前 N 即视为 0（惰性重置）
	ActivatedAt         *time.Time // 清扣时钟起点；nil 时回退 StartsAt

	// Per-day 每日额度模型字段（per-day redesign，逐步取代上方 burn-down 窗口）
	TodayRemaining float64 // 今日剩余套餐额度（官方刀，1:1，永不为负）
	TodayDay       int     // TodayRemaining 所属东八区绝对自然日序号；-1=未初始化
	StartDay       int     // 激活当天的东八区绝对自然日序号
	ExpireDay      int     // 最后发放 D 的东八区绝对自然日序号（含）；每透支 −1
	OverdraftOn    bool    // 本卡是否开启透支（上限按用户级月度计）

	AssignedBy *int64
	AssignedAt time.Time
	Notes      string

	CreatedAt time.Time
	UpdatedAt time.Time

	User           *User
	Group          *Group
	AssignedByUser *User
}

func (s *UserSubscription) IsActive() bool {
	return s.Status == SubscriptionStatusActive && time.Now().Before(s.ExpiresAt)
}

func (s *UserSubscription) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

func (s *UserSubscription) DaysRemaining() int {
	if s.IsExpired() {
		return 0
	}
	return int(time.Until(s.ExpiresAt).Hours() / 24)
}

func (s *UserSubscription) IsWindowActivated() bool {
	return s.DailyWindowStart != nil || s.WeeklyWindowStart != nil || s.MonthlyWindowStart != nil
}

func (s *UserSubscription) HasOneTimeDailyQuota() bool {
	if s == nil || s.StartsAt.IsZero() || s.ExpiresAt.IsZero() {
		return false
	}
	return !s.ExpiresAt.After(s.StartsAt.AddDate(0, 0, 1))
}

func (s *UserSubscription) NeedsDailyReset() bool {
	return s.NeedsDailyResetAt(time.Now())
}

func (s *UserSubscription) NeedsDailyResetAt(now time.Time) bool {
	if s.DailyWindowStart == nil {
		return false
	}
	if s.HasOneTimeDailyQuota() {
		return false
	}
	return !now.Before(s.DailyWindowStart.Add(24 * time.Hour))
}

func (s *UserSubscription) NeedsWeeklyReset() bool {
	if s.WeeklyWindowStart == nil {
		return false
	}
	return time.Since(*s.WeeklyWindowStart) >= 7*24*time.Hour
}

func (s *UserSubscription) NeedsMonthlyReset() bool {
	if s.MonthlyWindowStart == nil {
		return false
	}
	return time.Since(*s.MonthlyWindowStart) >= 30*24*time.Hour
}

func (s *UserSubscription) DailyResetTime() *time.Time {
	if s.DailyWindowStart == nil {
		return nil
	}
	if s.HasOneTimeDailyQuota() {
		t := s.ExpiresAt
		return &t
	}
	t := s.DailyWindowStart.Add(24 * time.Hour)
	return &t
}

func (s *UserSubscription) WeeklyResetTime() *time.Time {
	if s.WeeklyWindowStart == nil {
		return nil
	}
	t := s.WeeklyWindowStart.Add(7 * 24 * time.Hour)
	return &t
}

func (s *UserSubscription) MonthlyResetTime() *time.Time {
	if s.MonthlyWindowStart == nil {
		return nil
	}
	t := s.MonthlyWindowStart.Add(30 * 24 * time.Hour)
	return &t
}

func (s *UserSubscription) CheckDailyLimit(group *Group, additionalCost float64) bool {
	if !group.HasDailyLimit() {
		return true
	}
	return s.DailyUsageUSD+additionalCost <= *group.DailyLimitUSD
}

func (s *UserSubscription) CheckWeeklyLimit(group *Group, additionalCost float64) bool {
	if !group.HasWeeklyLimit() {
		return true
	}
	return s.WeeklyUsageUSD+additionalCost <= *group.WeeklyLimitUSD
}

func (s *UserSubscription) CheckMonthlyLimit(group *Group, additionalCost float64) bool {
	if !group.HasMonthlyLimit() {
		return true
	}
	return s.MonthlyUsageUSD+additionalCost <= *group.MonthlyLimitUSD
}

func (s *UserSubscription) CheckAllLimits(group *Group, additionalCost float64) (daily, weekly, monthly bool) {
	daily = s.CheckDailyLimit(group, additionalCost)
	weekly = s.CheckWeeklyLimit(group, additionalCost)
	monthly = s.CheckMonthlyLimit(group, additionalCost)
	return
}

// ===== Burn-down 计费模型 =====

// shanghaiLoc 是清扣/进度计算使用的时区（东八区）。
var shanghaiLoc = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		// 退回固定 UTC+8，避免缺少 tzdata 时 panic
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}()

// RemainingUSD 返回本卡当前剩余可用额度 = 发放 - 已消费 - 已清扣。
func (s *UserSubscription) RemainingUSD() float64 {
	r := s.GrantedTotalUSD - s.ConsumedUSD - s.ClawedUSD
	if r < 0 {
		return 0
	}
	return r
}

// IsDepleted 报告本卡 burn-down 余额是否已耗尽（剩余 ≤ ~0）。耗尽的卡会在扣费事务中被即时
// 标记 expired（用完即失效），不再等到时间到期。仅对 burn-down 卡（G>0）成立；legacy/standard
// 卡（G=0）不参与 burn-down 额度，永远返回 false。
func (s *UserSubscription) IsDepleted() bool {
	return s.GrantedTotalUSD > 0 && s.GrantedTotalUSD-s.ConsumedUSD-s.ClawedUSD <= 1e-7
}

// ClawbackClock 返回清扣时钟起点：优先 ActivatedAt，否则回退 StartsAt。
func (s *UserSubscription) ClawbackClock() time.Time {
	if s.ActivatedAt != nil && !s.ActivatedAt.IsZero() {
		return *s.ActivatedAt
	}
	return s.StartsAt
}

// TotalDays 返回本卡的总天数 = round(G / D)。D 为 0 时返回 0。
func (s *UserSubscription) TotalDays() int {
	if s.DailyAmountUSD <= 0 {
		return 0
	}
	return int((s.GrantedTotalUSD / s.DailyAmountUSD) + 0.5)
}

// startOfShanghaiDay 返回 t 所在的东八区当日 0 点。
func startOfShanghaiDay(t time.Time) time.Time {
	t = t.In(shanghaiLoc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, shanghaiLoc)
}

// CalendarDayAt 返回截至 now、自激活起经过的东八区零点数 N（激活当天为 0），上限为 TotalDays。
func (s *UserSubscription) CalendarDayAt(now time.Time) int {
	start := startOfShanghaiDay(s.ClawbackClock())
	cur := startOfShanghaiDay(now)
	n := int(cur.Sub(start).Hours()/24 + 0.5)
	if n < 0 {
		n = 0
	}
	if days := s.TotalDays(); days > 0 && n > days {
		n = days
	}
	return n
}

// ConsumptionDay 返回消费进度天 = 累计消费 / D（可超过日历天，表示已透支到第几天）。
func (s *UserSubscription) ConsumptionDay() float64 {
	if s.DailyAmountUSD <= 0 {
		return 0
	}
	return s.ConsumedUSD / s.DailyAmountUSD
}

// ClawbackFloorAt 返回截至日历天 N 时本卡剩余池子的下限 floor = max(0, G - N×D)。
func (s *UserSubscription) ClawbackFloorAt(now time.Time) float64 {
	n := s.CalendarDayAt(now)
	floor := s.GrantedTotalUSD - float64(n)*s.DailyAmountUSD
	if floor < 0 {
		return 0
	}
	return floor
}

// ClawbackShortfallAt 返回截至 now 应清扣的金额 = max(0, remaining - floor)。
//
// 天卡（TotalDays<=1）整张卡只有一天、到期即由作废流程处理，日内不做 clawback：
// 否则按「日历午夜」计天会把深夜买的卡在次日 0 点误扣光（如 23:55 买的天卡 5 分钟后清零）。
func (s *UserSubscription) ClawbackShortfallAt(now time.Time) float64 {
	if s.TotalDays() <= 1 {
		return 0
	}
	shortfall := s.RemainingUSD() - s.ClawbackFloorAt(now)
	if shortfall < 0 {
		return 0
	}
	return shortfall
}

// DailySpentAt 返回截至 now、本卡在「当前日历天」内已消费的额度。
// 采用惰性重置：仅当 DailySpentDay 等于当前日历天 N 时其值有效，否则视为 0
// （新的一天 daily_spent 自动归零，不跨天结转）。
func (s *UserSubscription) DailySpentAt(now time.Time) float64 {
	if s == nil {
		return 0
	}
	if s.DailySpentDay == s.CalendarDayAt(now) {
		if s.DailySpentUSD < 0 {
			return 0
		}
		return s.DailySpentUSD
	}
	return 0
}

// SpendableNowAt 返回在「最多往后透支 overdraftDays 天」约束下，本卡当前可被扣费的额度。
// 模型：整张卡 G 一次性进 users.balance，但每个日历天最多消费 D（每日限速）；透支允许某天突破到
// (1+overdraftDays)×D，相当于把后续天的额度提前花（计费周期前移）。本函数为「准入闸门」估算：
//
//	spendableNow = clamp(0, min(remaining, (1 + overdraftDays) × D − dailySpent(now)))
//
// 与旧的「累计式 (N+1+OD)×D − consumed」不同：不再按累计消费扣减，故**透支之后的次日照常解锁 D、
// 不锁死、无还债**；超前消费只是让卡更早用完。dailySpent 按日历天惰性归零，故不跨天结转。
// D=0（legacy/standard 卡）时返回 0，无副作用。
func (s *UserSubscription) SpendableNowAt(now time.Time, overdraftDays int) float64 {
	if s == nil || s.DailyAmountUSD <= 0 {
		return 0
	}
	remaining := s.RemainingUSD()
	avail := float64(1+overdraftDays)*s.DailyAmountUSD - s.DailySpentAt(now)
	if avail < 0 {
		avail = 0
	}
	if avail > remaining {
		avail = remaining
	}
	return avail
}

// LockedAt 返回因「最多透支 overdraftDays 天」约束而暂不可用、但仍计入 users.balance 的本卡金额。
// = remaining − SpendableNowAt。准入闸门用「balance − Σ locked」作为有效可花额度。
func (s *UserSubscription) LockedAt(now time.Time, overdraftDays int) float64 {
	locked := s.RemainingUSD() - s.SpendableNowAt(now, overdraftDays)
	if locked < 0 {
		return 0
	}
	return locked
}

// CanEnableOverdraft 报告本卡是否仍允许开启透支功能（累计预支天数未达 5 天硬上限）。
func (s *UserSubscription) CanEnableOverdraft() bool {
	if s == nil {
		return false
	}
	return s.TotalOverdraftCount < MaxSubscriptionOverdraftUses
}

// RemainingOverdraftUses 返回本卡剩余可往后预支的天数（5 − 已累计预支天数）。
func (s *UserSubscription) RemainingOverdraftUses() int {
	if s == nil {
		return 0
	}
	remaining := MaxSubscriptionOverdraftUses - s.TotalOverdraftCount
	if remaining < 0 {
		return 0
	}
	return remaining
}

// EffectiveOverdraftDaysAt 返回本卡当前「今日实际可预支天数」，准入闸门与扣费分摊须用同一口径。
//
//	effOD = clamp(0, min(用户设置 max_overdraft_days, 生命周期剩余预支额度 + 今日已预支天数))
//
// 取 min(配置, 剩余预支) 确保不超 5 天硬上限（修复闸门按配置 6D 放行、实际却已无额度的越权）；
// 而「+ 今日已预支天数」是把当天已计入 TotalOverdraftCount 的预支加回——否则当日额度会随当天消费
// 自我收缩，导致 5 天预算在单日内分多笔时用不满。未开透支或 D=0 时返回 0。
func (s *UserSubscription) EffectiveOverdraftDaysAt(now time.Time) int {
	if s == nil || s.MaxOverdraftDays == nil || s.DailyAmountUSD <= 0 {
		return 0
	}
	preToday := preSpendDays(s.DailySpentAt(now), s.DailyAmountUSD)
	budget := MaxSubscriptionOverdraftUses - s.TotalOverdraftCount + preToday
	d := *s.MaxOverdraftDays
	if d > budget {
		d = budget
	}
	if d < 0 {
		d = 0
	}
	return d
}

// UsesOverdraftAt 报告本次从该卡分摊 amount 后，当天用量是否突破了当日额度 D（即发生了预支）。
// 等价于 OverdraftDaysDeltaAt(now, amount) > 0。
func (s *UserSubscription) UsesOverdraftAt(now time.Time, amount float64) bool {
	return s.OverdraftDaysDeltaAt(now, amount) > 0
}

// preSpendDays 返回「当天用量 used 突破当日额度 D 之后、额外预支了几天」。
//
//	preSpend = max(0, ceil(used/D) − 1)   // used≤D→0；D<used≤2D→1；…
//
// −1e-9 吸收 numeric→float 尾差，使「恰好用满整数天额度」不被误判为多预支一天。
func preSpendDays(used, daily float64) int {
	if used <= 0 || daily <= 0 {
		return 0
	}
	n := int(math.Ceil(used/daily-1e-9)) - 1
	if n < 0 {
		return 0
	}
	return n
}

// OverdraftDaysDeltaAt 返回本次分摊 amount 后「当天新增的预支天数」。
//
//	before = ⌈dailySpent(now)/D⌉−1 的预支天数      // 本次之前当天已预支的天数
//	after  = ⌈(dailySpent(now)+amount)/D⌉−1 的预支天数
//	delta  = max(0, after − before)
//
// 仅统计「本笔把当天用量推到更靠后的天」所新增的天数：同一天内多笔小请求触及同一天只算一次。
// 调用方对 total_overdraft_count 做「+delta（封顶上限）」累加。D=0 或 amount≤0 时返回 0。
func (s *UserSubscription) OverdraftDaysDeltaAt(now time.Time, amount float64) int {
	if s == nil || s.DailyAmountUSD <= 0 || amount <= 0 {
		return 0
	}
	before := s.DailySpentAt(now)
	after := before + amount
	delta := preSpendDays(after, s.DailyAmountUSD) - preSpendDays(before, s.DailyAmountUSD)
	if delta < 0 {
		return 0
	}
	return delta
}
