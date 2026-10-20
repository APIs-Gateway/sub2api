//go:build integration

package repository

import (
	"context"
	"database/sql"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 连接真的带上了 application_name：从服务端 pg_stat_activity 里能看到，运维据此在停服窗口里
// 区分应用连接和其它连接。用测试库的真实地址拼出 DatabaseConfig，走和生产一样的 DSN 构造方法。
func TestDatabaseDSN_ApplicationNameVisibleInPgStatActivity(t *testing.T) {
	ctx := context.Background()

	u, err := url.Parse(integrationPostgresDSN)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	password, _ := u.User.Password()
	dbCfg := config.DatabaseConfig{
		Host:     u.Hostname(),
		Port:     port,
		User:     u.User.Username(),
		Password: password,
		DBName:   strings.TrimPrefix(u.Path, "/"),
		SSLMode:  "disable",
	}
	legacy := config.LegacyInviteConfig{
		Host: dbCfg.Host, Port: dbCfg.Port, User: dbCfg.User, Password: dbCfg.Password, DBName: dbCfg.DBName, SSLMode: "disable",
	}

	cases := map[string]struct {
		dsn  string
		want string
	}{
		"app":           {dbCfg.DSNWithTimezone("UTC"), config.DBApplicationName},
		"cli":           {dbCfg.DSNWithTimezoneAndApplicationName("UTC", config.DBApplicationNameCLI), config.DBApplicationNameCLI},
		"legacy_invite": {legacy.DSN(), config.DBApplicationNameLegacyInvite},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("postgres", tc.dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)

			var own string
			var pid int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT current_setting('application_name'), pg_backend_pid()`).Scan(&own, &pid))
			require.Equal(t, tc.want, own)

			// 换一条连接（测试库的全局连接）从 pg_stat_activity 看对方。
			var seen string
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT application_name FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&seen))
			require.Equal(t, tc.want, seen)
		})
	}
}
