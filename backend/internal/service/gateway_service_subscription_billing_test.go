//go:build unit

package service

import (
	"testing"
)

// TestBuildUsageBillingCommand_AlwaysBillsBalanceWithRateMultiplier 锁定 burn-down 模型：
// 所有计费统一进 BalanceCost（= ActualCost = TotalCost × RateMultiplier），SubscriptionCost 恒为 0；
// 订阅优先归集由计费仓储在事务内完成。免费（multiplier 0）不扣费。
func TestBuildUsageBillingCommand_AlwaysBillsBalanceWithRateMultiplier(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	subID := int64(42)

	tests := []struct {
		name           string
		totalCost      float64
		actualCost     float64
		isSubscription bool
		wantSub        float64
		wantBalance    float64
	}{
		{
			name:           "subscription group with 2x multiplier bills 2x to balance",
			totalCost:      1.0,
			actualCost:     2.0,
			isSubscription: true,
			wantSub:        0,
			wantBalance:    2.0,
		},
		{
			name:           "subscription group with 0.5x multiplier bills 0.5x to balance",
			totalCost:      1.0,
			actualCost:     0.5,
			isSubscription: true,
			wantSub:        0,
			wantBalance:    0.5,
		},
		{
			name:           "free request (multiplier 0) bills nothing",
			totalCost:      1.0,
			actualCost:     0,
			isSubscription: true,
			wantSub:        0,
			wantBalance:    0,
		},
		{
			name:           "balance billing uses ActualCost",
			totalCost:      1.0,
			actualCost:     2.0,
			isSubscription: false,
			wantSub:        0,
			wantBalance:    2.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &postUsageBillingParams{
				Cost:               &CostBreakdown{TotalCost: tt.totalCost, ActualCost: tt.actualCost},
				User:               &User{ID: 1},
				APIKey:             &APIKey{ID: 2, GroupID: &groupID},
				Account:            &Account{ID: 3},
				Subscription:       &UserSubscription{ID: subID},
				IsSubscriptionBill: tt.isSubscription,
			}

			cmd := buildUsageBillingCommand("req-1", nil, p)
			if cmd == nil {
				t.Fatal("buildUsageBillingCommand returned nil")
			}
			if cmd.SubscriptionCost != tt.wantSub {
				t.Errorf("SubscriptionCost = %v, want %v", cmd.SubscriptionCost, tt.wantSub)
			}
			if cmd.BalanceCost != tt.wantBalance {
				t.Errorf("BalanceCost = %v, want %v", cmd.BalanceCost, tt.wantBalance)
			}
		})
	}
}
