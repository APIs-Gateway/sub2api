package service

import (
	"math"
	"testing"
	"time"
)

// TestBurndownHelpers 校验 burn-down 模型的核心计算：消费进度天、日历天、清扣下限与差额。
func TestBurndownHelpers(t *testing.T) {
	loc := shanghaiLoc
	activated := time.Date(2026, 6, 1, 10, 0, 0, 0, loc) // $10/天 × 30 天 = $300

	newSub := func(consumed, clawed float64) *UserSubscription {
		a := activated
		return &UserSubscription{
			StartsAt:        activated,
			ActivatedAt:     &a,
			GrantedTotalUSD: 300,
			DailyAmountUSD:  10,
			ConsumedUSD:     consumed,
			ClawedUSD:       clawed,
		}
	}

	approx := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

	sub := newSub(0, 0)
	if got := sub.TotalDays(); got != 30 {
		t.Fatalf("TotalDays=%d want 30", got)
	}
	if got := sub.CalendarDayAt(activated); got != 0 {
		t.Fatalf("CalendarDayAt(day0)=%d want 0", got)
	}
	if got := sub.RemainingUSD(); !approx(got, 300) {
		t.Fatalf("Remaining=%v want 300", got)
	}
	// 第 0 天：floor=300，remaining=300 → 不清扣
	if got := sub.ClawbackShortfallAt(activated); !approx(got, 0) {
		t.Fatalf("shortfall(day0)=%v want 0", got)
	}

	day2 := activated.AddDate(0, 0, 2)
	if got := sub.CalendarDayAt(day2); got != 2 {
		t.Fatalf("CalendarDayAt(day2)=%d want 2", got)
	}
	// 第 2 天且未消费：floor=300-2*10=280，remaining=300 → 清扣 20（用不完作废）
	if got := sub.ClawbackShortfallAt(day2); !approx(got, 20) {
		t.Fatalf("shortfall(day2, consumed=0)=%v want 20", got)
	}

	// 透支：已消费 $50（用到第 5 天），第 2 天 → 消费进度领先日历，不清扣
	ahead := newSub(50, 0)
	if got := ahead.ConsumptionDay(); !approx(got, 5) {
		t.Fatalf("ConsumptionDay=%v want 5", got)
	}
	if got := ahead.ClawbackShortfallAt(day2); !approx(got, 0) {
		t.Fatalf("shortfall(day2, consumed=50)=%v want 0 (ahead of calendar)", got)
	}

	// 全部花光后 remaining 不为负
	spent := newSub(300, 0)
	if got := spent.RemainingUSD(); !approx(got, 0) {
		t.Fatalf("Remaining(all spent)=%v want 0", got)
	}
	if got := spent.ClawbackShortfallAt(day2); !approx(got, 0) {
		t.Fatalf("shortfall(all spent)=%v want 0", got)
	}
}

// TestDayCardNoClawback 天卡（TotalDays<=1）整张卡只一天、到期由作废流程处理，
// 日内绝不 clawback。防回归：深夜买的天卡曾按「日历午夜」计天，在次日 0 点被误扣光（user 762）。
func TestDayCardNoClawback(t *testing.T) {
	loc := shanghaiLoc
	activated := time.Date(2026, 6, 21, 23, 55, 0, 0, loc) // 23:55 激活，G=D=30 → TotalDays=1
	a := activated
	sub := &UserSubscription{
		StartsAt:        activated,
		ActivatedAt:     &a,
		GrantedTotalUSD: 30,
		DailyAmountUSD:  30,
		ConsumedUSD:     0,
	}
	if got := sub.TotalDays(); got != 1 {
		t.Fatalf("TotalDays=%d want 1", got)
	}
	// 5 分钟后跨东八区午夜（CalendarDayAt=1）：旧逻辑会误扣 30，新逻辑必须为 0。
	pastMidnight := time.Date(2026, 6, 22, 0, 0, 30, 0, loc)
	if got := sub.ClawbackShortfallAt(pastMidnight); got != 0 {
		t.Fatalf("day-card shortfall past midnight=%v want 0", got)
	}
	// 满 24h 后仍为 0（剩余由到期作废处理，而非日内 clawback）。
	if got := sub.ClawbackShortfallAt(activated.AddDate(0, 0, 1)); got != 0 {
		t.Fatalf("day-card shortfall next day=%v want 0", got)
	}
}
