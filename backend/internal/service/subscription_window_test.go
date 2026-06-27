package service

import (
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// TestMain 让 service 包测试的 time.Local = Asia/Shanghai，使三窗口引擎的窗口边界
// （timezone.StartOfDay/Week/Month，依赖 time.Local）与 east-8 日序号口径（EastDayNumber）一致，
// 与生产 timezone.Init("Asia/Shanghai") 对齐。既有 per-day 测试用绝对 east-8（shanghaiLoc）不受影响。
func TestMain(m *testing.M) {
	_ = timezone.Init("Asia/Shanghai")
	os.Exit(m.Run())
}

func fptr(v float64) *float64 { return &v }

// ── ResetWindows：东八区自然 日/周/月 边界惰性重置 ──────────────────────────────
func TestSubWindow_ResetWindows_NaturalBoundaries(t *testing.T) {
	now := time.Now()
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)

	t.Run("daily window from yesterday resets", func(t *testing.T) {
		yest := dayStart.Add(-24 * time.Hour)
		c := &SubWindow{DailyLimitUSD: 10, DailyUsageUSD: 7, DailyWindowStart: &yest}
		changed := c.ResetWindows(now)
		if !changed || c.DailyUsageUSD != 0 || c.DailyWindowStart == nil || !c.DailyWindowStart.Equal(dayStart) {
			t.Fatalf("daily not reset: changed=%v usage=%v start=%v", changed, c.DailyUsageUSD, c.DailyWindowStart)
		}
	})

	t.Run("daily window today does not reset", func(t *testing.T) {
		ds := dayStart
		c := &SubWindow{DailyLimitUSD: 10, DailyUsageUSD: 7, DailyWindowStart: &ds,
			WeeklyWindowStart: &weekStart, MonthlyWindowStart: &monthStart}
		if changed := c.ResetWindows(now); changed || c.DailyUsageUSD != 7 {
			t.Fatalf("daily wrongly reset: changed=%v usage=%v", changed, c.DailyUsageUSD)
		}
	})

	t.Run("nil windows activate without clearing fresh usage", func(t *testing.T) {
		c := &SubWindow{DailyLimitUSD: 10}
		changed := c.ResetWindows(now)
		if !changed || c.DailyWindowStart == nil || !c.DailyWindowStart.Equal(dayStart) {
			t.Fatalf("nil daily not activated: changed=%v start=%v", changed, c.DailyWindowStart)
		}
		if c.WeeklyWindowStart == nil || !c.WeeklyWindowStart.Equal(weekStart) {
			t.Fatalf("nil weekly not activated: %v", c.WeeklyWindowStart)
		}
		if c.MonthlyWindowStart == nil || !c.MonthlyWindowStart.Equal(monthStart) {
			t.Fatalf("nil monthly not activated: %v", c.MonthlyWindowStart)
		}
	})

	t.Run("weekly+monthly from past period reset", func(t *testing.T) {
		ds := dayStart
		oldWeek := weekStart.Add(-7 * 24 * time.Hour)
		oldMonth := monthStart.AddDate(0, -1, 0)
		c := &SubWindow{WeeklyUsageUSD: 5, MonthlyUsageUSD: 9,
			DailyWindowStart: &ds, WeeklyWindowStart: &oldWeek, MonthlyWindowStart: &oldMonth}
		c.ResetWindows(now)
		if c.WeeklyUsageUSD != 0 || !c.WeeklyWindowStart.Equal(weekStart) {
			t.Fatalf("weekly not reset: usage=%v start=%v", c.WeeklyUsageUSD, c.WeeklyWindowStart)
		}
		if c.MonthlyUsageUSD != 0 || !c.MonthlyWindowStart.Equal(monthStart) {
			t.Fatalf("monthly not reset: usage=%v start=%v", c.MonthlyUsageUSD, c.MonthlyWindowStart)
		}
	})
}

