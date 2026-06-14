package service

import "time"

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
	GrantedTotalUSD  float64    // G = D × days，开通时一次性发放总额
	DailyAmountUSD   float64    // D，开通时对 group.daily_limit_usd 的快照
	ConsumedUSD      float64    // 本卡累计消费（单调递增）
	ClawedUSD        float64    // 本卡累计被清扣（单调递增）
	LastClawbackDay  int        // 已对账到的最高日历天 N
	MaxOverdraftDays *int       // 本卡最多往后透支天数；nil = 不限制（用户在「我的订阅」自助设置）
	ActivatedAt      *time.Time // 清扣时钟起点；nil 时回退 StartsAt

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
func (s *UserSubscription) ClawbackShortfallAt(now time.Time) float64 {
	shortfall := s.RemainingUSD() - s.ClawbackFloorAt(now)
	if shortfall < 0 {
		return 0
	}
	return shortfall
}

// SpendableNowAt 返回在「最多往后透支 overdraftDays 天」约束下，本卡当前可被扣费的额度。
// 模型仍一次性把 G 发到 users.balance，本函数只用于「准入闸门」估算：
//
//	chargeCap    = (CalendarDayAt(now) + overdraftDays) × D   // 截至当前允许累计被扣到的上限
//	spendableNow = clamp(0, min(remaining, chargeCap − consumed))
//
// D=0（legacy/standard 卡）时 chargeCap=0、remaining=0，返回 0，无副作用。
func (s *UserSubscription) SpendableNowAt(now time.Time, overdraftDays int) float64 {
	remaining := s.RemainingUSD()
	chargeCap := (float64(s.CalendarDayAt(now)) + float64(overdraftDays)) * s.DailyAmountUSD
	avail := chargeCap - s.ConsumedUSD
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
