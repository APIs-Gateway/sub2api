package service

import (
	"testing"
	"time"
)

const eps = 1e-9

func feq(a, b float64) bool { return a-b < eps && b-a < eps }

// ── 自然日序号（规格第 4 节天数口径；场景 17：23:58 买卡 / 次日 00:00）──────────────
func TestEastDayNumber_NaturalDayBoundary(t *testing.T) {
	loc := shanghaiLoc
	d1 := time.Date(2026, 6, 24, 23, 58, 0, 0, loc) // 当晚
	d2 := time.Date(2026, 6, 25, 0, 0, 0, 0, loc)   // 次日 0 点
	n1, n2 := EastDayNumber(d1), EastDayNumber(d2)
	if n2 != n1+1 {
		t.Fatalf("跨午夜应 +1 天: n1=%d n2=%d", n1, n2)
	}
	// 同一自然日内不同时刻同序号
	if EastDayNumber(time.Date(2026, 6, 24, 0, 1, 0, 0, loc)) != n1 {
		t.Fatalf("同自然日序号应相等")
	}
}

// ExpireDayToExpiresAt 必须与自然日口径一致：
// today ≤ expire_day（active）⟺ now < expires_at；today > expire_day（expired）⟺ now ≥ expires_at。
func TestExpireDayToExpiresAt_Invariant(t *testing.T) {
	for _, d := range []int{0, 1, 20000, 21000} {
		exp := ExpireDayToExpiresAt(d)
		// expires_at 当刻即「次日 0 点」= 自然日 d+1 的起点。
		if got := EastDayNumber(exp); got != d+1 {
			t.Fatalf("ExpireDayToExpiresAt(%d) 落在日 %d，应为 %d", d, got, d+1)
		}
		// 到期日 d 当天最后一刻仍属 active：now=expires_at−1ns → 自然日 = d（≤ expire_day）。
		if got := EastDayNumber(exp.Add(-time.Nanosecond)); got != d {
			t.Fatalf("到期日当天末刻自然日=%d，应为 %d（仍 active）", got, d)
		}
	}
}

// ClampExpireDay 必须把超过上限的 expire_day 夹到 MaxExpireDay，且派生的 expires_at 不超过 MaxExpiresAt
// （覆盖「近上限卡再延长」入口，防超长有效期复现）。
func TestClampExpireDay(t *testing.T) {
	max := MaxExpireDay()
	if got := ClampExpireDay(max - 100); got != max-100 {
		t.Fatalf("正常值应不变: ClampExpireDay(max-100)=%d want %d", got, max-100)
	}
	if got := ClampExpireDay(max); got != max {
		t.Fatalf("恰好上限应不变: got %d want %d", got, max)
	}
	if got := ClampExpireDay(max + 5000); got != max {
		t.Fatalf("超上限应夹到 max: got %d want %d", got, max)
	}
	// clamp 后派生的 expires_at 不得超过 MaxExpiresAt。
	if ExpireDayToExpiresAt(ClampExpireDay(max + 5000)).After(MaxExpiresAt) {
		t.Fatalf("clamp 后 expires_at 仍超过 MaxExpiresAt")
	}
}

func TestEastMonthKey(t *testing.T) {
	loc := shanghaiLoc
	if k := EastMonthKey(time.Date(2026, 6, 30, 23, 59, 0, 0, loc)); k != "202606" {
		t.Fatalf("月份键=%s want 202606", k)
	}
	if k := EastMonthKey(time.Date(2026, 7, 1, 0, 0, 0, 0, loc)); k != "202607" {
		t.Fatalf("月份键=%s want 202607", k)
	}
}

// ── 惰性覆盖（场景 6 / 9 / 16）────────────────────────────────────────────────
func TestResetIfNewDay(t *testing.T) {
	// 场景 6：跨午夜且 today ≤ expire_day → 套餐余额重置为 D
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 3, TodayDay: 100, StartDay: 100, ExpireDay: 130}
	if !c.ResetIfNewDay(101) {
		t.Fatal("跨天应变更")
	}
	if !feq(c.TodayRemaining, 10) || c.TodayDay != 101 || c.Expired {
		t.Fatalf("跨天有效期应重置为 D: %+v", c)
	}
	// 同日再调用：不动
	if c.ResetIfNewDay(101) {
		t.Fatal("同日不应变更")
	}
	// 场景 9：today > expire_day → 置 0 且标 expired，绝不再发额度
	c2 := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 7, TodayDay: 130, StartDay: 100, ExpireDay: 130}
	c2.ResetIfNewDay(131)
	if !feq(c2.TodayRemaining, 0) || !c2.Expired {
		t.Fatalf("过期应置 0 并标 expired: %+v", c2)
	}
	// 过期后再跨天仍不发额度
	c2.ResetIfNewDay(132)
	if !feq(c2.TodayRemaining, 0) {
		t.Fatalf("过期卡不得再发额度: %+v", c2)
	}
}

// ── 场景 11：转套餐当天已用（TodaySpentFromPackage）────────────────────────────
func TestTodaySpentFromPackage(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 4, TodayDay: 100, ExpireDay: 130}
	if !feq(c.TodaySpentFromPackage(100), 6) { // 今天已用 10-4=6
		t.Fatalf("今日已用应为 6")
	}
	if !feq(c.TodaySpentFromPackage(101), 0) { // 非当日 → 0
		t.Fatalf("非当日应为 0")
	}
	// DailySpentUSD 有效（DailySpentDay 对齐）时优先用它：跨透支借天的真实已用不被 D−remaining 误算。
	c2 := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 8, TodayDay: 100, DailySpentUSD: 12, DailySpentDay: 100, ExpireDay: 130}
	if !feq(c2.TodaySpentFromPackage(100), 12) {
		t.Fatalf("应优先读 DailySpentUSD=12，不被 D-remaining 误算: %+v", c2)
	}
}

// ── 场景 12：退款/转套餐剩余天数含透支借天（RefundableDays = max(0, expire_day − today)）──
func TestRefundableDays_ContainsOverdraftBorrow(t *testing.T) {
	// 买 T=30、start=100、expire=129；今天 109（已用 10 天，含今天）
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 109, StartDay: 100, ExpireDay: 129}
	if c.RefundableDays(109) != 20 { // 没透支：129-109=20
		t.Fatalf("未透支剩余应 20")
	}
	// 透支借天会令 expire_day 提前（每借 1 天 expire−1）：借 5 天后 129→124。
	c.ExpireDay = 124
	if c.RefundableDays(109) != 15 { // 124-109=15，旧公式会错退到 20
		t.Fatalf("透支后剩余应 15: expire=%d refundable=%d", c.ExpireDay, c.RefundableDays(109))
	}
	// expire_day 落到 today 之前 → 夹到 0，不出现负退款。
	c.ExpireDay = 105
	if c.RefundableDays(109) != 0 {
		t.Fatalf("expire<today 应夹到 0: got %d", c.RefundableDays(109))
	}
}
