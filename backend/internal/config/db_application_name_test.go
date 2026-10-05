package config

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var dsnApplicationNameRE = regexp.MustCompile(`(?:^| )application_name=(\S+)(?: |$)`)

func dsnApplicationName(t *testing.T, dsn string) string {
	t.Helper()
	m := dsnApplicationNameRE.FindStringSubmatch(dsn)
	require.NotNil(t, m, "DSN 里没有 application_name: %q", dsn)
	return m[1]
}

// 主库连接带固定的 application_name；名字不含主机、用户、库名、密码，也不含时区。
func TestDatabaseDSN_ApplicationName(t *testing.T) {
	d := &DatabaseConfig{
		Host:     "db-host.internal.example",
		Port:     5432,
		User:     "svc_user",
		Password: "fake-pw-one",
		DBName:   "sub2api_prod",
		SSLMode:  "disable",
	}

	for name, dsn := range map[string]string{
		"with_password":    d.DSNWithTimezone("UTC"),
		"default_timezone": d.DSNWithTimezone(""),
		"explicit_app":     d.DSNWithTimezoneAndApplicationName("UTC", DBApplicationName),
	} {
		t.Run(name, func(t *testing.T) {
			got := dsnApplicationName(t, dsn)
			require.Equal(t, DBApplicationName, got)
			for _, secret := range []string{d.Host, d.User, d.Password, d.DBName, "UTC", "Shanghai"} {
				require.False(t, strings.Contains(got, secret), "application_name 不能带 %q", secret)
			}
			// 原有参数一个不少。
			require.Contains(t, dsn, "host=db-host.internal.example")
			require.Contains(t, dsn, "password=fake-pw-one")
			require.Contains(t, dsn, "dbname=sub2api_prod")
			require.Contains(t, dsn, "TimeZone=")
		})
	}

	// 密码为空时不出现 password，application_name 仍在。
	noPw := *d
	noPw.Password = ""
	dsn := noPw.DSNWithTimezone("UTC")
	require.NotContains(t, dsn, "password=")
	require.Equal(t, DBApplicationName, dsnApplicationName(t, dsn))
}

func TestDatabaseDSN_ApplicationNameVariants(t *testing.T) {
	d := &DatabaseConfig{Host: "h", Port: 5432, User: "u", Password: "p", DBName: "db", SSLMode: "disable"}

	require.Equal(t, DBApplicationNameCLI, dsnApplicationName(t, d.DSNWithTimezoneAndApplicationName("UTC", DBApplicationNameCLI)))

	// 三种连接的名字互不相同，且都以 sub2api- 开头（运维按前缀筛应用连接）。
	names := []string{DBApplicationName, DBApplicationNameCLI, DBApplicationNameLegacyInvite}
	seen := map[string]bool{}
	for _, n := range names {
		require.True(t, strings.HasPrefix(n, "sub2api-"), n)
		require.False(t, seen[n], "重名: %s", n)
		seen[n] = true
		require.LessOrEqual(t, len(n), 63, "PostgreSQL 会截断超过 63 字节的 application_name")
	}
}

// 不合规的名字不能破坏 key=value 形式的 DSN（空格会让后面的键值错位）。
func TestDatabaseDSN_ApplicationNameIsSanitized(t *testing.T) {
	d := &DatabaseConfig{Host: "h", Port: 5432, User: "u", Password: "p", DBName: "db", SSLMode: "disable"}

	dsn := d.DSNWithTimezoneAndApplicationName("UTC", "evil name' host=attacker")
	got := dsnApplicationName(t, dsn)
	require.Equal(t, "evil_name__host_attacker", got)
	require.NotContains(t, dsn, "host=attacker")

	require.Equal(t, DBApplicationName, dsnApplicationName(t, d.DSNWithTimezoneAndApplicationName("UTC", "")))
	require.Equal(t, "ok.name_1-2", sanitizeDBApplicationName("ok.name_1-2"))
}

func TestLegacyInviteDSN_ApplicationName(t *testing.T) {
	c := &LegacyInviteConfig{Host: "10.1.2.3", Port: 6543, User: "legacy_ro", Password: "fake-pw-two", DBName: "legacydb", SSLMode: "disable"}

	dsn := c.DSN()
	got := dsnApplicationName(t, dsn)
	require.Equal(t, DBApplicationNameLegacyInvite, got)
	for _, secret := range []string{c.Host, c.User, c.Password, c.DBName} {
		require.False(t, strings.Contains(got, secret), "application_name 不能带 %q", secret)
	}
	require.NotEqual(t, DBApplicationName, got, "旧站只读连接要能和真正的应用连接区分开")

	c.Password = ""
	require.NotContains(t, c.DSN(), "password=")
	require.Equal(t, DBApplicationNameLegacyInvite, dsnApplicationName(t, c.DSN()))
}