// ── SubRemaining：三窗口剩余最小值；不限=+Inf；clamp≥0 ─────────────────────────
func TestSubWindow_SubRemaining(t *testing.T) {
	t.Run("daily is the binding window", func(t *testing.T) {
		c := &SubWindow{DailyLimitUSD: 10, WeeklyLimitUSD: 70, MonthlyLimitUSD: 300,
			DailyUsageUSD: 8, WeeklyUsageUSD: 8, MonthlyUsageUSD: 8}
		if got := c.SubRemaining(); !feq(got, 2) {
			t.Fatalf("want 2, got %v", got)
		}
	})
	t.Run("weekly is the binding window", func(t *testing.T) {
		c := &SubWindow{DailyLimitUSD: 10, WeeklyLimitUSD: 70,
			DailyUsageUSD: 0, WeeklyUsageUSD: 69}
		if got := c.SubRemaining(); !feq(got, 1) {
			t.Fatalf("want 1, got %v", got)
		}
	})
	t.Run("all limits null/0 = unconfigured card -> 0 coverage (safety guard)", func(t *testing.T) {
		c := &SubWindow{}
		if got := c.SubRemaining(); got != 0 {
			t.Fatalf("unconfigured card must contribute 0 coverage, got %v", got)
		}
	})
	t.Run("only weekly configured: daily/monthly unlimited, weekly binds", func(t *testing.T) {
		c := &SubWindow{WeeklyLimitUSD: 70, WeeklyUsageUSD: 65}
		if got := c.SubRemaining(); !feq(got, 5) {
			t.Fatalf("want 5 (weekly binds, others unlimited), got %v", got)
		}
	})
	t.Run("usage over limit clamps to 0", func(t *testing.T) {
		c := &SubWindow{DailyLimitUSD: 10, DailyUsageUSD: 12}
		if got := c.SubRemaining(); got != 0 {
			t.Fatalf("want 0, got %v", got)
		}
	})
}

func activeCard(remaining float64) *SubWindow {
	now := time.Now()
	exp := ExpireDayToExpiresAt(EastDayNumber(now) + 10)
	ds := timezone.StartOfDay(now)
	ws := timezone.StartOfWeek(now)
	ms := timezone.StartOfMonth(now)
	return &SubWindow{
		DailyLimitUSD: remaining, DailyUsageUSD: 0,
		DailyWindowStart: &ds, WeeklyWindowStart: &ws, MonthlyWindowStart: &ms,
		ExpiresAt: exp, Status: SubscriptionStatusActive,
	}
}

// ── AdmitWindow：有卡看三窗口、撞上限/无卡看钱包 ───────────────────────────────
func TestAdmitWindow(t *testing.T) {
	now := time.Now()
	t.Run("active card with remaining", func(t *testing.T) {
		if !AdmitWindow(activeCard(10), &WalletState{Balance: 0}, now) {
			t.Fatal("should admit on subscription remaining")
		}
	})
	t.Run("hit limit but wallet positive", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 10 // remaining 0
		if !AdmitWindow(c, &WalletState{Balance: 5}, now) {
			t.Fatal("should admit by falling back to wallet")
		}
	})
	t.Run("hit limit and wallet empty -> deny", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 10
		if AdmitWindow(c, &WalletState{Balance: 0}, now) {
			t.Fatal("should deny: limit hit + no wallet")
		}
	})
	t.Run("no card + wallet positive", func(t *testing.T) {
		if !AdmitWindow(nil, &WalletState{Balance: 1}, now) {
			t.Fatal("no card but wallet>0 should admit")
		}
	})
	t.Run("no card + wallet empty -> deny", func(t *testing.T) {
		if AdmitWindow(nil, &WalletState{Balance: 0}, now) {
			t.Fatal("no card + no wallet should deny")
		}
	})
	t.Run("expired card falls back to wallet", func(t *testing.T) {
		c := activeCard(10)
		c.ExpiresAt = ExpireDayToExpiresAt(EastDayNumber(now) - 3) // 已过期
		if AdmitWindow(c, &WalletState{Balance: 0}, now) {
			t.Fatal("expired + no wallet should deny")
		}
		if !AdmitWindow(c, &WalletState{Balance: 5}, now) {
			t.Fatal("expired + wallet>0 should admit")
		}
	})
}

