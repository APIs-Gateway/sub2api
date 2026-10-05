package repository

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// slogCapture 收集 slog 记录，用来断言启动检查的告警。
type slogCapture struct {
	mu      *sync.Mutex
	records *[]slog.Record
}

func newSlogCapture() slogCapture {
	return slogCapture{mu: &sync.Mutex{}, records: &[]slog.Record{}}
}

func (h slogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h slogCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, r)
	return nil
}
func (h slogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h slogCapture) WithGroup(string) slog.Handler      { return h }

func (h slogCapture) warnings() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range *h.records {
		if r.Level == slog.LevelWarn {
			out = append(out, r.Message)
		}
	}
	return out
}

func TestApplyCreditUnitStartup_FixesProcessUnit(t *testing.T) {
	t.Run("defaults_to_usd", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		require.NoError(t, applyCreditUnitStartup(nil, 1))
		require.False(t, service.CurrentCreditUnit().IsCNY())
		require.EqualValues(t, 1, service.CurrentCreditUnit().LegacyDivisor)
	})

	t.Run("codex_before_cutover_keeps_recharge_multiplier_13", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		require.NoError(t, applyCreditUnitStartup(map[string]string{
			service.SettingBalanceRechargeMult: "13.00",
			service.SettingLegacyCreditDivisor: "13",
		}, 1))
		require.False(t, service.CurrentCreditUnit().IsCNY())
		require.EqualValues(t, 13, service.CurrentCreditUnit().LegacyDivisor)
	})

	t.Run("cny_is_fixed_when_multiplier_is_one", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		require.NoError(t, applyCreditUnitStartup(map[string]string{
			service.SettingCreditCurrency:      "CNY",
			service.SettingLegacyCreditDivisor: "13",
			service.SettingBalanceRechargeMult: "1",
		}, 0.0769))
		require.True(t, service.CurrentCreditUnit().IsCNY())
		require.EqualValues(t, 13, service.CurrentCreditUnit().LegacyDivisor)
		require.NotNil(t, service.CurrentCreditUnit().CostUnit())
	})

	t.Run("cny_with_multiplier_13_refuses_and_leaves_unit_unfixed", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		err := applyCreditUnitStartup(map[string]string{
			service.SettingCreditCurrency:      "CNY",
			service.SettingBalanceRechargeMult: "13.00",
		}, 0.0769)
		require.Error(t, err)
		require.Contains(t, err.Error(), service.SettingBalanceRechargeMult)
		require.False(t, service.CurrentCreditUnit().IsCNY(), "拒绝启动时不能把进程固定成 CNY")
		// 状态没被污染：之后还能用合法配置初始化。
		require.NoError(t, applyCreditUnitStartup(map[string]string{service.SettingCreditCurrency: "CNY"}, 0.0769))
		require.True(t, service.CurrentCreditUnit().IsCNY())
	})

	t.Run("unit_cannot_change_within_a_process", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		require.NoError(t, applyCreditUnitStartup(map[string]string{service.SettingCreditCurrency: "CNY"}, 0.0769))
		// 同样的配置重复调用无副作用。
		require.NoError(t, applyCreditUnitStartup(map[string]string{service.SettingCreditCurrency: "CNY"}, 0.0769))
		// 不同的配置被拒绝，进程保持 CNY。
		require.Error(t, applyCreditUnitStartup(map[string]string{service.SettingCreditCurrency: "USD"}, 1))
		require.True(t, service.CurrentCreditUnit().IsCNY())
	})

	t.Run("invalid_settings_refuse_to_start", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		require.Error(t, applyCreditUnitStartup(map[string]string{service.SettingCreditCurrency: "EUR"}, 1))
		require.Error(t, applyCreditUnitStartup(map[string]string{service.SettingLegacyCreditDivisor: "0"}, 1))
		require.False(t, service.CurrentCreditUnit().IsCNY())
	})
}

func TestApplyCreditUnitStartup_WarnsOnLegacyDefaultRate(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	cny := map[string]string{service.SettingCreditCurrency: "CNY", service.SettingLegacyCreditDivisor: "13"}

	t.Run("default_rate_above_half_warns", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		capture := newSlogCapture()
		slog.SetDefault(slog.New(capture))
		require.NoError(t, applyCreditUnitStartup(cny, 1), "只告警，不拒绝启动")
		warns := capture.warnings()
		require.Len(t, warns, 1)
		require.True(t, strings.Contains(warns[0], "default.rate_multiplier"), warns[0])
	})

	t.Run("converted_default_rate_is_quiet", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		capture := newSlogCapture()
		slog.SetDefault(slog.New(capture))
		require.NoError(t, applyCreditUnitStartup(cny, 0.0769))
		require.Empty(t, capture.warnings())
	})

	t.Run("usd_mode_default_rate_one_is_quiet", func(t *testing.T) {
		t.Cleanup(service.ResetCreditUnitForTest())
		capture := newSlogCapture()
		slog.SetDefault(slog.New(capture))
		require.NoError(t, applyCreditUnitStartup(nil, 1))
		require.Empty(t, capture.warnings())
	})
}
