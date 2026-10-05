//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 真实 settings 表：启动时读 CREDIT_CURRENCY / 充值倍率，决定放行还是拒绝启动。
// 每个用例在一个会回滚的事务里写 settings，先清掉这几个键，免得别的集成测试留下的行干扰。
func TestInitCreditUnitAtStartup_ReadsSettingsTable(t *testing.T) {
	ctx := context.Background()

	run := func(t *testing.T, settings map[string]string, defaultRate float64) error {
		t.Helper()
		tx := testEntTx(t)
		client := tx.Client()
		_, err := client.Setting.Delete().Where(setting.KeyIn(service.CreditUnitStartupSettingKeys()...)).Exec(ctx)
		require.NoError(t, err)
		for k, v := range settings {
			require.NoError(t, client.Setting.Create().SetKey(k).SetValue(v).Exec(ctx))
		}
		cfg := &config.Config{}
		cfg.Default.RateMultiplier = defaultRate
		return initCreditUnitAtStartup(ctx, client, cfg)
	}

	t.Run("no_settings_means_usd", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		require.NoError(t, run(t, nil, 1))
		require.False(t, service.CurrentCreditUnit().IsCNY())
	})

	t.Run("cny_with_multiplier_one_starts", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		require.NoError(t, run(t, map[string]string{
			service.SettingCreditCurrency:      "CNY",
			service.SettingLegacyCreditDivisor: "13",
			service.SettingCNYCutoverAt:        "2026-10-12T21:40:00Z",
			service.SettingBalanceRechargeMult: "1",
		}, 0.0769))
		require.True(t, service.CurrentCreditUnit().IsCNY())
		require.EqualValues(t, 13, service.CurrentCreditUnit().LegacyDivisor)
		require.False(t, service.CurrentCreditUnit().CutoverAt.IsZero())
	})

	t.Run("cny_with_multiplier_13_refuses", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		err := run(t, map[string]string{
			service.SettingCreditCurrency:      "CNY",
			service.SettingBalanceRechargeMult: "13.00",
		}, 0.0769)
		require.Error(t, err)
		require.Contains(t, err.Error(), service.SettingBalanceRechargeMult)
		require.False(t, service.CurrentCreditUnit().IsCNY())
	})
}
