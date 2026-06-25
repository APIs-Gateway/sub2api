package service

import (
	"fmt"
	"math"
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

// canOverdraft 报告是否还能透支：本卡开了透支 + 本月未满 + 还有「未来天」可借（expire_day > today）。
// 注意：D 必须 > 0，否则借天不产生额度（防 D=0 卡空耗月度次数）。
func canOverdraft(c *PerDayCard, w *WalletState, today int) bool {
	return c.OverdraftOn &&
		c.DailyAmountUSD > 0 &&
		w.MonthlyOverdraftCount < MaxMonthlyOverdraftUses &&
		c.ExpireDay > today
}

// CanOverdraft 导出版（供准入闸/展示用）。调用前必须已 ResetIfNewDay(today) 且
// ResetMonthIfNeeded(monthKey)——否则跨月的 stale MonthlyOverdraftCount 会误判。
func CanOverdraft(c *PerDayCard, w *WalletState, today int) bool {
	return canOverdraft(c, w, today)
}

// Admit 准入（请求前）：套餐余额>0 或 钱包>0 或 可透支 —— 三者任一可用即放行。
// 与 Settle 对称地先做两个惰性重置：按 today 覆盖套餐余额、按 monthKey 重置透支月度计数
// （否则跨月后「仅透支可用」会被上个月的 stale count 误拒，而真正会重置的 Settle 在准入
// 失败后根本进不去）。调用方需把覆盖/重置结果写回。流式请求必须先放行，放行后只结算不拒绝。
func Admit(c *PerDayCard, w *WalletState, today int, monthKey string) bool {
	c.ResetIfNewDay(today)
	w.ResetMonthIfNeeded(monthKey)
	return c.TodayRemaining > 0 || w.Balance > 0 || canOverdraft(c, w, today)
}

// SettleResult 记录一次结算的扣费明细（用于落库/审计/测试断言）。
type SettleResult struct {
	SubPay        float64 // 从套餐余额(1:1)扣的官方刀
	WalletPay     float64 // 从钱包正余额扣的「售价货币」额（= 官方刀×倍率）
	OverdraftDays int     // 本次透支借走的天数（= expire_day 提前天数 = 月度计数增量）
	OverdraftPay  float64 // 透支补发后从套餐余额(1:1)扣的官方刀
	WalletNegPay  float64 // 最终缺口记入钱包负数的「售价货币」额（= 官方刀×倍率）
}

// TotalOfficial 返回本次实际从套餐侧（套餐余额+透支）扣的官方刀合计。
func (r SettleResult) TotalOfficial() float64 { return r.SubPay + r.OverdraftPay }

// Settle 按 per-day 瀑布结算单笔请求的官方成本 cost（multiplier=钱包计费倍率，仅对钱包生效）。
// 固定顺序：套餐余额(1:1) → 钱包正余额(×倍率) → 透支(借未来天1:1) → 钱包负数(兜底缺口)。
// 就地变更 c、w；不变量：c.TodayRemaining 永不为负，溢出全落 w.Balance（可负）。
func Settle(c *PerDayCard, w *WalletState, cost, multiplier float64, today int, monthKey string) SettleResult {
	if multiplier <= 0 {
		multiplier = 1
	}
	var res SettleResult

	c.ResetIfNewDay(today)
	w.ResetMonthIfNeeded(monthKey)

	C := cost
	if C <= 0 {
		return res
	}

	// 1) 套餐余额（1:1，最多扣到 0，永不为负）
	if c.TodayRemaining > 0 {
		subPay := math.Min(c.TodayRemaining, C)
		c.TodayRemaining -= subPay
		C -= subPay
		res.SubPay = subPay
	}

	// 2) 钱包正余额（×倍率）：只用钱包正数部分
	if C > 0 && w.Balance > 0 {
		payOfficial := math.Min(C, w.Balance/multiplier)
		if payOfficial > 0 {
			walletPay := payOfficial * multiplier
			if walletPay > w.Balance { // 防浮点把 balance 扣成极小负
				walletPay = w.Balance
			}
			w.Balance -= walletPay
			C -= payOfficial
			res.WalletPay = walletPay
		}
	}

	// 3) 透支（1:1，借未来天）：expire_day−1 + 给套餐余额补发 D + 月度计数+1，循环至 C=0 或不可再借。
	for C > 0 && canOverdraft(c, w, today) {
		c.ExpireDay--
		w.MonthlyOverdraftCount++
		res.OverdraftDays++
		use := math.Min(c.DailyAmountUSD, C)
		C -= use
		c.TodayRemaining += c.DailyAmountUSD - use // 借来未用完的部分留作当日后续
		res.OverdraftPay += use
	}

	// 4) 最终缺口 → 钱包负数（套餐余额绝不为负；钱包负数不随次日清零）
	if C > 0 {
		neg := C * multiplier
		w.Balance -= neg
		res.WalletNegPay = neg
	}
	return res
}

// TodaySpentFromPackage 返回本卡今天已从套餐余额扣掉的官方成本（= D − today_remaining，下限 0）。
// 转套餐当天「新卡套餐余额 = max(0, D_新 − 旧卡今日已用)」时用它。调用前应已 ResetIfNewDay(today)。
func (c *PerDayCard) TodaySpentFromPackage(today int) float64 {
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
		StartDay:       s.StartDay,
		ExpireDay:      s.ExpireDay,
		OverdraftOn:    s.OverdraftOn,
		Expired:        s.Status == SubscriptionStatusExpired,
	}
}

// ToWalletState 把用户模型投影成 per-day 引擎所需的钱包/月度透支状态。
func (u *User) ToWalletState() WalletState {
	if u == nil {
		return WalletState{}
	}
	return WalletState{
		Balance:               u.Balance,
		MonthlyOverdraftCount: u.MonthlyOverdraftCount,
		MonthlyOverdraftMonth: u.MonthlyOverdraftMonth,
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
