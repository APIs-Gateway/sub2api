package service

import (
	"testing"
	"time"
)

// 透支用量计数测试基线：$10/天 × 30 天 = $300，2026-06-01 10:00(东八区)激活。
func odActivated() time.Time { return time.Date(2026, 6, 1, 10, 0, 0, 0, shanghaiLoc) }

// odSub 构造一张订阅卡（仅用于透支开关相关断言）。
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

// TestSubscriptionOverdraftUseHelpers 校验 RemainingOverdraftUses / CanEnableOverdraft：
// 累计预支天数封顶 5 天，达上限后不可再开启透支。
func TestSubscriptionOverdraftUseHelpers(t *testing.T) {
	// D=10。
	sub := odSub(300, 10, 0, 0, 0, 0)
	sub.TotalOverdraftCount = 4

	if got := sub.RemainingOverdraftUses(); got != 1 {
		t.Fatalf("RemainingOverdraftUses=%d want 1", got)
	}
	if !sub.CanEnableOverdraft() {
		t.Fatal("toc=4 应可开启透支")
	}

	sub.TotalOverdraftCount = MaxSubscriptionOverdraftUses
	if got := sub.RemainingOverdraftUses(); got != 0 {
		t.Fatalf("RemainingOverdraftUses at cap=%d want 0", got)
	}
	if sub.CanEnableOverdraft() {
		t.Fatal("达上限后不应可开启透支")
	}
}
