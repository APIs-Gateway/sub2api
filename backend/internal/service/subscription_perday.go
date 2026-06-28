package service

import (
	"fmt"
	"time"
)

// per-day 每日额度模型的纯计费引擎（per-day redesign 第 4 节）。
//
// 设计：把「惰性覆盖 + 准入 + 结算瀑布」实现成纯函数，在内存值结构（PerDayCard/WalletState）
// 上运算并就地变更。repository 层在事务内 SELECT ... FOR UPDATE 取行→映射成这两个结构→调用
// 本引擎→把变更写回。好处：唯一真相源在 Go、可对规格第 8 节边界做穷尽单测、不在 SQL 里写业务。
//
// 关键不变量：PerDayCard.TodayRemaining 永远 ≥ 0；一切溢出/欠账只落在 WalletState.Balance（可负）。

// MaxMonthlyOverdraftUses 是「每用户每自然月」可透支的次数硬上限（规格默认 5，可调）。
// 与旧 burn-down 的 per-card MaxSubscriptionOverdraftUses 区分：新模型按用户/自然月计，
// 名下多张卡共享同一计数，次月 1 号（东八区）惰性重置。
const MaxMonthlyOverdraftUses = 5

const east8OffsetSeconds = 8 * 3600

// EastDayNumber 返回 t 的「东八区绝对自然日序号」= floor((unix + 8h) / 86400)。
// 同一序号空间用于 start_day / expire_day / today_day，可直接做 expire_day − today 等算术，
// 避免时间戳差值争议（如 23:58 买卡 / 00:00 重置）。业务时间均在 1970 之后，整除即等于 floor。
func EastDayNumber(t time.Time) int {
	return int((t.Unix() + east8OffsetSeconds) / 86400)
}

// TodayEastDayNumber 返回当前东八区自然日序号。
func TodayEastDayNumber() int { return EastDayNumber(time.Now()) }

// EastDayStart 返回东八区自然日序号 day 的当日 0 点时间戳。
func EastDayStart(day int) time.Time {
	return time.Unix(int64(day)*86400-east8OffsetSeconds, 0).In(shanghaiLoc)
}

// ExpireDayToExpiresAt 把 expire_day（最后服务日，含）换算成 expires_at 时间戳 = 次日 0 点
// （卡服务到 expire_day 当日结束）。per-day 以 expire_day 为唯一真相源，但大量旧路径仍按
// expires_at 判 active/expired（GetActiveByUserIDAndGroupID/BatchUpdateExpiredStatus 等），
// 故凡写 expire_day 处都用本函数同步 expires_at，使两套口径一致：
//
//	today > expire_day  ⟺  now ≥ ExpireDayToExpiresAt(expire_day)
func ExpireDayToExpiresAt(expireDay int) time.Time {
	return EastDayStart(expireDay + 1)
}

// ExpiresAtToExpireDay 把 expires_at 反推成 expire_day（最后服务日，含）。
// 仅适用于 per-day 规范化后的边界：`ExpireDayToExpiresAt(day)` 的逆变换。
func ExpiresAtToExpireDay(expiresAt time.Time) int {
	return EastDayNumber(expiresAt) - 1
}

// MaxExpireDay 返回 expire_day 的上限（东八区自然日序号）= MaxExpiresAt 对应的最后服务日。
func MaxExpireDay() int { return EastDayNumber(MaxExpiresAt) - 1 }

// ClampExpireDay 把 expire_day 夹到 ≤ MaxExpireDay()，防止建卡/续费/延长写出超过 MaxExpiresAt
// 的有效期（凡算出新 expire_day 处都过一遍，避免「超长有效期」从不同入口复现）。
func ClampExpireDay(expireDay int) int {
	if max := MaxExpireDay(); expireDay > max {
		return max
	}
	return expireDay
}

// EastMonthKey 返回 t 的东八区月份键 YYYYMM。
func EastMonthKey(t time.Time) string {
	t = t.In(shanghaiLoc)
	return fmt.Sprintf("%04d%02d", t.Year(), int(t.Month()))
}

// CurrentEastMonthKey 返回当前东八区月份键。
func CurrentEastMonthKey() string { return EastMonthKey(time.Now()) }

