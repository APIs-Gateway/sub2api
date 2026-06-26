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

// ── 场景 1：套餐余额够付 → 1:1 扣套餐余额，不乘倍率 ──────────────────────────
func TestSettle_PackageOnly(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 10, TodayDay: 100, ExpireDay: 130}
	w := &WalletState{Balance: 100, MonthlyOverdraftMonth: "202606"}
	r := Settle(c, w, 4, 2.0, 100, "202606") // 倍率2，但套餐扣费不乘
	if !feq(r.SubPay, 4) || !feq(c.TodayRemaining, 6) || !feq(w.Balance, 100) {
		t.Fatalf("应只扣套餐 4: r=%+v card=%+v wallet=%+v", r, c, w)
	}
	if r.WalletPay != 0 || r.WalletNegPay != 0 || r.OverdraftDays != 0 {
		t.Fatalf("不应动钱包/透支: %+v", r)
	}
}

// ── 场景 2 / 7：套餐尽 + 钱包>0 → 钱包正余额按 ×倍率 计费 ──────────────────────
func TestSettle_PackageThenWallet(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 2, TodayDay: 100, ExpireDay: 130}
	w := &WalletState{Balance: 100, MonthlyOverdraftMonth: "202606"}
	// C=5：套餐扣 2（1:1），剩 3 官方刀走钱包 ×2 = 6
	r := Settle(c, w, 5, 2.0, 100, "202606")
	if !feq(r.SubPay, 2) || !feq(c.TodayRemaining, 0) {
		t.Fatalf("套餐应扣 2 到 0: r=%+v card=%+v", r, c)
	}
	if !feq(r.WalletPay, 6) || !feq(w.Balance, 94) {
		t.Fatalf("钱包应扣 3×2=6: r=%+v wallet=%+v", r, w)
	}
	if r.OverdraftDays != 0 || r.WalletNegPay != 0 {
		t.Fatalf("不应透支/记负: %+v", r)
	}
}

// ── 场景 3：套餐+钱包正都尽 + 透支开 → 透支借未来天（expire−1 + 补 D）按 1:1 ─────
func TestSettle_Overdraft(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 100, StartDay: 98, ExpireDay: 130, OverdraftOn: true}
	w := &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}
	// C=25：透支 3 次（10+10+10>=25→实际借 3 天，use=10,10,5），expire 130→127，count 3
	r := Settle(c, w, 25, 1.0, 100, "202606")
	if r.OverdraftDays != 3 || c.ExpireDay != 127 || w.MonthlyOverdraftCount != 3 {
		t.Fatalf("应透支 3 天 expire→127 count→3: r=%+v card=%+v wallet=%+v", r, c, w)
	}
	if !feq(r.OverdraftPay, 25) || !feq(c.TodayRemaining, 5) { // 第3天借10用5，剩5留当日
		t.Fatalf("透支扣 25、当日余 5: r=%+v card=%+v", r, c)
	}
	if !feq(w.Balance, 0) || r.WalletNegPay != 0 {
		t.Fatalf("不应记钱包负: wallet=%+v r=%+v", w, r)
	}
}

// 透支「借来未用完」：C=5, D=10 → 借 1 天，use=5，today_remaining += 5
func TestSettle_OverdraftLeftover(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 100, ExpireDay: 130, OverdraftOn: true}
	w := &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}
	r := Settle(c, w, 5, 1.0, 100, "202606")
	if r.OverdraftDays != 1 || !feq(c.TodayRemaining, 5) || c.ExpireDay != 129 {
		t.Fatalf("借 1 天用 5 留 5: r=%+v card=%+v", r, c)
	}
}

