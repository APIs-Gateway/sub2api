package service

import "testing"

// TestIsDepleted 校验 burn-down「用完即失效」的耗尽判定 IsDepleted：
// 仅 burn-down 卡（G>0）参与；剩余 = G − consumed − clawed ≤ ~0（含浮点尾差）即视为耗尽。
func TestIsDepleted(t *testing.T) {
	cases := []struct {
		name                      string
		granted, consumed, clawed float64
		want                      bool
	}{
		{"fresh", 300, 0, 0, false},
		{"half_spent", 300, 150, 0, false},
		{"almost_spent", 300, 290, 5, false}, // remaining 5
		{"exact_by_consume", 300, 300, 0, true},
		{"exact_by_consume_and_claw", 300, 290, 10, true}, // 300-290-10 = 0
		{"float_residual_within_epsilon", 30, 29.99999999, 0, true},
		{"overspent_guard", 300, 305, 0, true}, // remaining 计为负，亦视为耗尽
		// legacy/standard 卡：G=0 不参与 burn-down，永不耗尽（避免被误标 expired）。
		{"legacy_zero_grant", 0, 0, 0, false},
		{"legacy_zero_grant_with_consume", 0, 5, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &UserSubscription{GrantedTotalUSD: c.granted, ConsumedUSD: c.consumed, ClawedUSD: c.clawed}
			if got := s.IsDepleted(); got != c.want {
				t.Fatalf("IsDepleted(granted=%v consumed=%v clawed=%v) = %v, want %v",
					c.granted, c.consumed, c.clawed, got, c.want)
			}
		})
	}
}
