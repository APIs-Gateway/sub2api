//go:build unit

package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 本文件覆盖 billing.inflight_reservation.lock_free_min_balance 的配置加载语义：
// 默认关闭（0）、config.yaml 与环境变量两种写法、同节其余字段不被清零、非法值行为。
// free 部署在 config.yaml 里设 500000；codex 不配置，必须保持 0（关闭）。

const lockFreeEnvKey = "BILLING_INFLIGHT_RESERVATION_LOCK_FREE_MIN_BALANCE"

func writeLockFreeConfigFile(t *testing.T, body string) {
	t.Helper()
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(body), 0o600))
	t.Setenv("CONFIG_FILE", configFile)
}

// 同节其余字段必须仍是默认值：写了 billing 节（或只写了 lock_free_min_balance）不能把它们清零。
func requireInflightDefaultsKept(t *testing.T, r BillingInflightReservationConfig) {
	t.Helper()
	require.True(t, r.Enabled, "billing.inflight_reservation.enabled 应保持默认 true")
	require.Equal(t, 900, r.TTLSeconds, "ttl_seconds 应保持默认 900")
	require.Equal(t, 8192, r.DefaultMaxOutputTokens, "default_max_output_tokens 应保持默认 8192")
}

func TestLoadLockFreeMinBalanceDefaultsToDisabled(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(lockFreeEnvKey, "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, float64(0), cfg.Billing.InflightReservation.LockFreeMinBalance)
	requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
}

// codex 站：config.yaml 有 billing 节但没有 lock_free_min_balance，仍须是 0。
func TestLoadLockFreeMinBalanceStaysZeroWhenBillingSectionOmitsIt(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(lockFreeEnvKey, "")
	writeLockFreeConfigFile(t, "billing:\n  circuit_breaker:\n    enabled: true\n    failure_threshold: 7\n")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, float64(0), cfg.Billing.InflightReservation.LockFreeMinBalance)
	requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
	require.Equal(t, 7, cfg.Billing.CircuitBreaker.FailureThreshold)
}

// free 站：config.yaml 目前没有 billing 节，新增后只写这一项，其余字段不能被清零。
func TestLoadLockFreeMinBalanceFromConfigFile(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(lockFreeEnvKey, "")
	writeLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: 500000\n")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, float64(500000), cfg.Billing.InflightReservation.LockFreeMinBalance)
	requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
	// billing 节下的兄弟配置同样不受影响。
	require.Equal(t, CircuitBreakerConfig{
		Enabled:             true,
		FailureThreshold:    5,
		ResetTimeoutSeconds: 30,
		HalfOpenRequests:    3,
	}, cfg.Billing.CircuitBreaker)
}

func TestLoadLockFreeMinBalanceConfigFileAcceptsDecimalAndExponent(t *testing.T) {
	cases := map[string]float64{
		"500000.5": 500000.5,
		"5e5":      500000,
		"0.01":     0.01,
		"0":        0,
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv(lockFreeEnvKey, "")
			writeLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: "+raw+"\n")

			cfg, err := Load()
			require.NoError(t, err)
			require.InDelta(t, want, cfg.Billing.InflightReservation.LockFreeMinBalance, 1e-9)
			requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
		})
	}
}

// 环境变量规则：AutomaticEnv + 把 "." 替换成 "_"，即 BILLING_INFLIGHT_RESERVATION_LOCK_FREE_MIN_BALANCE。
// 该 key 有 SetDefault，所以没有 config.yaml 时环境变量同样生效。
func TestLoadLockFreeMinBalanceFromEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(lockFreeEnvKey, "500000")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, float64(500000), cfg.Billing.InflightReservation.LockFreeMinBalance)
	requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
}

func TestLoadLockFreeMinBalanceEnvironmentOverridesConfigFile(t *testing.T) {
	resetViperWithJWTSecret(t)
	writeLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: 500000\n")
	t.Setenv(lockFreeEnvKey, "1000")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, float64(1000), cfg.Billing.InflightReservation.LockFreeMinBalance)
	requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
}

