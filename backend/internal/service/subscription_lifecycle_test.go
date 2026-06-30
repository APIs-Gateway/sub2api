package service

import (
	"math"
	"testing"
)

func lcApprox(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// 规格第 5 节续费：expire_day = max(cur, today−1) + addDays。
func TestRenewExpireDay(t *testing.T) {
	const today = 1000
	cases := []struct {
		name             string
		curExpireDay     int
		addDays          int
		wantNewExpireDay int
	}{
		// 未到期（cur ≥ today）→ 从原到期日顺延，无缝衔接。
		{"未到期顺延", 1040, 30, 1070},
		// 当天到期（cur == today）→ 仍按原到期日顺延。
		{"当天到期顺延", 1000, 30, 1030},
		// 已到期（cur < today−1）→ 从今天起算 addDays 天：today + addDays − 1。
		{"已到期从今天起算", 980, 30, 1029},
		// 昨天刚过期（cur == today−1）边界 → max(999,999)+30 = 1029，与从今天起算一致。
		{"昨天过期边界", 999, 30, 1029},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RenewExpireDay(c.curExpireDay, today, c.addDays); got != c.wantNewExpireDay {
				t.Fatalf("RenewExpireDay(%d,%d,%d)=%d want %d", c.curExpireDay, today, c.addDays, got, c.wantNewExpireDay)
			}
		})
	}
}

// 续费写出的 expire_day 不得超过 MaxExpireDay()（与建卡/延长口径一致）。
func TestRenewExpireDay_ClampsToMax(t *testing.T) {
	maxd := MaxExpireDay()
	got := RenewExpireDay(maxd, maxd, 90)
	if got != maxd {
		t.Fatalf("续到上限应夹住：got %d want %d", got, maxd)
	}
}

// 规格第 6 节退款示例表（P=300, T=30, 均当天即退）。refundable 由卡的 expire_day−today 推出。
func TestRefundAmount_SpecTable(t *testing.T) {
	const (
		price     = 300.0
		origT     = 30
		startDay  = 1000
		buyExpire = startDay + origT - 1 // 1029：买入日 day1，最后服务日 = start+T−1
	)
	cases := []struct {
		name        string
		today       int
		overdraftBk int // 今天透支借走的天数（每次透支 expire_day−1）
		wantRefund  float64
	}{
		{"刚买就退、没透支", startDay, 0, 290},        // refundable 29
		{"用10天、没透支", startDay + 9, 0, 200},    // refundable 20
		{"用10天+今天透支5次", startDay + 9, 5, 150}, // refundable 15（已含透支借走）
		{"透支把天数借光", startDay + 9, 20, 0},      // refundable 0
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			card := &PerDayCard{ExpireDay: buyExpire - c.overdraftBk}
			refundable := card.RefundableDays(c.today)
			got := RefundAmount(price, refundable, origT)
			if !lcApprox(got, c.wantRefund) {
				t.Fatalf("退款额=%v want %v（refundable=%d）", got, c.wantRefund, refundable)
			}
		})
	}
}

// 退款额非法/边界输入：T≤0 / refundable≤0 / price≤0 → 0。
func TestRefundAmount_Guards(t *testing.T) {
	if got := RefundAmount(300, 10, 0); got != 0 {
		t.Fatalf("T=0 应退 0，got %v", got)
	}
	if got := RefundAmount(300, 0, 30); got != 0 {
		t.Fatalf("refundable=0 应退 0，got %v", got)
	}
	if got := RefundAmount(-5, 10, 30); got != 0 {
		t.Fatalf("price<0 应退 0，got %v", got)
	}
}

// 单笔订单口径(P1#10/#4):refundableDays > originalT(续费叠加后整卡剩余天超过本单 T)时
// 夹到 originalT,退款额/折价 ≤ 本单 P,绝不超退/超抵。
func TestRefundAmount_ClampToOriginalT(t *testing.T) {
	// 续费叠加:整卡剩 55 天,但本单 T=30 → 夹到 30 → 退满本单 300(而非 300×55/30=550)。
	if got := RefundAmount(300, 55, 30); !lcApprox(got, 300) {
		t.Fatalf("refundable>T 应夹到 T、退满 P=300，got %v", got)
	}
	// refundable == T → 退满 P。
	if got := RefundAmount(300, 30, 30); !lcApprox(got, 300) {
		t.Fatalf("refundable==T 应退满 300，got %v", got)
	}
	// refundable < T → 不受夹影响,正常按比例。
	if got := RefundAmount(300, 20, 30); !lcApprox(got, 200) {
		t.Fatalf("refundable<T 应按比例 200，got %v", got)
	}
	// 同口径保护转套餐剩余价值 V:旧卡续费叠加剩 40 天但 T_旧=30 → V 夹到 P_旧=300。
	if got := ChangePlanRemainingValue(300, 40, 30); !lcApprox(got, 300) {
		t.Fatalf("V 应夹到 P_旧=300，got %v", got)
	}
}