// ── 场景 4：准入——套餐=0 且 钱包≤0 且 不可透支 → 拒绝（唯一拒绝场景）─────────────
func TestAdmit_RejectOnlyWhenAllExhausted(t *testing.T) {
	today := 100
	// 全不可用 → 拒绝
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: today, ExpireDay: today, OverdraftOn: true} // expire==today 无未来天
	w := &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}
	if Admit(c, w, today, "202606") {
		t.Fatal("三来源全不可用应拒绝")
	}
	// 套餐有余额 → 放行
	c2 := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 1, TodayDay: today, ExpireDay: today}
	if !Admit(c2, &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}, today, "202606") {
		t.Fatal("套餐>0 应放行")
	}
	// 钱包>0 → 放行
	c3 := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: today, ExpireDay: today}
	if !Admit(c3, &WalletState{Balance: 0.01, MonthlyOverdraftMonth: "202606"}, today, "202606") {
		t.Fatal("钱包>0 应放行")
	}
	// 可透支（有未来天）→ 放行
	c4 := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: today, ExpireDay: today + 5, OverdraftOn: true}
	if !Admit(c4, &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}, today, "202606") {
		t.Fatal("可透支应放行")
	}
}

// 跨月：上月透支已满 + 套餐/钱包皆空，仅透支可用时，Admit 必须先惰性重置月度计数再判，否则误拒。
func TestAdmit_CrossMonthOverdraftReset(t *testing.T) {
	today := 100
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: today, ExpireDay: today + 10, OverdraftOn: true}
	w := &WalletState{Balance: 0, MonthlyOverdraftCount: 5, MonthlyOverdraftMonth: "202606"}
	// 同月已满 → 拒绝
	if Admit(c, w, today, "202606") {
		t.Fatal("本月透支已满且无其他来源应拒绝")
	}
	// 次月第一笔：Admit 须惰性重置月度计数 → 仅透支可用也应放行
	if !Admit(c, w, today, "202607") {
		t.Fatal("跨月后仅透支可用应放行")
	}
	if w.MonthlyOverdraftCount != 0 || w.MonthlyOverdraftMonth != "202607" {
		t.Fatalf("Admit 应已惰性重置月度计数: %+v", w)
	}
}

// ── 场景 5 / 16：流式超付——所有来源用尽 → 缺口记钱包负数，套餐余额永不为负 ──────────
func TestSettle_StreamingOverpayWalletNegative(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 2, TodayDay: 100, ExpireDay: 130, OverdraftOn: false}
	w := &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}
	// 透支关：套餐扣 2，剩 8 官方刀全记钱包负 ×1.5 = 12
	r := Settle(c, w, 10, 1.5, 100, "202606")
	if !feq(c.TodayRemaining, 0) {
		t.Fatalf("套餐永不为负: card=%+v", c)
	}
	if !feq(r.WalletNegPay, 12) || !feq(w.Balance, -12) {
		t.Fatalf("缺口 8×1.5=12 记钱包负: r=%+v wallet=%+v", r, w)
	}
}

// ── 场景 15：钱包为负 + 次日套餐发 D → 套餐照常可用、钱包这步因 ≤0 跳过 ──────────────
func TestSettle_NegativeWalletNextDay(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 100, ExpireDay: 130}
	w := &WalletState{Balance: -20, MonthlyOverdraftMonth: "202606"} // 钱包欠费
	// 次日 101：惰性覆盖发 D=10；C=4 全走套餐，钱包负数不变、不锁卡
	r := Settle(c, w, 4, 2.0, 101, "202606")
	if !feq(r.SubPay, 4) || !feq(c.TodayRemaining, 6) {
		t.Fatalf("次日套餐照常发并扣: r=%+v card=%+v", r, c)
	}
	if !feq(w.Balance, -20) || r.WalletPay != 0 {
		t.Fatalf("钱包负数不动、不参与: wallet=%+v r=%+v", w, r)
	}
}

