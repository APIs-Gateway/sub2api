package service

import (
	"math"
	"testing"
	"time"
)

// per-day 计费模型测试基线：$10/天 × 30 天 = $300，2026-06-01 10:00(东八区)激活。
func odActivated() time.Time { return time.Date(2026, 6, 1, 10, 0, 0, 0, shanghaiLoc) }

// odSub 构造一张 burn-down 卡。dailySpent/dailySpentDay 表示「某日历天内已消费额度」。
func odSub(g, d, consumed, clawed, dailySpent float64, dailySpentDay int) *UserSubscription {
	a := odActivated()
	return &UserSubscription{
		StartsAt:        a,
		ActivatedAt:     &a,
		GrantedTotalUSD: g,
		DailyAmountUSD:  d,
		ConsumedUSD:     consumed,
		ClawedUSD:       clawed,
		DailySpentUSD:   dailySpent,
		DailySpentDay:   dailySpentDay,
	}
}

func odApprox(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// TestSubscriptionPerDaySpendable 校验 per-day 口径：
// spendable = clamp(0, min(remaining, (1+overdraftDays)×D − dailySpent(now)))。
// 关键不变量：①每个日历天至少能动用当天的 D（不被历史超支拖住）；②不跨天结转（次日 dailySpent 归零）。
func TestSubscriptionPerDaySpendable(t *testing.T) {
	day0 := odActivated()
	day2 := day0.AddDate(0, 0, 2)

	cases := []struct {
		name          string
		sub           *UserSubscription
		now           time.Time
		overdraftDays int
		wantSpendable float64
		wantLocked    float64
	}{
		// 激活当天、未用：解锁 1 天 → 可花 10，锁 290。
		{"day0_fresh", odSub(300, 10, 0, 0, 0, 0), day0, 0, 10, 290},
		// 当天透支 3 天：(1+3)×10=40 → 可花 40，锁 260。
		{"day0_overdraft3", odSub(300, 10, 0, 0, 0, 0), day0, 3, 40, 260},
		// 当天已用满 10、无透支：可花 0，全锁（闸门触发点）。
		{"day0_used_full", odSub(300, 10, 10, 0, 10, 0), day0, 0, 0, 290},
		// 当天已用 10、透支 3 天：(1+3)×10−10=30 → 可花 30，锁 260。
		{"day0_used_full_overdraft3", odSub(300, 10, 10, 0, 10, 0), day0, 3, 30, 260},
		// 次日重置（关键）：昨天(day0)用过 10，今天 day2、dailySpentDay 仍是 0 → 视为 0 → 照常可花 10。
		{"next_day_fresh_D", odSub(300, 10, 10, 0, 10, 0), day2, 0, 10, 280},
		// 存量已超支卡(sora_arti 型)：累计消费远超累计解锁、只剩 8，但今天未用 → 当天即可用满那 8。
		{"overspent_card_usable_today", odSub(300, 10, 292, 0, 0, 2), day2, 0, 8, 0},
		// cap 远超 remaining：可花 = remaining，锁 0。
		{"overdraft_huge", odSub(300, 10, 0, 0, 0, 0), day0, 100, 300, 0},
		// D=0(legacy/standard 卡)：可花 0、锁 0。
		{"zero_daily", &UserSubscription{StartsAt: day0, ActivatedAt: ptrTime(day0), GrantedTotalUSD: 0, DailyAmountUSD: 0}, day2, 0, 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.SpendableNowAt(tc.now, tc.overdraftDays); !odApprox(got, tc.wantSpendable) {
				t.Fatalf("SpendableNowAt=%v want %v", got, tc.wantSpendable)
			}
			if got := tc.sub.LockedAt(tc.now, tc.overdraftDays); !odApprox(got, tc.wantLocked) {
				t.Fatalf("LockedAt=%v want %v", got, tc.wantLocked)
			}
			// 不变量：spendable + locked == remaining。
			if got := tc.sub.SpendableNowAt(tc.now, tc.overdraftDays) + tc.sub.LockedAt(tc.now, tc.overdraftDays); !odApprox(got, tc.sub.RemainingUSD()) {
				t.Fatalf("spendable+locked=%v want remaining=%v", got, tc.sub.RemainingUSD())
			}
		})
	}
}