// 环境变量设为空串被 viper 当作未设置（未开 AllowEmptyEnv），不会把 config.yaml 的值覆盖成 0。
func TestLoadLockFreeMinBalanceEmptyEnvironmentKeepsConfigFileValue(t *testing.T) {
	resetViperWithJWTSecret(t)
	writeLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: 500000\n")
	t.Setenv(lockFreeEnvKey, "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, float64(500000), cfg.Billing.InflightReservation.LockFreeMinBalance)
}

// 其它 inflight 字段的环境变量仍然各自生效，不被新字段影响。
func TestLoadInflightReservationSiblingFieldsStillOverridable(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(lockFreeEnvKey, "500000")
	t.Setenv("BILLING_INFLIGHT_RESERVATION_ENABLED", "false")
	t.Setenv("BILLING_INFLIGHT_RESERVATION_TTL_SECONDS", "60")
	t.Setenv("BILLING_INFLIGHT_RESERVATION_DEFAULT_MAX_OUTPUT_TOKENS", "1024")

	cfg, err := Load()
	require.NoError(t, err)
	r := cfg.Billing.InflightReservation
	require.False(t, r.Enabled)
	require.Equal(t, 60, r.TTLSeconds)
	require.Equal(t, 1024, r.DefaultMaxOutputTokens)
	require.Equal(t, float64(500000), r.LockFreeMinBalance)
}

// 负数 / 0 / NaN / ±Inf 都能加载成功且 Validate 不报错：约定它们等价于「关闭」，
// 由消费方统一按 `threshold > 0 && balance >= threshold` 判断（NaN、负数、0 都不满足，+Inf 永远不满足）。
// 这里钉住加载层的行为，防止有人把它改成「负数报错」后导致配置写错就起不来服务。
func TestLoadLockFreeMinBalanceNonPositiveOrNonFiniteLoadsWithoutError(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   func(float64) bool
	}{
		{"zero", "0", func(v float64) bool { return v == 0 }},
		{"negative", "-1", func(v float64) bool { return v == -1 }},
		{"negative_decimal", "-0.5", func(v float64) bool { return v == -0.5 }},
		{"nan", ".nan", math.IsNaN},
		{"pos_inf", ".inf", func(v float64) bool { return math.IsInf(v, 1) }},
		{"neg_inf", "-.inf", func(v float64) bool { return math.IsInf(v, -1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv(lockFreeEnvKey, "")
			writeLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: "+tc.raw+"\n")

			cfg, err := Load()
			require.NoError(t, err)
			require.True(t, tc.ok(cfg.Billing.InflightReservation.LockFreeMinBalance),
				"unexpected value %v", cfg.Billing.InflightReservation.LockFreeMinBalance)
			requireInflightDefaultsKept(t, cfg.Billing.InflightReservation)
		})
	}
}

func TestLoadLockFreeMinBalanceNegativeFromEnvironmentLoadsWithoutError(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv(lockFreeEnvKey, "-100")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, float64(-100), cfg.Billing.InflightReservation.LockFreeMinBalance)
}

// 字符串（无法解析成数字）在 unmarshal 阶段直接报错，服务启动失败而不是静默当成 0：
// 配置写错时宁可起不来，也不要悄悄把「余额够就免锁」关掉或误开。
func TestLoadLockFreeMinBalanceRejectsNonNumericConfigFileValue(t *testing.T) {
	for _, raw := range []string{"abc", `"500k"`, "[1, 2]"} {
		t.Run(raw, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv(lockFreeEnvKey, "")
			writeLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: "+raw+"\n")

			cfg, err := Load()
			require.Error(t, err)
			require.Nil(t, cfg)
			require.Contains(t, err.Error(), "lock_free_min_balance")
		})
	}
}

func TestLoadLockFreeMinBalanceRejectsNonNumericEnvironmentValue(t *testing.T) {
	for _, raw := range []string{"abc", "500k", "5,00,000"} {
		t.Run(raw, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv(lockFreeEnvKey, raw)

			cfg, err := Load()
			require.Error(t, err)
			require.Nil(t, cfg)
			require.Contains(t, err.Error(), "lock_free_min_balance")
		})
	}
}

// 已知特性（viper 弱类型解码）：YAML 布尔值 true 会被解成 1，false 解成 0，不报错。
// `lock_free_min_balance: true` 因此等价于阈值 1（余额 >= 1 的用户免锁），运维写错时要留意；
// 这里只钉住现状，不是鼓励这种写法。
func TestLoadLockFreeMinBalanceBooleanIsWeaklyDecodedToNumber(t *testing.T) {
	for raw, want := range map[string]float64{"true": 1, "false": 0} {
		t.Run(raw, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv(lockFreeEnvKey, "")
			writeLockFreeConfigFile(t, "billing:\n  inflight_reservation:\n    lock_free_min_balance: "+raw+"\n")

			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, want, cfg.Billing.InflightReservation.LockFreeMinBalance)
		})
	}
}