// ── 透支月度上限：每用户每自然月最多 5 次，超出走钱包负数 ───────────────────────────
func TestSettle_MonthlyOverdraftCap(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 100, ExpireDay: 200, OverdraftOn: true}
	w := &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}
	// C=70：本月最多透支 5 次=50 官方刀，剩 20 记钱包负 ×1 = 20
	r := Settle(c, w, 70, 1.0, 100, "202606")
	if r.OverdraftDays != 5 || w.MonthlyOverdraftCount != MaxMonthlyOverdraftUses {
		t.Fatalf("应透支封顶 5: r=%+v wallet=%+v", r, w)
	}
	if !feq(r.OverdraftPay, 50) || !feq(r.WalletNegPay, 20) || !feq(w.Balance, -20) {
		t.Fatalf("50 透支 + 20 钱包负: r=%+v wallet=%+v", r, w)
	}
	// 同月再来：不能再透支，直接钱包负
	c.ExpireDay = 195
	r2 := Settle(c, w, 10, 1.0, 100, "202606")
	if r2.OverdraftDays != 0 || !feq(r2.WalletNegPay, 10) {
		t.Fatalf("本月已满不得再透支: r2=%+v", r2)
	}
	// 次月惰性重置 → 可再透支
	r3 := Settle(c, w, 10, 1.0, 100, "202607")
	if w.MonthlyOverdraftMonth != "202607" || r3.OverdraftDays != 1 {
		t.Fatalf("次月应重置可再透支: wallet=%+v r3=%+v", w, r3)
	}
}

// 透支边界：只有 expire_day > today 才能借；借到 expire_day==today 即停。
func TestSettle_OverdraftStopsAtNoFutureDay(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 100, ExpireDay: 102, OverdraftOn: true}
	w := &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}
	// expire=today+2 → 最多借 2 天（expire 102→101→100 停），C=100 → 借 2 天=20，剩 80 钱包负
	r := Settle(c, w, 100, 1.0, 100, "202606")
	if r.OverdraftDays != 2 || c.ExpireDay != 100 {
		t.Fatalf("最多借到 expire==today: r=%+v card=%+v", r, c)
	}
	if !feq(r.WalletNegPay, 80) {
		t.Fatalf("其余记钱包负 80: r=%+v", r)
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
}

func TestSettle_TracksTodaySpentAcrossOverdraft(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 100, ExpireDay: 105, OverdraftOn: true}
	w := &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}

	r := Settle(c, w, 12, 1.0, 100, "202606")

	if r.OverdraftDays != 2 || !feq(r.OverdraftPay, 12) {
		t.Fatalf("应透支两天并扣 12 官方刀: r=%+v", r)
	}
	if !feq(c.TodayRemaining, 8) {
		t.Fatalf("第二个借天未用完应留 8，got %v", c.TodayRemaining)
	}
	if !feq(c.TodaySpentFromPackage(100), 12) {
		t.Fatalf("今日套餐侧实际已用应为 12，不能由 D-remaining 误算成 2: card=%+v", c)
	}
}

// ── 场景 12：退款/转套餐剩余天数含透支借天（RefundableDays）─────────────────────
func TestRefundableDays_ContainsOverdraftBorrow(t *testing.T) {
	// 买 T=30、start=100、expire=129；今天 109（已用 10 天，含今天）
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 109, StartDay: 100, ExpireDay: 129, OverdraftOn: true}
	if c.RefundableDays(109) != 20 { // 没透支：129-109=20
		t.Fatalf("未透支剩余应 20")
	}
	// 今天透支 5 次 → expire 129→124
	w := &WalletState{Balance: 0, MonthlyOverdraftMonth: "202606"}
	Settle(c, w, 50, 1.0, 109, "202606")
	if c.ExpireDay != 124 || c.RefundableDays(109) != 15 { // 124-109=15，旧公式会错退到 20
		t.Fatalf("透支后剩余应 15: expire=%d refundable=%d", c.ExpireDay, c.RefundableDays(109))
	}
}

// multiplier ≤ 0 兜底为 1。
func TestSettle_MultiplierFallback(t *testing.T) {
	c := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 0, TodayDay: 100, ExpireDay: 130}
	w := &WalletState{Balance: 5, MonthlyOverdraftMonth: "202606"}
	r := Settle(c, w, 3, 0, 100, "202606") // 倍率0 → 视为1
	if !feq(r.WalletPay, 3) || !feq(w.Balance, 2) {
		t.Fatalf("倍率应兜底为 1: r=%+v wallet=%+v", r, w)
	}
}
