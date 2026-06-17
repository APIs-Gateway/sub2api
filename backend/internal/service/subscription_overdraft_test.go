package service

import (
	"math"
	"testing"
	"time"
)

// TestSubscriptionOverdraftHelpers 校验「最多往后透支 N 天」准入闸门用到的
// SpendableNowAt / LockedAt：spendable = clamp(0, min(remaining, (elapsed+N)×D − consumed))。
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
		// 第 0 天、N=0、未消费：cap=(0+0)*10=0 → 可花 0，余额里 300 全锁定。
		{"day0_N0_fresh", newSub(0, 0), day0, 0, 0, 300},
		// 第 0 天、N=3：cap=30 → 可花 30，锁定 270。
		{"day0_N3", newSub(0, 0), day0, 3, 30, 270},
		// 第 2 天、N=0：cap=20 → 可花 20，锁定 280。
		{"day2_N0", newSub(0, 0), day2, 0, 20, 280},
		// 第 2 天、N=3、已消费 20：cap=(2+3)*10=50，剩可扣 50-20=30；remaining=280 → 可花 30，锁定 250。
		{"day2_N3_consumed20", newSub(20, 0), day2, 3, 30, 250},
		// 已消费到/超过 cap：cap=50，consumed=50 → 可花 0；remaining=250 全锁定（闸门触发点）。
		{"day2_N3_atcap", newSub(50, 0), day2, 3, 0, 250},
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

// TestEffectiveOverdraftDaysAt 校验施加全局上限后的有效透支天数：
//   - 未自设卡按 adminCap；自设卡取 min(卡值, adminCap)（用户只能更严，不能比上限更松）。
//   - adminCap=0 即「不允许预支」（最严，非无限）。
//   - 已超额老用户：取「已透支天数」作下限，避免把额度压成负值；其当前可花≈0~不到 1 日额度，
//     随日历天推进 floor 自然下降、回到上限内（绝不无限透支）。
func TestEffectiveOverdraftDaysAt(t *testing.T) {
	loc := shanghaiLoc
	activated := time.Date(2026, 6, 1, 10, 0, 0, 0, loc) // $10/天
	day2 := activated.AddDate(0, 0, 2)                   // N=2

	intPtr := func(v int) *int { return &v }
	newSub := func(consumed float64, maxDays *int) *UserSubscription {
		a := activated
		return &UserSubscription{
			StartsAt:         activated,
			ActivatedAt:      &a,
			GrantedTotalUSD:  300,
			DailyAmountUSD:   10,
			ConsumedUSD:      consumed,
			MaxOverdraftDays: maxDays,
		}
	}

	cases := []struct {
		name     string
		sub      *UserSubscription
		adminCap int
		want     int
	}{
		// 未超额（consumed=0，floor=0）：未自设 → 取 cap。
		{"nullcard_cap1", newSub(0, nil), 1, 1},
		{"nullcard_cap0_strict", newSub(0, nil), 0, 0},    // cap=0 = 不允许预支
		{"nullcard_capNeg_clamp0", newSub(0, nil), -1, 0}, // 负值夹到 0
		// 自设卡：取 min(卡值, cap)。
		{"card0_cap5", newSub(0, intPtr(0)), 5, 0}, // 用户自设更严
		{"card3_cap10", newSub(0, intPtr(3)), 10, 3},
		{"card10_cap3_clamp", newSub(0, intPtr(10)), 3, 3}, // 卡值超 cap → 被 cap 夹住
		// 已超额老用户豁免（floor = ceil(consumed/D) - N）。
		{"over_consumed80_cap1", newSub(80, nil), 1, 6},   // 80/10-2=6 > cap1 → 6
		{"over_consumed50_cap0", newSub(50, nil), 0, 3},   // cap0 也豁免到 floor=3
		{"within_consumed20_cap5", newSub(20, nil), 5, 5}, // 2-2=0，floor 不触发 → cap
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.EffectiveOverdraftDaysAt(day2, tc.adminCap); got != tc.want {
				t.Fatalf("EffectiveOverdraftDaysAt(cap=%d)=%d want %d", tc.adminCap, got, tc.want)
			}
		})
	}

	// D=0（legacy/standard 卡）：无预支概念，floor 恒 0 → 取 cap，不 panic。
	zero := &UserSubscription{StartsAt: activated, ActivatedAt: &activated, DailyAmountUSD: 0}
	if got := zero.EffectiveOverdraftDaysAt(day2, 5); got != 5 {
		t.Fatalf("zero-daily EffectiveOverdraftDaysAt=%d want 5", got)
	}
}
