package service

import (
	"context"
	"errors"
	"testing"
)

// creditFiatSettingRepoStub 只实现折算路径真正会用到的 GetMultiple，
// 其余方法一律 panic——被调用就说明实现走了预期之外的路。
type creditFiatSettingRepoStub struct {
	values map[string]string
	err    error
}

func (creditFiatSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}
func (creditFiatSettingRepoStub) GetValue(context.Context, string) (string, error) {
	panic("unexpected GetValue call")
}
func (creditFiatSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}
func (r creditFiatSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.values, nil
}
func (creditFiatSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}
func (creditFiatSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}
func (creditFiatSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

// 生产当前的设置值：1 元兑 13 额度，D∈[30,420]，D≥210 起吃最低单价 0.04。
func prodCreditFiatSettings() map[string]string {
	return map[string]string{
		SettingBalanceRechargeMult:            "13.00",
		SettingSubscriptionMinDaily:           "30.00",
		SettingSubscriptionMaxDaily:           "420.00",
		SettingSubscriptionMinRatioStartDaily: "210.00",
		SettingSubscriptionMinRatio:           "0.05",
		SettingSubscriptionMaxRatio:           "0.04",
		SettingSubscriptionMaxDays:            "360",
	}
}

func TestCreditFiatPricing_ReadsProductionSettings(t *testing.T) {
	svc := NewSettingService(creditFiatSettingRepoStub{values: prodCreditFiatSettings()}, nil)

	multiplier, cfg := svc.CreditFiatPricing(context.Background())

	if !approx(multiplier, 13, 1e-9) {
		t.Errorf("multiplier=%v want 13", multiplier)
	}
	// u(D) 端点必须落在设置值上，否则订阅侧折算会整体偏掉。
	if u := cfg.UnitPrice(30); !approx(u, 0.05, 1e-9) {
		t.Errorf("UnitPrice(30)=%v want 0.05", u)
	}
	if u := cfg.UnitPrice(210); !approx(u, 0.04, 1e-9) {
		t.Errorf("UnitPrice(210)=%v want 0.04", u)
	}
}

// 读设置失败必须回落到默认值，而不是把倍率变成 0 让折算炸掉。
func TestCreditFiatPricing_FallsBackOnError(t *testing.T) {
	svc := NewSettingService(creditFiatSettingRepoStub{err: errors.New("db down")}, nil)

	multiplier, cfg := svc.CreditFiatPricing(context.Background())

	if multiplier != defaultBalanceRechargeMultiplier {
		t.Errorf("multiplier=%v want %v", multiplier, defaultBalanceRechargeMultiplier)
	}
	if cfg != DefaultSubscriptionPricingConfig() {
		t.Errorf("cfg=%+v want default", cfg)
	}
}

// nil service / nil repo 都不能 panic——handler 在依赖未接线时会走到这里。
func TestCreditFiatPricing_NilReceiverAndRepoAreSafe(t *testing.T) {
	var nilSvc *SettingService
	multiplier, cfg := nilSvc.CreditFiatPricing(context.Background())
	if multiplier != defaultBalanceRechargeMultiplier || cfg != DefaultSubscriptionPricingConfig() {
		t.Errorf("nil service 应返回默认值, got %v / %+v", multiplier, cfg)
	}

	emptySvc := NewSettingService(nil, nil)
	multiplier, cfg = emptySvc.CreditFiatPricing(context.Background())
	if multiplier != defaultBalanceRechargeMultiplier || cfg != DefaultSubscriptionPricingConfig() {
		t.Errorf("nil repo 应返回默认值, got %v / %+v", multiplier, cfg)
	}
}

// 倍率损坏（0/负数/非数字）时退化为 1，即不折算。
func TestCreditFiatPricing_NormalizesBrokenMultiplier(t *testing.T) {
	for _, raw := range []string{"0", "-13", "abc", ""} {
		vals := prodCreditFiatSettings()
		vals[SettingBalanceRechargeMult] = raw
		svc := NewSettingService(creditFiatSettingRepoStub{values: vals}, nil)

		multiplier, _ := svc.CreditFiatPricing(context.Background())
		if multiplier != defaultBalanceRechargeMultiplier {
			t.Errorf("raw=%q: multiplier=%v want %v", raw, multiplier, defaultBalanceRechargeMultiplier)
		}
	}
}

func TestSubscriptionPricingConfig_ReadsSettings(t *testing.T) {
	svc := NewSettingService(creditFiatSettingRepoStub{values: prodCreditFiatSettings()}, nil)

	cfg := svc.SubscriptionPricingConfig(context.Background())

	if !approx(cfg.DMin, 30, 1e-9) || !approx(cfg.DMax, 420, 1e-9) {
		t.Errorf("D 区间=[%v,%v] want [30,420]", cfg.DMin, cfg.DMax)
	}
	if !approx(cfg.DFloor, 210, 1e-9) {
		t.Errorf("DFloor=%v want 210", cfg.DFloor)
	}
}

func TestSubscriptionPricingConfig_FallsBackOnError(t *testing.T) {
	svc := NewSettingService(creditFiatSettingRepoStub{err: errors.New("db down")}, nil)
	if cfg := svc.SubscriptionPricingConfig(context.Background()); cfg != DefaultSubscriptionPricingConfig() {
		t.Errorf("cfg=%+v want default", cfg)
	}

	var nilSvc *SettingService
	if cfg := nilSvc.SubscriptionPricingConfig(context.Background()); cfg != DefaultSubscriptionPricingConfig() {
		t.Errorf("nil service cfg=%+v want default", cfg)
	}
}

// SubscriptionService 的定价装载已委托给 SettingService，两条路必须给出同一份配置，
// 否则下单报价与展示折算会用不同的公式。
func TestSubscriptionServiceDelegatesPricingConfig(t *testing.T) {
	settingSvc := NewSettingService(creditFiatSettingRepoStub{values: prodCreditFiatSettings()}, nil)
	subSvc := &SubscriptionService{settingService: settingSvc}

	want := settingSvc.SubscriptionPricingConfig(context.Background())
	if got := subSvc.subscriptionPricingConfig(context.Background()); got != want {
		t.Errorf("委托结果 %+v 与 SettingService 的 %+v 不一致", got, want)
	}

	var nilSub *SubscriptionService
	if got := nilSub.subscriptionPricingConfig(context.Background()); got != DefaultSubscriptionPricingConfig() {
		t.Errorf("nil SubscriptionService 应回落默认, got %+v", got)
	}
}