// ── SettleWindow：订阅覆盖(1:1) → 钱包正(×倍率) → 钱包负 ───────────────────────
func TestSettleWindow(t *testing.T) {
	now := time.Now()

	t.Run("subscription covers fully, three usages accrue, wallet untouched", func(t *testing.T) {
		c := activeCard(10)
		w := &WalletState{Balance: 100}
		res := SettleWindow(c, w, 4, 2.0, now)
		if !feq(res.SubCover, 4) || res.WalletPay != 0 || res.WalletNegPay != 0 {
			t.Fatalf("unexpected res: %+v", res)
		}
		if !feq(c.DailyUsageUSD, 4) || !feq(c.WeeklyUsageUSD, 4) || !feq(c.MonthlyUsageUSD, 4) {
			t.Fatalf("three usages not accrued: d=%v w=%v m=%v", c.DailyUsageUSD, c.WeeklyUsageUSD, c.MonthlyUsageUSD)
		}
		if !feq(w.Balance, 100) {
			t.Fatalf("wallet should be untouched, got %v", w.Balance)
		}
	})

	t.Run("cost exceeds daily remaining -> cover part, rest hits wallet x multiplier", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 7 // remaining 3
		w := &WalletState{Balance: 100}
		res := SettleWindow(c, w, 5, 2.0, now) // cover 3, leftover 2 official -> wallet 2*2=4
		if !feq(res.SubCover, 3) || !feq(res.WalletPay, 4) || res.WalletNegPay != 0 {
			t.Fatalf("unexpected res: %+v", res)
		}
		if !feq(c.DailyUsageUSD, 10) || !feq(w.Balance, 96) {
			t.Fatalf("state wrong: dailyUsage=%v balance=%v", c.DailyUsageUSD, w.Balance)
		}
	})

	t.Run("subscription + wallet exhausted -> wallet goes negative", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 10 // remaining 0
		w := &WalletState{Balance: 6}
		res := SettleWindow(c, w, 5, 2.0, now) // cover 0; wallet covers 3 official (6/2), leftover 2 -> neg 4
		if res.SubCover != 0 || !feq(res.WalletPay, 6) || !feq(res.WalletNegPay, 4) {
			t.Fatalf("unexpected res: %+v", res)
		}
		if !feq(w.Balance, -4) {
			t.Fatalf("wallet should be -4, got %v", w.Balance)
		}
	})

	t.Run("no card -> pure wallet standard billing x multiplier", func(t *testing.T) {
		w := &WalletState{Balance: 100}
		res := SettleWindow(nil, w, 5, 2.0, now)
		if res.SubCover != 0 || !feq(res.WalletPay, 10) {
			t.Fatalf("unexpected res: %+v", res)
		}
		if !feq(w.Balance, 90) {
			t.Fatalf("wallet should be 90, got %v", w.Balance)
		}
	})

	t.Run("just-expired card flags JustExpired and bills wallet", func(t *testing.T) {
		c := activeCard(10)
		c.ExpiresAt = ExpireDayToExpiresAt(EastDayNumber(now) - 2) // 加载时 active 但已过期
		w := &WalletState{Balance: 100}
		res := SettleWindow(c, w, 3, 1.0, now)
		if !c.JustExpired {
			t.Fatal("should flag JustExpired")
		}
		if res.SubCover != 0 || !feq(res.WalletPay, 3) {
			t.Fatalf("expired should bill wallet only: %+v", res)
		}
	})

	t.Run("zero cost is a no-op", func(t *testing.T) {
		c := activeCard(10)
		w := &WalletState{Balance: 100}
		res := SettleWindow(c, w, 0, 1.0, now)
		if res.SubCover != 0 || res.WalletPay != 0 || res.WalletNegPay != 0 || !feq(w.Balance, 100) {
			t.Fatalf("zero cost should be no-op: %+v balance=%v", res, w.Balance)
		}
	})
}

