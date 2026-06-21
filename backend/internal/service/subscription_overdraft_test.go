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
