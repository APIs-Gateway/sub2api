package service

import (
	"errors"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// 三窗口限额计费引擎（恢复 upstream 日/周/月 usage-vs-limit + fork 的「订阅覆盖→钱包兜底」瀑布）。
// 详见 docs/billing-perday-redesign.md §4。设计同 per-day 引擎：纯函数在内存值结构上运算并就地变更，
// repository 层在事务内 SELECT ... FOR UPDATE 取行 → 映射成 SubWindow/WalletState → 调本引擎 → 写回。
//
// 与旧 per-day pool 的本质区别：订阅不是「一笔可花余额(today_remaining)」，而是「日/周/月 三个用量上限」。
// 关键不变量：
//   - 订阅 *_usage 只升不退；撞某窗口上限即该窗口停止覆盖（不存在“窗口重置清零白嫖”，溢出全落钱包）。
//   - 一切溢出/欠账只落 WalletState.Balance（可负，不随窗口重置清零）。
//   - 透支不在 SettleWindow 内：由 ManualOverdraftWindow 显式触发（仅解“日上限” + expires_at−1 天）。
//
// 时区口径：窗口边界用 timezone.StartOfDay/Week/Month（默认 Asia/Shanghai，周一/1 号起）；
// 退款/透支借天的“整天”算术复用 EastDayNumber/ExpireDayToExpiresAt（东八区绝对日序号），二者在
// Asia/Shanghai 部署下一致。

// SubWindow 是三窗口模型下一张订阅卡的最小可计费状态（映射 user_subscriptions 的限额/用量/窗口起点/有效期）。
type SubWindow struct {
	// 限额（本版从 group 搬到卡上）；<=0（含 NULL→0）= 该窗口不限。
	DailyLimitUSD   float64 // D
	WeeklyLimitUSD  float64 // W
	MonthlyLimitUSD float64 // M

	// 用量（沿用 upstream，累加、只升；撞上限即该窗口停止覆盖）。
	DailyUsageUSD   float64
	WeeklyUsageUSD  float64
	MonthlyUsageUSD float64

	// 窗口起点（沿用 upstream；nil = 未激活，首次访问惰性设为当前周期起点）。
	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time

	ExpiresAt time.Time // 有效期（东八区，自然日折算）；透支每次提前 1 天
	Status    string    // active / expired

	JustExpired bool // 本次被惰性判定为过期（加载时 status='active' 但 now≥expires_at）；供调用方落库 status='expired' + 失效缓存
}

// active 报告卡当前是否生效：status='active' 且未过期。
func (c *SubWindow) active(now time.Time) bool {
	return c.Status == SubscriptionStatusActive && now.Before(c.ExpiresAt)
}

// ResetWindows 惰性按东八区自然 日/周/月 边界重置三窗口用量：窗口起点早于当前周期起点（或为 nil）
// → 对应 *_usage 归零、起点设为当前周期起点。返回是否发生变更。
func (c *SubWindow) ResetWindows(now time.Time) bool {
	changed := false
	if ds := timezone.StartOfDay(now); c.DailyWindowStart == nil || c.DailyWindowStart.Before(ds) {
		c.DailyUsageUSD = 0
		c.DailyWindowStart = &ds
		changed = true
	}
	if ws := timezone.StartOfWeek(now); c.WeeklyWindowStart == nil || c.WeeklyWindowStart.Before(ws) {
		c.WeeklyUsageUSD = 0
		c.WeeklyWindowStart = &ws
		changed = true
	}
	if ms := timezone.StartOfMonth(now); c.MonthlyWindowStart == nil || c.MonthlyWindowStart.Before(ms) {
		c.MonthlyUsageUSD = 0
		c.MonthlyWindowStart = &ms
		changed = true
	}
	return changed
}

// SubRemaining 返回三窗口剩余的最小值（某窗口剩余 = limit − usage；limit<=0 视为该窗口不限）；clamp ≥0。
// 调用前应已 ResetWindows(now)。
//
// 防御性安全闸（review #1/#2）：**三限额全为 NULL/0 的卡视为「未配置/未回填」→ 订阅不覆盖、返回 0**
// （回落钱包），绝不解释成「无限 1:1 全覆盖」。正常订阅卡开通时必带 D>0；若某卡三限额全空（漏回填 /
// 映射缺失 / 脏数据），此分支堵死「订阅免费覆盖所有官方成本、不扣钱包」的资损入口。
// 只要至少配了一档（哪怕只配了 W 或 M），其余 NULL 档按「该窗口不限」处理（仍受已配档封顶，不会失控）。
func (c *SubWindow) SubRemaining() float64 {
	rem := math.Inf(1)
	anyLimit := false
	if c.DailyLimitUSD > 0 {
		rem = math.Min(rem, c.DailyLimitUSD-c.DailyUsageUSD)
		anyLimit = true
	}
	if c.WeeklyLimitUSD > 0 {
		rem = math.Min(rem, c.WeeklyLimitUSD-c.WeeklyUsageUSD)
		anyLimit = true
	}
	if c.MonthlyLimitUSD > 0 {
		rem = math.Min(rem, c.MonthlyLimitUSD-c.MonthlyUsageUSD)
		anyLimit = true
	}
	if !anyLimit {
		return 0 // 未配置卡：订阅不覆盖
	}
	if rem < 0 {
		return 0
	}
	return rem
}

// WindowSettleResult 记录一次三窗口结算的扣费明细（用于落库/审计/测试断言）。
type WindowSettleResult struct {
	SubCover     float64 // 订阅覆盖的官方刀（1:1，加到三窗口 usage；不乘倍率、不扣钱包）
	WalletPay    float64 // 从钱包正余额扣的售价货币额（= 官方刀 × 倍率）
	WalletNegPay float64 // 最终缺口记入钱包负数的售价货币额（= 官方刀 × 倍率）
}

// AdmitWindow 准入（请求前）：有生效卡看三窗口余量、撞上限/无卡看钱包。先惰性重置窗口。
// 透支不参与准入（它是用户事先的独立动作，借完体现在“日窗口又有余量”里）。流式请求先放行、放行后只结算。
func AdmitWindow(c *SubWindow, w *WalletState, now time.Time) bool {
	if c != nil && c.active(now) {
		c.ResetWindows(now)
		if c.SubRemaining() > 0 {
			return true
		}
		return w.Balance > 0 // 撞窗口上限 → 回落钱包
	}
	return w.Balance > 0 // 无生效卡 → 钱包标准计费
}

// SettleWindow 按三窗口瀑布结算单笔请求的官方成本 cost（multiplier=钱包计费倍率，仅对钱包生效）。
// 固定顺序：订阅覆盖（1:1，加三窗口 usage） → 钱包正余额（×倍率） → 钱包负数（兜底缺口）。
// 不含透支（由 ManualOverdraftWindow 显式触发）。就地变更 c、w；订阅 usage 只升、撞上限即停，溢出全落 w.Balance。
func SettleWindow(c *SubWindow, w *WalletState, cost, multiplier float64, now time.Time) WindowSettleResult {
	if multiplier <= 0 {
		multiplier = 1
	}
	var res WindowSettleResult

	active := c != nil && c.active(now)
	if c != nil {
		if active {
			c.ResetWindows(now)
		} else if c.Status == SubscriptionStatusActive {
			c.JustExpired = true // 加载时 active、now≥expires_at → 本次惰性过期
		}
	}

	C := cost
	if C <= 0 {
		return res
	}

	// 1) 订阅覆盖（1:1，加三窗口 usage；撞上限即该窗口停止覆盖）
	if active {
		cover := math.Min(C, c.SubRemaining())
		if cover > 0 {
			c.DailyUsageUSD += cover
			c.WeeklyUsageUSD += cover
			c.MonthlyUsageUSD += cover
			C -= cover
			res.SubCover = cover
		}
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

	// 3) 最终缺口 → 钱包负数（订阅 usage 绝不“扣负”；钱包负数不随窗口重置清零）
	if C > 0 {
		neg := C * multiplier
		w.Balance -= neg
		res.WalletNegPay = neg
	}
	return res
}

// 手动透支错误（仅解“日上限”）。
var (
	ErrOverdraftNoActiveCard      = errors.New("no active subscription card to overdraft")
	ErrOverdraftDailyNotExhausted = errors.New("daily limit not exhausted; overdraft not needed")
	ErrOverdraftMonthlyLimit      = errors.New("monthly overdraft limit reached")
	ErrOverdraftNoFutureDay       = errors.New("no future day left to borrow for overdraft")
)

// ManualOverdraftWindow 用户手动透支（仅解“日上限”）：daily_usage 清零（刷新当日额度）+ expires_at 提前 1 天
// （借未来一天）+ 用户级月度计数 +1。周/月上限照样生效（本函数不动 weekly/monthly usage）。
// 调用方须在事务内锁 user 行 + active 卡行，并在调用前 w.ResetMonthIfNeeded(monthKey) 惰性按月重置计数。
//
// 校验（顺序即错误优先级）：
//  1. 有生效卡（active）；
//  2. 当日额度已撞满（daily_limit>0 且 daily_usage≥daily_limit）——按钮语义是“今天 D 用完了想继续用”，
//     未撞满则无需透支（不让用户白白损失一天有效期 + 一次月度额度）。先 ResetWindows 对齐自然日：
//     新的一天 daily_usage 已自动归零→不算撞满→拒；
//  3. 本月透支 < MaxMonthlyOverdraftUses；
//  4. 仍有“未来一天”可借（最后服务日 > 今天）。
//
// 注意：撞日上限即可借，即便周/月窗口也撞上限也允许（透支只清日窗口、对 W/M 无效，用户借了也越不过 W/M，
// 见 spec §8 场景 22）；不在此额外拦截。
func ManualOverdraftWindow(c *SubWindow, w *WalletState, now time.Time) error {
	if c == nil || !c.active(now) {
		return ErrOverdraftNoActiveCard
	}
	c.ResetWindows(now) // 先按自然边界对齐（新的一天 daily_usage 已归零 → 不算撞满）
	if !(c.DailyLimitUSD > 0 && c.DailyUsageUSD >= c.DailyLimitUSD) {
		return ErrOverdraftDailyNotExhausted
	}
	if w.MonthlyOverdraftCount >= MaxMonthlyOverdraftUses {
		return ErrOverdraftMonthlyLimit
	}
	today := EastDayNumber(now)
	lastServiceDay := ExpiresAtToExpireDay(c.ExpiresAt) // = EastDayNumber(expires_at) − 1
	if lastServiceDay <= today {
		return ErrOverdraftNoFutureDay
	}
	c.DailyUsageUSD = 0                                    // 刷新当日额度（仅日窗口清零）
	c.ExpiresAt = ExpireDayToExpiresAt(lastServiceDay - 1) // 借未来一天：最后服务日 −1
	w.MonthlyOverdraftCount++
	return nil
}

// RefundableDaysByExpiry 返回剩余可退/服务天数 = max(0, 最后服务日 − 今天)。
// 最后服务日 = EastDayNumber(expiresAt) − 1；expires_at 已含透支借天的扣减（每透支提前 1 天）。
// 今天已开始服务、不计入可退（与 per-day RefundableDays 同口径，只是真相源换成 expires_at）。
func RefundableDaysByExpiry(expiresAt, now time.Time) int {
	if d := ExpiresAtToExpireDay(expiresAt) - EastDayNumber(now); d > 0 {
		return d
	}
	return 0
}

// ToSubWindow 把订阅卡领域模型投影成三窗口引擎所需的最小状态。limit 字段为 *float64（NULL=不限→0）。
func (s *UserSubscription) ToSubWindow() SubWindow {
	if s == nil {
		return SubWindow{}
	}
	return SubWindow{
		DailyLimitUSD:      nilToZero(s.DailyLimitUSD),
		WeeklyLimitUSD:     nilToZero(s.WeeklyLimitUSD),
		MonthlyLimitUSD:    nilToZero(s.MonthlyLimitUSD),
		DailyUsageUSD:      s.DailyUsageUSD,
		WeeklyUsageUSD:     s.WeeklyUsageUSD,
		MonthlyUsageUSD:    s.MonthlyUsageUSD,
		DailyWindowStart:   s.DailyWindowStart,
		WeeklyWindowStart:  s.WeeklyWindowStart,
		MonthlyWindowStart: s.MonthlyWindowStart,
		ExpiresAt:          s.ExpiresAt,
		Status:             s.Status,
	}
}

func nilToZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