// TestSubscriptionFrontLoadNoDebt 校验「透支=计费周期前移、无还债」：当天预支多天后，
// 次日照常解锁当天的 D，不被前一天的超前消费锁死。
func TestSubscriptionFrontLoadNoDebt(t *testing.T) {
	day0 := odActivated()
	day1 := day0.AddDate(0, 0, 1)
	day2 := day0.AddDate(0, 0, 2)

	// D=100、10 天卡；day0 一次性突破到 600(今天 100 + 预支 5 天)。透支已达上限关闭(maxDays=nil)。
	burst := odSub(1000, 100, 600, 0, 600, 0)
	burst.TotalOverdraftCount = 5

	// 当天(day0)已用 600 → 可花 0。
	if got := burst.SpendableNowAt(day0, 0); !odApprox(got, 0) {
		t.Fatalf("day0 after burst spendable=%v want 0", got)
	}
	// 次日(day1):dailySpentDay 仍是 0 → 视为 0 → 照常可花 100（不锁、不还债）。
	if got := burst.SpendableNowAt(day1, 0); !odApprox(got, 100) {
		t.Fatalf("day1 spendable=%v want 100 (fresh D, no debt)", got)
	}
	if got := burst.LockedAt(day1, 0); !odApprox(got, 300) {
		t.Fatalf("day1 locked=%v want 300", got)
	}
	// 再次日(day2)同样可花 100。
	if got := burst.SpendableNowAt(day2, 0); !odApprox(got, 100) {
		t.Fatalf("day2 spendable=%v want 100", got)
	}
}

// TestSubscriptionOverdraftDaysDelta 校验「当天新增预支天数」delta：
// delta = preSpendDays(dailySpent+amount) − preSpendDays(dailySpent)。
func TestSubscriptionOverdraftDaysDelta(t *testing.T) {
	day0 := odActivated()
	// D=60；dailySpentDay=0 使 dailySpent 在 day0 生效。
	sub := func(dailySpent float64) *UserSubscription { return odSub(1800, 60, 0, 0, dailySpent, 0) }

	cases := []struct {
		name      string
		sub       *UserSubscription
		amount    float64
		wantDelta int
	}{
		{"exact_one_day_no_overdraft", sub(0), 60, 0}, // 恰好用满当天 D → 0
		{"one_cent_over", sub(0), 60.01, 1},           // 越线一点 → 预支 1 天
		{"burst_5_days", sub(0), 360, 5},              // 一次花到 6 天量 → 预支 5 天
		{"within_today", sub(0), 30, 0},               // 当天额度内 → 0
		{"two_days_exact", sub(0), 120, 1},            // 2×D → 预支 1 天
		{"amount_zero", sub(61.60), 0, 0},             // amount≤0 → 0
		{"same_day_no_new", sub(61.60), 0.01, 0},      // 已预支 1 天，本笔仍在该天内 → 0
		{"cross_into_next", sub(61.60), 60, 1},        // 推进到第 2 个预支天 → +1
		{"zero_daily", &UserSubscription{StartsAt: day0, ActivatedAt: ptrTime(day0), DailyAmountUSD: 0}, 100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.OverdraftDaysDeltaAt(day0, tc.amount); got != tc.wantDelta {
				t.Fatalf("OverdraftDaysDeltaAt=%d want %d", got, tc.wantDelta)
			}
		})
	}
}

// TestSubscriptionEffectiveOverdraftDays 校验闸门/分摊同口径的有效预支天数：
// effOD = clamp(0, min(配置, 5 − toc + 今日已预支天数))。
func TestSubscriptionEffectiveOverdraftDays(t *testing.T) {
	day0 := odActivated()
	// D=100,DailySpentDay=0 使 dailySpent 在 day0 生效。
	mk := func(maxDays *int, toc int, dailySpent float64) *UserSubscription {
		s := odSub(1000, 100, 0, 0, dailySpent, 0)
		s.MaxOverdraftDays = maxDays
		s.TotalOverdraftCount = toc
		return s
	}
	five, two := 5, 2

	cases := []struct {
		name string
		sub  *UserSubscription
		want int
	}{
		{"overdraft_off", mk(nil, 0, 0), 0},
		{"fresh_full_budget", mk(&five, 0, 0), 5},
		// toc=4 → 生命周期只剩 1 天预支（修复闸门按配置 5 越权）。
		{"budget_nearly_used", mk(&five, 4, 0), 1},
		// toc=2 但今天已预支 2 天(ds=250) → 加回 → 当日额度不自我收缩,仍为 5。
		{"intraday_no_shrink", mk(&five, 2, 250), 5},
		{"configured_lower", mk(&two, 0, 0), 2},
		{"zero_daily", &UserSubscription{StartsAt: day0, ActivatedAt: ptrTime(day0), MaxOverdraftDays: &five, DailyAmountUSD: 0}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.EffectiveOverdraftDaysAt(day0); got != tc.want {
				t.Fatalf("EffectiveOverdraftDaysAt=%d want %d", got, tc.want)
			}
		})
	}
}

