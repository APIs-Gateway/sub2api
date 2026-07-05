package service

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// entGroupIDValue 把 ent 可空 group_id(*int64) 映射为 domain/业务侧 int64：
// NULL（自定义 D+T 卡无 group 归属）→ 0。与 repository.groupIDValue 同语义，供 service 层直读 ent 实体时使用。
func entGroupIDValue(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

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

	// 三窗口限额（per-day→三窗口 redesign，限额从 group 搬到订阅卡自身）；nil = 该窗口不限。
	// 与上方 *_usage_usd 配对：三窗口 usage < limit 才由订阅 1:1 覆盖（见 subscription_window.go）。
	DailyLimitUSD   *float64
	WeeklyLimitUSD  *float64
	MonthlyLimitUSD *float64

	// Burn-down 计费模型字段
	GrantedTotalUSD     float64    // G = D × days，开通时一次性发放总额
	DailyAmountUSD      float64    // D，开通时对 group.daily_limit_usd 的快照
	ConsumedUSD         float64    // 本卡累计消费（单调递增）
	ClawedUSD           float64    // 本卡累计被清扣（单调递增）
	LastClawbackDay     int        // 已对账到的最高日历天 N
	MaxOverdraftDays    *int       // 【已退役·留列待上线前 drop】旧 per-card 透支开关；三窗口模型改用用户级月度透支（users.monthly_overdraft_count + ManualOverdraftWindow）
	TotalOverdraftCount int        // 【已退役·留列待上线前 drop】旧 per-card 累计预支天数；新模型按用户/自然月计
	DailySpentUSD       float64    // 当前日内套餐侧已实际扣掉的官方刀（per-day 用于转套餐防双领）
	DailySpentDay       int        // per-day 为东八区绝对自然日序号；旧 burn-down 为自激活起日历天 N
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

	// Admin-only refund affordance for paid subscription cards.
	RefundOrderID     *int64
	RefundOrderStatus string
	RefundOrderAmount *float64
	RefundOrderPay    *float64
	RefundableAmount  *float64
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
	return s.NeedsWeeklyResetAt(time.Now())
}

// NeedsWeeklyResetAt 周窗口是否该重置:窗口起点早于「当前自然周起点(东八区周一 0 点)」即过期。
// P2#12:与计费侧 SubWindow.ResetWindows(timezone.StartOfWeek) 同口径——旧的 7×24h 滚动窗会与
// 自然周边界错位 0–6 天,导致 /v1/usage 展示的「已用/重置时间」与实际计费不一致。
func (s *UserSubscription) NeedsWeeklyResetAt(now time.Time) bool {
	if s.WeeklyWindowStart == nil {
		return false
	}
	return s.WeeklyWindowStart.Before(timezone.StartOfWeek(now))
}

func (s *UserSubscription) NeedsMonthlyReset() bool {
	return s.NeedsMonthlyResetAt(time.Now())
}

// NeedsMonthlyResetAt 月窗口是否该重置:窗口起点早于「当前自然月起点(东八区 1 号 0 点)」即过期。
// P2#12:与计费侧 SubWindow.ResetWindows(timezone.StartOfMonth) 同口径(旧的 30×24h 滚动窗会错位)。
func (s *UserSubscription) NeedsMonthlyResetAt(now time.Time) bool {
	if s.MonthlyWindowStart == nil {
		return false
	}
	return s.MonthlyWindowStart.Before(timezone.StartOfMonth(now))
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
	return s.WeeklyResetTimeAt(time.Now())
}

// WeeklyResetTimeAt 下次周重置 = 下一个自然周起点(东八区下周一 0 点)。P2#12:与计费自然周对齐(旧为 start+7d)。
func (s *UserSubscription) WeeklyResetTimeAt(now time.Time) *time.Time {
	if s.WeeklyWindowStart == nil {
		return nil
	}
	t := timezone.StartOfWeek(now).AddDate(0, 0, 7)
	return &t
}

func (s *UserSubscription) MonthlyResetTime() *time.Time {
	return s.MonthlyResetTimeAt(time.Now())
}

// MonthlyResetTimeAt 下次月重置 = 下一个自然月起点(东八区下月 1 号 0 点)。P2#12:与计费自然月对齐(旧为 start+30d)。
func (s *UserSubscription) MonthlyResetTimeAt(now time.Time) *time.Time {
	if s.MonthlyWindowStart == nil {
		return nil
	}
	t := timezone.StartOfMonth(now).AddDate(0, 1, 0)
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
	if s.StartDay > 0 {
		return s.clampCalendarDay(EastDayNumber(now) - s.StartDay)
	}
	if s.ExpireDay > 0 {
		days := s.TotalDays()
		if days > 0 {
			today := EastDayNumber(now)
			remainingDays := s.ExpireDay - today + 1
			if remainingDays < 0 {
				remainingDays = 0
			}
			return s.clampCalendarDay(days - remainingDays)
		}
	}

	start := startOfShanghaiDay(s.ClawbackClock())
	cur := startOfShanghaiDay(now)
	n := int(cur.Sub(start).Hours()/24 + 0.5)
	return s.clampCalendarDay(n)
}

func (s *UserSubscription) clampCalendarDay(n int) int {
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
