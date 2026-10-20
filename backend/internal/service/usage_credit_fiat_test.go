package service

import (
	"context"
	"testing"
)

func subIDPtr(v int64) *int64 { return &v }

// 只有订阅扣费且卡号合法的记录才需要查卡；钱包记录、nil 卡号、非正卡号都要跳过，
// 重复卡号只查一次。判错的后果是折算走错单价，用户看到的花费偏离真实值近一倍。
func TestCollectSubscriptionIDs(t *testing.T) {
	logs := []UsageLog{
		{BillingType: BillingTypeBalance, SubscriptionID: subIDPtr(101)},      // 钱包记录：即便带卡号也跳过
		{BillingType: BillingTypeSubscription, SubscriptionID: nil},           // 无卡号
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(0)},   // 卡号非正
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(-5)},  // 卡号非正
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(202)}, // 命中
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(202)}, // 重复，去重
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(303)}, // 命中
	}

	got := collectSubscriptionIDs(logs)

	if len(got) != 2 {
		t.Fatalf("collectSubscriptionIDs 返回 %v, want 两个 ID", got)
	}
	if got[0] != 202 || got[1] != 303 {
		t.Errorf("collectSubscriptionIDs=%v want [202 303]", got)
	}
}

func TestCollectSubscriptionIDs_EmptyInputs(t *testing.T) {
	if got := collectSubscriptionIDs(nil); len(got) != 0 {
		t.Errorf("nil logs 应返回空, got %v", got)
	}
	if got := collectSubscriptionIDs([]UsageLog{}); len(got) != 0 {
		t.Errorf("空 logs 应返回空, got %v", got)
	}
	walletOnly := []UsageLog{
		{BillingType: BillingTypeBalance},
		{BillingType: BillingTypeBalance},
	}
	if got := collectSubscriptionIDs(walletOnly); len(got) != 0 {
		t.Errorf("纯钱包记录应返回空, got %v", got)
	}
}

// 依赖未接线时必须安全退化成「按充值倍率折算」，而不是 panic 或返回 nil 折算器。
// entClient 为 nil 时若实现仍试图查库，这里会直接 panic。
func TestBuildCreditFiatRate_DegradesWithoutDependencies(t *testing.T) {
	cfg := prodPricingConfig()
	logs := []UsageLog{
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(202), ActualCost: 5},
	}
	ctx := context.Background()

	cases := []struct {
		name   string
		svc    *UsageService
		userID int64
	}{
		{"nil service", nil, 1},
		{"nil entClient", &UsageService{}, 1},
		{"userID 非法", &UsageService{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate := tc.svc.BuildCreditFiatRate(ctx, tc.userID, 13, cfg, logs)
			if rate == nil {
				t.Fatal("BuildCreditFiatRate 不应返回 nil")
			}
			// 卡没登记上，订阅记录回落到钱包价 5 ÷ 13。
			if got := rate.Convert(5, BillingTypeSubscription, 202); !approx(got, 0.38461538, 1e-8) {
				t.Errorf("Convert=%v want≈0.38461538（钱包价）", got)
			}
		})
	}
}

// 整页都是钱包扣费时不该查库——entClient 为 nil 而不 panic 就证明了这一点。
func TestBuildCreditFiatRate_WalletOnlyPageSkipsLookup(t *testing.T) {
	svc := &UsageService{}
	logs := []UsageLog{
		{BillingType: BillingTypeBalance, ActualCost: 0.5},
		{BillingType: BillingTypeBalance, ActualCost: 1.5},
	}

	rate := svc.BuildCreditFiatRate(context.Background(), 1, 13, prodPricingConfig(), logs)

	if got := rate.Convert(0.5, BillingTypeBalance, 0); !approx(got, 0.03846154, 1e-8) {
		t.Errorf("Convert(0.5)=%v want≈0.03846154", got)
	}
}