// TestSubscriptionOverdraftUseHelpers 校验 RemainingOverdraftUses / CanEnableOverdraft / UsesOverdraftAt。
func TestSubscriptionOverdraftUseHelpers(t *testing.T) {
	day0 := odActivated()
	// D=10。
	sub := odSub(300, 10, 0, 0, 0, 0)
	sub.TotalOverdraftCount = 4

	if got := sub.RemainingOverdraftUses(); got != 1 {
		t.Fatalf("RemainingOverdraftUses=%d want 1", got)
	}
	if !sub.CanEnableOverdraft() {
		t.Fatal("toc=4 应可开启透支")
	}

	// 当天用满 10 = 当日额度，未预支；再加 0.001 → 跨入预支第 1 天 → 算透支。
	sub.DailySpentUSD, sub.DailySpentDay = 10, 0
	if sub.UsesOverdraftAt(day0, 0) {
		t.Fatal("amount=0 不应算透支")
	}
	if sub.UsesOverdraftAt(day0, -1) {
		t.Fatal("amount<0 不应算透支")
	}
	if !sub.UsesOverdraftAt(day0, 0.001) {
		t.Fatal("用满当天 D 后再消费应算透支")
	}
	// 当天只用了半天额度(5)，再花 4 仍在当天 D 内 → 不算透支。
	sub.DailySpentUSD = 5
	if sub.UsesOverdraftAt(day0, 4) {
		t.Fatal("当天 D 额度内不应算透支")
	}

	sub.TotalOverdraftCount = MaxSubscriptionOverdraftUses
	if got := sub.RemainingOverdraftUses(); got != 0 {
		t.Fatalf("RemainingOverdraftUses at cap=%d want 0", got)
	}
	if sub.CanEnableOverdraft() {
		t.Fatal("达上限后不应可开启透支")
	}
}

// TestSubscriptionOverdraftGate 校验透支准入闸门(per-day)：用 min(balance, Σremaining) 而非用户总余额，
// 防止充值/签到余额架空闸门(回归 user 280)；并覆盖存量超支卡当天可用。
func TestSubscriptionOverdraftGate(t *testing.T) {
	day0 := odActivated()
	one := 1

	// G=1800, D=60；dailySpent 表示当天已用,consumed 决定 remaining。day0 当天消费即 dailySpent。
	card := func(consumed float64, dailySpent float64, maxDays *int, count int) UserSubscription {
		s := odSub(1800, 60, consumed, 0, dailySpent, 0)
		s.MaxOverdraftDays = maxDays
		s.TotalOverdraftCount = count
		return *s
	}
	gate := func(subs []UserSubscription, extra float64) error {
		cl, ll, rem, co := aggregateSubscriptionLocks(subs, day0)
		return subscriptionOverdraftGate(rem+extra, cl, ll, rem, co)
	}

	// extra = 充值(非订阅)余额。下列订阅限速判定用 extra=0(无充值)以走到订阅逻辑。
	t.Run("recharge_balance_funds_overflow", func(t *testing.T) {
		// 当天 60 花尽、未开透支,但有 13.84 充值余额 → 放行(由充值承担,卡锁定额度不被烧)。
		if err := gate([]UserSubscription{card(60, 60, nil, 0)}, 13.84); err != nil {
			t.Fatalf("有充值余额时应放行，got %v", err)
		}
	})
	t.Run("today_unlock_available_passes", func(t *testing.T) {
		if err := gate([]UserSubscription{card(0, 0, &one, 0)}, 0); err != nil {
			t.Fatalf("当天解锁额度可用时应放行，got %v", err)
		}
	})
	t.Run("within_overdraft_allowance_passes", func(t *testing.T) {
		// 当天 60 用尽,仍在 max_overdraft_days=1 额度内 → 放行。
		if err := gate([]UserSubscription{card(60, 60, &one, 0)}, 0); err != nil {
			t.Fatalf("仍在透支额度内应放行，got %v", err)
		}
	})
	t.Run("overdraft_off_no_recharge_blocks", func(t *testing.T) {
		// 当天花尽、未开透支、无充值余额 → 拦截。
		if err := gate([]UserSubscription{card(60, 60, nil, 0)}, 0); err != ErrSubscriptionOverdraftLimit {
			t.Fatalf("未开透支/无充值且当天额度花尽应被拦，got %v", err)
		}
	})
	t.Run("overspent_card_usable_today", func(t *testing.T) {
		// 存量超支卡:剩余 28.24、今天未用 → currentLocked=0 → 放行(可用当天 D 覆盖那 28.24)。
		if err := gate([]UserSubscription{card(1771.76, 0, nil, 0)}, 0); err != nil {
			t.Fatalf("超支卡当天应可用其剩余，got %v", err)
		}
	})
}
