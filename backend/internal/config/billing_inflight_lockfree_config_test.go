//go:build unit

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 「计费免锁特权」改成按用户属性授权后，billing.inflight_reservation.lock_free_min_balance 已被删除。
// 线上 config.yaml 里仍写着 `lock_free_min_balance: 500000`（以及对应的环境变量可能仍在）：
// 配置必须照样能加载、不报错，其余字段保持默认。

const legacyLockFreeEnvKey = "BILLING_INFLIGHT_RESERVATION_LOCK_FREE_MIN_BALANCE"

func writeLegacyLockFreeConfigFile(t *testing.T, body string) {
	t.Helper()
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(body), 0o600))
	t.Setenv("CONFIG_FILE", configFile)
}

func requireInflightDefaultsKept(t *testing.T, r BillingInflightReservationConfig) {
	t.Helper()
	require.True(t, r.Enabled, "billing.inflight_reservation.enabled 应保持默认 true")
	require.Equal(t, 900, r.TTLSeconds, "ttl_seconds 应保持默认 900")
	require.Equal(t, 8192, r.DefaultMaxOutputTokens, "default_max_output_tokens 应保持默认 8192")
}

// 线上 free 站的现状：config.yaml 里有这一行。
func TestLoadIgnoresLegacyLockFreeMinBalanceInConfigFile(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(legacyLockFreeEnvKey, "")
	writeLegacyLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: 500000\n")

	cfg, err := Load()
	require.NoError(t, err)
	requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
}

// 旧配置里非数字、负数等任何写法也不再有校验（字段已不存在）。
func TestLoadIgnoresAnyLegacyLockFreeMinBalanceValue(t *testing.T) {
	for _, raw := range []string{"0", "-1", "1.5e6", "abc", "true", `"500000"`, "[1, 2]"} {
		t.Run(raw, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv(legacyLockFreeEnvKey, "")
			writeLegacyLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: "+raw+"\n")

			cfg, err := Load()
			require.NoError(t, err)
			requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
		})
	}
}

// 旧环境变量同样被忽略。
func TestLoadIgnoresLegacyLockFreeMinBalanceEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(legacyLockFreeEnvKey, "500000")

	cfg, err := Load()
	require.NoError(t, err)
	requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
}

// 旧 yaml 里同节的其它字段照常生效，不被遗留字段影响。
func TestLoadLegacyLockFreeMinBalanceDoesNotAffectSiblingFields(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(legacyLockFreeEnvKey, "")
	writeLegacyLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    enabled: false\n    ttl_seconds: 60\n    default_max_output_tokens: 1024\n    lock_free_min_balance: 500000\n")

	cfg, err := Load()
	require.NoError(t, err)
	r := cfg.Billing.InflightReservation
	require.False(t, r.Enabled)
	require.Equal(t, 60, r.TTLSeconds)
	require.Equal(t, 1024, r.DefaultMaxOutputTokens)
}
