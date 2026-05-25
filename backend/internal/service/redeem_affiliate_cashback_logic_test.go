//go:build unit

package service

import "testing"

func TestShouldAccrueAffiliateCashbackForRedeem(t *testing.T) {
	groupID := int64(9)

	tests := []struct {
		name string
		code *RedeemCode
		want bool
	}{
		{
			name: "nil code",
			code: nil,
			want: false,
		},
		{
			name: "positive balance redeem",
			code: &RedeemCode{Type: RedeemTypeBalance, Value: 10},
			want: true,
		},
		{
			name: "zero balance redeem",
			code: &RedeemCode{Type: RedeemTypeBalance, Value: 0},
			want: false,
		},
		{
			name: "negative balance redeem",
			code: &RedeemCode{Type: RedeemTypeBalance, Value: -10},
			want: false,
		},
		{
			name: "subscription redeem with group and validity",
			code: &RedeemCode{Type: RedeemTypeSubscription, GroupID: &groupID, ValidityDays: 30},
			want: true,
		},
		{
			name: "subscription redeem missing group",
			code: &RedeemCode{Type: RedeemTypeSubscription, ValidityDays: 30},
			want: false,
		},
		{
			name: "subscription redeem zero validity",
			code: &RedeemCode{Type: RedeemTypeSubscription, GroupID: &groupID, ValidityDays: 0},
			want: false,
		},
		{
			name: "concurrency redeem",
			code: &RedeemCode{Type: RedeemTypeConcurrency, Value: 1},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAccrueAffiliateCashbackForRedeem(tc.code); got != tc.want {
				t.Fatalf("shouldAccrueAffiliateCashbackForRedeem() = %v, want %v", got, tc.want)
			}
		})
	}
}