// PerDayCard 是 per-day 模型下一张订阅卡的最小可计费状态（映射 user_subscriptions 新字段）。
type PerDayCard struct {
	DailyAmountUSD float64 // D，每日额度（官方刀/天）
	TodayRemaining float64 // 今日剩余（官方刀，1:1，永不为负）
	TodayDay       int     // today_remaining 所属东八区日序号；-1=未初始化
	DailySpentUSD  float64 // 今日套餐侧已实际扣掉的官方刀（含透支借天后扣掉的额度）
	DailySpentDay  int     // daily_spent_usd 所属东八区日序号；与 today_day 同口径
	StartDay       int     // 激活日（东八区日序号）
	ExpireDay      int     // 最后发放 D 的东八区日序号（含）；每透支 −1
	OverdraftOn    bool    // 本卡是否开启透支
	Expired        bool    // 是否已被惰性标记为过期（status='expired'）
}

// WalletState 是用户钱包（其他余额）+ 透支月度计数的最小状态（映射 users 新字段）。
type WalletState struct {
	Balance               float64 // users.balance（赠送+充值；可负）
	MonthlyOverdraftCount int     // 当前自然月已透支次数
	MonthlyOverdraftMonth string  // 上一计数所属东八区月份 YYYYMM
}

// ResetIfNewDay 惰性覆盖：按 today 把 today_remaining 对齐到当日。返回是否发生变更。
//   - today == today_day：今天已处理 → 不动。
//   - today <= expire_day：仍在有效期 → today_remaining = D，today_day = today。
//   - today >  expire_day：已过期 → today_remaining = 0、标记 expired，绝不再发额度
//     （防过期卡跨天被重置出额度）。
func (c *PerDayCard) ResetIfNewDay(today int) bool {
	if c.DailySpentDay != today {
		c.DailySpentUSD = 0
		c.DailySpentDay = today
	}
	if c.TodayDay == today {
		return false
	}
	if today <= c.ExpireDay {
		c.TodayRemaining = c.DailyAmountUSD
		c.TodayDay = today
	} else {
		c.TodayRemaining = 0
		c.TodayDay = today
		c.Expired = true
	}
	return true
}

// ResetMonthIfNeeded 惰性按月重置透支计数：月份变了就清零并记新月份。返回是否变更。
func (w *WalletState) ResetMonthIfNeeded(month string) bool {
	if w.MonthlyOverdraftMonth == month {
		return false
	}
	w.MonthlyOverdraftCount = 0
	w.MonthlyOverdraftMonth = month
	return true
}

// TodaySpentFromPackage 返回本卡今天已从套餐余额扣掉的官方成本。
// 转套餐当天「新卡套餐余额 = max(0, D_新 − 旧卡今日已用)」时用它。调用前应已 ResetIfNewDay(today)。
// per-day 热路径维护 DailySpentUSD；若读取到旧数据（DailySpentDay 未对齐），退化为 D − today_remaining。
func (c *PerDayCard) TodaySpentFromPackage(today int) float64 {
	if c.DailySpentDay == today {
		if c.DailySpentUSD < 0 {
			return 0
		}
		return c.DailySpentUSD
	}
	if c.TodayDay != today {
		return 0
	}
	spent := c.DailyAmountUSD - c.TodayRemaining
	if spent < 0 {
		return 0
	}
	return spent
}

// ToPerDayCard 把订阅卡模型投影成 per-day 引擎所需的最小状态。
func (s *UserSubscription) ToPerDayCard() PerDayCard {
	if s == nil {
		return PerDayCard{}
	}
	return PerDayCard{
		DailyAmountUSD: s.DailyAmountUSD,
		TodayRemaining: s.TodayRemaining,
		TodayDay:       s.TodayDay,
		DailySpentUSD:  s.DailySpentUSD,
		DailySpentDay:  s.DailySpentDay,
		StartDay:       s.StartDay,
		ExpireDay:      s.ExpireDay,
		OverdraftOn:    s.OverdraftOn,
		Expired:        s.Status == SubscriptionStatusExpired,
	}
}

// RefundableDays 返回可退/剩余服务天数 = max(0, expire_day − today)。
// 今天已开始服务、不计入可退；expire_day 已含「已用天 + 透支借走天」的扣减（每透支 expire_day−1）。
func (c *PerDayCard) RefundableDays(today int) int {
	d := c.ExpireDay - today
	if d < 0 {
		return 0
	}
	return d
}
