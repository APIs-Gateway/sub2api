package service

import (
	"context"
	"math"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 官方价汇率只用于把模型官方美元价显示成人民币对照价；未配置或存了坏值时
// 必须回落到默认汇率，不能把 0 交给前端（那会让官方价显示成 ¥0）。
func TestParsePaymentConfig_OfficialPriceCNYRate(t *testing.T) {
	svc := &PaymentConfigService{}
	cases := map[string]float64{
		"":      DefaultOfficialPriceCNYRate,
		"7.15":  7.15,
		"0":     DefaultOfficialPriceCNYRate,
		"-3":    DefaultOfficialPriceCNYRate,
		"abc":   DefaultOfficialPriceCNYRate,
		"100.5": DefaultOfficialPriceCNYRate,
	}
	for raw, want := range cases {
		cfg := svc.parsePaymentConfig(map[string]string{SettingOfficialPriceCNYRate: raw})
		if math.Abs(cfg.OfficialPriceCNYRate-want) > 1e-9 {
			t.Fatalf("raw %q parsed as %v, want %v", raw, cfg.OfficialPriceCNYRate, want)
		}
	}
}

func TestUpdatePaymentConfig_OfficialPriceCNYRate(t *testing.T) {
	t.Run("persists without rounding", func(t *testing.T) {
		repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
		svc := &PaymentConfigService{settingRepo: repo}
		rate := 7.1234
		if err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{OfficialPriceCNYRate: &rate}); err != nil {
			t.Fatalf("UpdatePaymentConfig returned error: %v", err)
		}
		if got := repo.values[SettingOfficialPriceCNYRate]; got != "7.1234" {
			t.Fatalf("stored %q, want 7.1234", got)
		}
	})

	for _, bad := range []float64{0, -1, 100.01, math.NaN(), math.Inf(1)} {
		repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
		svc := &PaymentConfigService{settingRepo: repo}
		v := bad
		err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{OfficialPriceCNYRate: &v})
		if got := infraerrors.Reason(err); got != "INVALID_OFFICIAL_PRICE_CNY_RATE" {
			t.Fatalf("rate %v: Reason(err) = %q", bad, got)
		}
		if len(repo.updates) != 0 {
			t.Fatalf("rate %v: settings were written: %v", bad, repo.updates)
		}
	}
}
