package service

import (
	"math"
	"testing"
	"time"
)

// TestSubscriptionOverdraftHelpers 校验「最多往后透支 N 天」准入闸门用到的
// SpendableNowAt / LockedAt：spendable = clamp(0, min(remaining, (elapsed+1+N)×D − consumed))。
// 「+1」= 当天即解锁 1 天额度（买当天就能用，1 天卡也可用满）。
func TestSubscriptionOverdraftHelpers(t *testing.T) {
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

	day0 := activated
	day2 := activated.AddDate(0, 0, 2)

	cases := []struct {
		name          string
		sub           *UserSubscription
		now           time.Time
		overdraftDays int
		wantSpendable float64
		wantLocked    float64
	}{
		// 第 0 天、N=0、未消费：cap=(0+1+0)*10=10 → 当天解锁 1 天，可花 10，锁定 290。
		{"day0_N0_fresh", newSub(0, 0), day0, 0, 10, 290},
		// 第 0 天、透支 3 天：cap=(0+1+3)*10=40 → 可花 40，锁定 260。
		{"day0_N3", newSub(0, 0), day0, 3, 40, 260},
		// 第 2 天、N=0：cap=(2+1+0)*10=30 → 可花 30，锁定 270。
		{"day2_N0", newSub(0, 0), day2, 0, 30, 270},
		// 第 2 天、透支 3 天、已消费 20：cap=(2+1+3)*10=60，剩可扣 60-20=40；remaining=280 → 可花 40，锁定 240。
		{"day2_N3_consumed20", newSub(20, 0), day2, 3, 40, 240},
		// 已消费到 cap：cap=60，consumed=60 → 可花 0；remaining=240 全锁定（闸门触发点）。
		{"day2_N3_atcap", newSub(60, 0), day2, 3, 0, 240},
		{"day2_N3_overcap", newSub(80, 0), day2, 3, 0, 220},
		// cap 超过 remaining：N 很大 → 可花 = remaining，锁定 0（等同无限）。
		{"day2_Nbig", newSub(0, 0), day2, 100, 300, 0},
		// D=0（legacy/standard 卡）：cap=0、remaining=0 → 可花 0、锁定 0，无副作用。
		{"zero_daily", &UserSubscription{StartsAt: activated, ActivatedAt: &activated, GrantedTotalUSD: 0, DailyAmountUSD: 0}, day2, 0, 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.SpendableNowAt(tc.now, tc.overdraftDays); !approx(got, tc.wantSpendable) {
				t.Fatalf("SpendableNowAt=%v want %v", got, tc.wantSpendable)
			}
			if got := tc.sub.LockedAt(tc.now, tc.overdraftDays); !approx(got, tc.wantLocked) {
				t.Fatalf("LockedAt=%v want %v", got, tc.wantLocked)
			}
			// 不变量：spendable + locked == remaining。
			if got := tc.sub.SpendableNowAt(tc.now, tc.overdraftDays) + tc.sub.LockedAt(tc.now, tc.overdraftDays); !approx(got, tc.sub.RemainingUSD()) {
				t.Fatalf("spendable+locked=%v want remaining=%v", got, tc.sub.RemainingUSD())
			}
		})
	}
}

func TestSubscriptionOverdraftUseHelpers(t *testing.T) {
	loc := shanghaiLoc
	activated := time.Date(2026, 6, 1, 10, 0, 0, 0, loc)
	day2 := activated.AddDate(0, 0, 2)
	a := activated
	sub := &UserSubscription{
		StartsAt:            activated,
		ActivatedAt:         &a,
		GrantedTotalUSD:     300,
		DailyAmountUSD:      10,
		ConsumedUSD:         29.99,
		TotalOverdraftCount: 4,
	}

	if got := sub.RemainingOverdraftUses(); got != 1 {
		t.Fatalf("RemainingOverdraftUses=%d want 1", got)
	}
	if !sub.CanEnableOverdraft() {
		t.Fatal("card with one remaining overdraft use should be enableable")
	}
	if sub.UsesOverdraftAt(day2, 0.01) {
		t.Fatal("request within the current-day cap should not count as overdraft")
	}
	if !sub.UsesOverdraftAt(day2, 0.02) {
		t.Fatal("request crossing the current-day cap should count as overdraft")
	}

	sub.ConsumedUSD = 35
	if !sub.UsesOverdraftAt(day2, 0.01) {
		t.Fatal("request starting after current-day cap is exhausted should count as overdraft")
	}

	sub.TotalOverdraftCount = MaxSubscriptionOverdraftUses
	if got := sub.RemainingOverdraftUses(); got != 0 {
		t.Fatalf("RemainingOverdraftUses at cap=%d want 0", got)
	}
	if sub.CanEnableOverdraft() {
		t.Fatal("card at max overdraft uses should not be enableable")
	}
}

// TestSubscriptionOverdraftDaysReached 校验「按往后预支天数计量」：消费前沿领先解锁线几天，
// 同一天内多个越线小请求触及同一天 → 只占 1 天，不会按请求数瞬间烧光配额（493 的根因）。
func TestSubscriptionOverdraftDaysReached(t *testing.T) {
	loc := shanghaiLoc
	activated := time.Date(2026, 6, 1, 10, 0, 0, 0, loc) // $60/天
	day0 := activated
	day2 := activated.AddDate(0, 0, 2)

	newSub := func(consumed float64) *UserSubscription {
		a := activated
		return &UserSubscription{StartsAt: activated, ActivatedAt: &a, GrantedTotalUSD: 1800, DailyAmountUSD: 60, ConsumedUSD: consumed}
	}

	cases := []struct {
		name     string
		sub      *UserSubscription
		now      time.Time
		amount   float64
		wantDays int
	}{
		// 493 的真实场景：day0 已花 $61.60，再分摊 $0 → 前沿落在 day1，仅领先 1 天（旧实现会按请求计成 5）。
		{"day0_493_over_by_1.6", newSub(61.60), day0, 0, 1},
		{"day0_exact_one_day", newSub(0), day0, 60, 0},      // 恰好花满当天额度 → 不算透支
		{"day0_one_cent_over", newSub(0), day0, 60.01, 1},   // 越线一点点 → 领先 1 天
		{"day0_reach_day5", newSub(0), day0, 360, 5},        // day0 直接花到第 6 天额度 → 领先 5 天
		{"day0_within_today", newSub(0), day0, 30, 0},       // 当天额度内 → 0
		{"day2_reach_day5", newSub(0), day2, 360, 3},        // day2 解锁到 day2，花到 day5 → 领先 3 天
		{"day2_within_unlocked", newSub(0), day2, 180, 0},   // day2 解锁 3 天=$180，恰好用满 → 0
		{"zero_daily", &UserSubscription{StartsAt: activated, ActivatedAt: &activated, DailyAmountUSD: 0}, day0, 100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.OverdraftDaysReachedAt(tc.now, tc.amount); got != tc.wantDays {
				t.Fatalf("OverdraftDaysReachedAt=%d want %d", got, tc.wantDays)
			}
		})
	}
}