// 规格第 7 节转套餐：剩余价值 V 与退款同口径；新卡当天余额 = max(0, D_新 − 旧卡今日已用)。
func TestChangePlan_Calculators(t *testing.T) {
	// V：旧卡 P=300/T=30，剩 20 天 → 200；借光（剩 0 天）→ 0。
	if got := ChangePlanRemainingValue(300, 20, 30); !lcApprox(got, 200) {
		t.Fatalf("V=%v want 200", got)
	}
	if got := ChangePlanRemainingValue(300, 0, 30); got != 0 {
		t.Fatalf("借光 V 应为 0，got %v", got)
	}

	// 新卡当天余额防套利：旧卡今天已领满 D_旧=10 → 转 D_新=10 当天为 0（不再多领一份）。
	if got := ChangePlanNewCardTodayBalance(10, 10); got != 0 {
		t.Fatalf("D_新=10 旧用 10 → 当天应 0，got %v", got)
	}
	// 升档：D_新=20 旧用 10 → 当天 10；次日起按 D_新=20 发放（次日逻辑在结算侧）。
	if got := ChangePlanNewCardTodayBalance(20, 10); !lcApprox(got, 10) {
		t.Fatalf("D_新=20 旧用 10 → 当天应 10，got %v", got)
	}
	// 降档：D_新=5 旧用 10 → 当天 0（不为负）。
	if got := ChangePlanNewCardTodayBalance(5, 10); got != 0 {
		t.Fatalf("D_新<旧用 → 当天应 0，got %v", got)
	}
}

// QuoteChangePlan 组合测算（规格第 7 节）：多退少补 + 新卡发放参数。
func TestQuoteChangePlan(t *testing.T) {
	cfg := DefaultSubscriptionPricingConfig()
	const today = 1000
	// 旧卡：P=300、T=30、剩 20 天、今天已用 4（D_旧=10、today_remaining=6）。
	q := QuoteChangePlan(cfg, 300, 20, 30 /*dNew*/, 12 /*tNew*/, 30 /*oldTodaySpent*/, 4, today)

	// V 与退款同口径：300×20/30 = 200。
	if !lcApprox(q.OldRemainingValue, 200) {
		t.Fatalf("V=%v want 200", q.OldRemainingValue)
	}
	// P_新 = cfg.Price(12,30)；Diff = P_新 − V。
	wantPNew := cfg.Price(12, 30)
	if !lcApprox(q.NewPlanPrice, wantPNew) {
		t.Fatalf("P_新=%v want %v", q.NewPlanPrice, wantPNew)
	}
	if !lcApprox(q.Diff, wantPNew-200) {
		t.Fatalf("Diff=%v want %v", q.Diff, wantPNew-200)
	}
	// 新卡当天余额 = max(0, 12−4) = 8（防套利：今天已领的 4 不再重复发）。
	if !lcApprox(q.NewCardTodayBalance, 8) {
		t.Fatalf("新卡当天余额=%v want 8", q.NewCardTodayBalance)
	}
	// 新卡 expire_day = today + 30 − 1 = 1029。
	if q.NewCardExpireDay != today+29 {
		t.Fatalf("新卡 expire_day=%d want %d", q.NewCardExpireDay, today+29)
	}
}

// 旧卡被透支借光天数（remaining=0）→ V=0，转套餐 = 全额买新套餐（Diff = P_新）。
func TestQuoteChangePlan_OldDepleted(t *testing.T) {
	cfg := DefaultSubscriptionPricingConfig()
	const today = 1000
	q := QuoteChangePlan(cfg, 300 /*oldRefundableDays*/, 0, 30, 10, 30, 0, today)
	if q.OldRemainingValue != 0 {
		t.Fatalf("借光 V 应为 0，got %v", q.OldRemainingValue)
	}
	if !lcApprox(q.Diff, cfg.Price(10, 30)) {
		t.Fatalf("Diff 应= 全额 P_新=%v，got %v", cfg.Price(10, 30), q.Diff)
	}
}

// TodaySpentFromPackage 作为「旧卡今日已用」喂给转套餐新卡当天余额：闭环校验。
func TestChangePlan_TodaySpentFeedsNewCard(t *testing.T) {
	const today = 1000
	// 旧卡 D=10、今天已花 6（today_remaining=4）。
	old := &PerDayCard{DailyAmountUSD: 10, TodayRemaining: 4, TodayDay: today}
	spent := old.TodaySpentFromPackage(today) // 期望 6
	if !lcApprox(spent, 6) {
		t.Fatalf("旧卡今日已用=%v want 6", spent)
	}
	// 转 D_新=10 → 当天 max(0,10−6)=4。
	if got := ChangePlanNewCardTodayBalance(10, spent); !lcApprox(got, 4) {
		t.Fatalf("新卡当天余额=%v want 4", got)
	}
}
