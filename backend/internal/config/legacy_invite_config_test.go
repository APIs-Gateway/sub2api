package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLoadLegacyInviteFromEnvironment 钉死一次真实的线上故障。
//
// viper 的 AutomaticEnv 只对「已经注册过的 key」生效。legacy_invite 这一节最初漏了
// user / password / dbname 三个 SetDefault，于是这三个环境变量压根没被读进来，
// 拼出的 DSN 缺字段，lib/pq 把后一个键值对整个当成了用户名，
// 报出 `no pg_hba.conf entry for host "...", user "dbname="`。
//
// 只要有任何一个 key 从 setDefaults 里掉出去，这条用例就会红。
func TestLoadLegacyInviteFromEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("LEGACY_INVITE_ENABLED", "true")
	t.Setenv("LEGACY_INVITE_HOST", "10.1.2.3")
	t.Setenv("LEGACY_INVITE_PORT", "6543")
	t.Setenv("LEGACY_INVITE_USER", "legacy_ro")
	t.Setenv("LEGACY_INVITE_PASSWORD", "s3cret")
	t.Setenv("LEGACY_INVITE_DBNAME", "legacydb")
	t.Setenv("LEGACY_INVITE_SSLMODE", "disable")
	t.Setenv("LEGACY_INVITE_MIN_PAID_AMOUNT", "300")
	t.Setenv("LEGACY_INVITE_QUERY_TIMEOUT_SECONDS", "7")
	t.Setenv("LEGACY_INVITE_MAX_OPEN_CONNS", "3")

	cfg, err := Load()
	require.NoError(t, err)

	lc := cfg.LegacyInvite
	require.True(t, lc.Enabled)
	require.Equal(t, "10.1.2.3", lc.Host)
	require.Equal(t, 6543, lc.Port)
	require.Equal(t, "legacy_ro", lc.User)
	require.Equal(t, "s3cret", lc.Password)
	require.Equal(t, "legacydb", lc.DBName)
	require.Equal(t, "disable", lc.SSLMode)
	require.InDelta(t, 300, lc.MinPaidAmount, 0.001)
	require.Equal(t, 7, lc.QueryTimeoutSeconds)
	require.Equal(t, 3, lc.MaxOpenConns)

	// 六个字段必须都出现在 DSN 里：少一个就会让驱动错位解析后面的键值对
	dsn := lc.DSN()
	for _, want := range []string{
		"host=10.1.2.3", "port=6543", "user=legacy_ro",
		"password=s3cret", "dbname=legacydb", "sslmode=disable",
	} {
		require.Contains(t, dsn, want)
	}
}

// TestLegacyInviteDefaultSSLModeIsSupportedByDriver 防止默认值再被改成驱动不认的取值。
//
// lib/pq 只接受 disable / require / verify-ca / verify-full。libpq 自己的默认值 prefer
// 看起来最合理，照着 PostgreSQL 文档填也是它——但 lib/pq 会在 sql.Open 阶段直接报
// unsupported sslmode。这条断言把默认值锁在驱动认识的集合里。
func TestLegacyInviteDefaultSSLModeIsSupportedByDriver(t *testing.T) {
	resetViperWithJWTSecret(t)

	cfg, err := Load()
	require.NoError(t, err)
	require.Contains(t,
		[]string{"disable", "require", "verify-ca", "verify-full"},
		cfg.LegacyInvite.SSLMode,
		"legacy_invite.sslmode 的默认值必须是 lib/pq 能解析的",
	)
	// 默认关闭：单站部署不该因为这一节而去连任何东西
	require.False(t, cfg.LegacyInvite.Enabled)
}