// ── ManualOverdraftWindow：仅解日上限（daily_usage=0 + expires_at−1天 + 月计数+1）─────
func TestManualOverdraftWindow(t *testing.T) {
	now := time.Now()
	today := EastDayNumber(now)

	t.Run("success refreshes daily window and borrows one day", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 10                              // 日上限已满
		c.WeeklyUsageUSD = 50                             // 周/月用量不应被透支清掉
		c.MonthlyUsageUSD = 200
		c.ExpiresAt = ExpireDayToExpiresAt(today + 5)     // 最后服务日 today+5
		w := &WalletState{MonthlyOverdraftCount: 0, MonthlyOverdraftMonth: CurrentEastMonthKey()}
		if err := ManualOverdraftWindow(c, w, now); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if c.DailyUsageUSD != 0 {
			t.Fatalf("daily usage should reset to 0, got %v", c.DailyUsageUSD)
		}
		if !c.ExpiresAt.Equal(ExpireDayToExpiresAt(today + 4)) {
			t.Fatalf("expires_at should be -1 day: %v", c.ExpiresAt)
		}
		if w.MonthlyOverdraftCount != 1 {
			t.Fatalf("monthly count should be 1, got %v", w.MonthlyOverdraftCount)
		}
		if c.WeeklyUsageUSD != 50 || c.MonthlyUsageUSD != 200 {
			t.Fatalf("weekly/monthly usage must NOT be reset by overdraft: w=%v m=%v", c.WeeklyUsageUSD, c.MonthlyUsageUSD)
		}
	})

	t.Run("daily not exhausted -> rejected", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 5 // 未撞满
		c.ExpiresAt = ExpireDayToExpiresAt(today + 5)
		w := &WalletState{}
		if err := ManualOverdraftWindow(c, w, now); err != ErrOverdraftDailyNotExhausted {
			t.Fatalf("want ErrOverdraftDailyNotExhausted, got %v", err)
		}
	})

	t.Run("monthly limit reached", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 10 // 撞满日额度以触达月度校验
		c.ExpiresAt = ExpireDayToExpiresAt(today + 5)
		w := &WalletState{MonthlyOverdraftCount: MaxMonthlyOverdraftUses, MonthlyOverdraftMonth: CurrentEastMonthKey()}
		if err := ManualOverdraftWindow(c, w, now); err != ErrOverdraftMonthlyLimit {
			t.Fatalf("want ErrOverdraftMonthlyLimit, got %v", err)
		}
	})

	t.Run("no future day to borrow", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 10                      // 撞满日额度以触达"未来天"校验
		c.ExpiresAt = ExpireDayToExpiresAt(today) // 最后服务日=今天，无未来天
		w := &WalletState{}
		if err := ManualOverdraftWindow(c, w, now); err != ErrOverdraftNoFutureDay {
			t.Fatalf("want ErrOverdraftNoFutureDay, got %v", err)
		}
	})

	t.Run("not active", func(t *testing.T) {
		c := activeCard(10)
		c.DailyUsageUSD = 10
		c.ExpiresAt = ExpireDayToExpiresAt(today - 3) // 已过期
		w := &WalletState{}
		if err := ManualOverdraftWindow(c, w, now); err != ErrOverdraftNoActiveCard {
			t.Fatalf("want ErrOverdraftNoActiveCard, got %v", err)
		}
	})
}

// ── RefundableDaysByExpiry：max(0, 最后服务日 − 今天) ──────────────────────────
func TestRefundableDaysByExpiry(t *testing.T) {
	now := time.Now()
	today := EastDayNumber(now)
	if got := RefundableDaysByExpiry(ExpireDayToExpiresAt(today), now); got != 0 {
		t.Fatalf("expires today -> 0, got %v", got)
	}
	if got := RefundableDaysByExpiry(ExpireDayToExpiresAt(today+29), now); got != 29 {
		t.Fatalf("29 future days, got %v", got)
	}
	if got := RefundableDaysByExpiry(ExpireDayToExpiresAt(today-5), now); got != 0 {
		t.Fatalf("already expired -> 0, got %v", got)
	}
}
