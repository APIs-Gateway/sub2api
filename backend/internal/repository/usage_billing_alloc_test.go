//go:build unit

package repository

import (
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// east8 = 东八区(无 DST，等价 Asia/Shanghai 偏移)，用于构造与 service 计天口径一致的时间。
var east8 = time.FixedZone("UTC+8", 8*3600)

// allocDay0 固定一个东八区日期；卡激活与 now 同日 → CalendarDayAt=0、DailySpentDay=0 时 dailySpent 生效。
func allocDay0() time.Time { return time.Date(2026, 6, 1, 10, 0, 0, 0, east8) }

// allocCard 构造一张 burn-down 卡。dailySpent 表示「当日(day0)已消费额度」。
func allocCard(g, d, consumed, dailySpent float64, maxOD *int, toc int) *service.UserSubscription {
	a := allocDay0()
	return &service.UserSubscription{
		StartsAt:            a,
		ActivatedAt:         &a,
		GrantedTotalUSD:     g,
		DailyAmountUSD:      d,
		ConsumedUSD:         consumed,
		ClawedUSD:           0,
		DailySpentUSD:       dailySpent,
		DailySpentDay:       0,
		MaxOverdraftDays:    maxOD,
		TotalOverdraftCount: toc,
	}
}

func allocApprox(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func assertAlloc(t *testing.T, alloc []float64, wantAlloc []float64, remaining, wantRemaining float64) {
	t.Helper()
	if len(alloc) != len(wantAlloc) {
		t.Fatalf("alloc len=%d want %d", len(alloc), len(wantAlloc))
	}
	for i := range alloc {
		if !allocApprox(alloc[i], wantAlloc[i]) {
			t.Fatalf("alloc[%d]=%v want %v (full=%v)", i, alloc[i], wantAlloc[i], alloc)
		}
	}
	if !allocApprox(remaining, wantRemaining) {
		t.Fatalf("remaining=%v want %v", remaining, wantRemaining)
	}
}

// TestPlanSubscriptionDailyAllocation 校验多卡「叠加」分摊:一张用满当日额度再用下一张、
// 所有卡正常额度用尽才透支(回归 wzreee:月卡被先烧进透支、日卡闲置)。
func TestPlanSubscriptionDailyAllocation(t *testing.T) {
	now := allocDay0()
	five := 5

	t.Run("stack_daily_then_monthly_no_overdraft", func(t *testing.T) {
		// 顺序=按到期先后:日卡(D30)在前、月卡(D90,可透支)在后。花 100。
		// 期望:先用满日卡 30,再用月卡 70,均在各自当日额度内 → 不透支。
		daily := allocCard(30, 30, 0, 0, nil, 0)
		monthly := allocCard(2700, 90, 1164, 0, &five, 0)
		alloc, rem := planSubscriptionDailyAllocation([]*service.UserSubscription{daily, monthly}, now, 100)
		assertAlloc(t, alloc, []float64{30, 70}, rem, 0)
	})

	t.Run("no_premature_overdraft_uses_other_card_first", func(t *testing.T) {
		// 关键回归:旧单轮逻辑会把月卡(在前时)烧到 (1+5)*90 才轮到日卡 → 月卡透支、日卡不动。
		// 新逻辑无论顺序,都先用满两卡正常额度(30+90=120)才透支。花 110 → 全在正常额度内。
		monthly := allocCard(2700, 90, 1164, 0, &five, 0)
		daily := allocCard(30, 30, 0, 0, nil, 0)
		alloc, rem := planSubscriptionDailyAllocation([]*service.UserSubscription{monthly, daily}, now, 110)
		// 月卡在前:先用满月卡 90,再日卡 20 → 都没透支。
		assertAlloc(t, alloc, []float64{90, 20}, rem, 0)
	})

	t.Run("overdraft_only_after_all_normal_exhausted", func(t *testing.T) {
		// 两卡正常额度合计 120;花 150 → 120 走正常,余 30 才从可透支的月卡走透支。
		daily := allocCard(30, 30, 0, 0, nil, 0)
		monthly := allocCard(2700, 90, 1164, 0, &five, 0)
		alloc, rem := planSubscriptionDailyAllocation([]*service.UserSubscription{daily, monthly}, now, 150)
		// 日卡 30(无透支额度), 月卡 90 正常 + 30 透支 = 120。
		assertAlloc(t, alloc, []float64{30, 120}, rem, 0)
	})

	t.Run("leftover_returned_for_slippage", func(t *testing.T) {
		// 单日卡 D30、不可透支;花 50 → 当日额度只能分 30,余 20 交溢出处理。
		daily := allocCard(30, 30, 0, 0, nil, 0)
		alloc, rem := planSubscriptionDailyAllocation([]*service.UserSubscription{daily}, now, 50)
		assertAlloc(t, alloc, []float64{30}, rem, 20)
	})

	t.Run("daily_partly_used_then_spill", func(t *testing.T) {
		// 日卡今天已用 25(剩当日额度 5)、月卡未用。花 40。
		// 日卡补 5 到限额,再月卡 35 → 都不透支。
		daily := allocCard(30, 30, 25, 25, nil, 0)
		monthly := allocCard(2700, 90, 1164, 0, &five, 0)
		alloc, rem := planSubscriptionDailyAllocation([]*service.UserSubscription{daily, monthly}, now, 40)
		assertAlloc(t, alloc, []float64{5, 35}, rem, 0)
	})

	t.Run("zero_cost_noop", func(t *testing.T) {
		monthly := allocCard(2700, 90, 0, 0, &five, 0)
		alloc, rem := planSubscriptionDailyAllocation([]*service.UserSubscription{monthly}, now, 0)
		assertAlloc(t, alloc, []float64{0}, rem, 0)
	})
}
