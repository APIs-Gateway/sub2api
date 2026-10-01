package repository

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func readMigrationForTest(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
	require.NoError(t, err)
	return string(b)
}

// 195：表结构按设计定稿——platform / source 列，同 source 内分组唯一（跨 source 允许重复）。
func TestAPIKeyGroupRoutesMigrationShape(t *testing.T) {
	sqlText := readMigrationForTest(t, "195_api_key_group_routes.sql")

	require.Contains(t, sqlText, "CREATE TABLE IF NOT EXISTS api_key_group_routes")
	require.Contains(t, sqlText, "platform    VARCHAR(32) NOT NULL")
	require.Contains(t, sqlText, "source      VARCHAR(16) NOT NULL DEFAULT 'user'")
	require.Contains(t, sqlText, "UNIQUE (api_key_id, source, group_id)")
	require.Contains(t, sqlText, "UNIQUE (api_key_id, platform, source, placement, position)")
	require.Contains(t, sqlText, "CHECK (placement = 'tail' OR source = 'admin')")
	require.Contains(t, sqlText, "REFERENCES api_keys(id) ON DELETE CASCADE")
	require.Contains(t, sqlText, "REFERENCES groups(id)   ON DELETE CASCADE")
	// 不得退回跨 source 唯一：那会让用户提交的分组撞隐藏链时暴露隐藏链。
	require.NotContains(t, sqlText, "UNIQUE (api_key_id, group_id)")
}

// 196：两列可空、无默认值、无外键；照 190 的写法不写 IF NOT EXISTS，并带 lock_timeout。
func TestUsageLogServedGroupMigrationShape(t *testing.T) {
	sqlText := readMigrationForTest(t, "196_usage_log_served_group.sql")

	require.Contains(t, sqlText, "SET LOCAL lock_timeout")
	require.Contains(t, sqlText, "ALTER TABLE usage_logs ADD COLUMN served_group_id BIGINT;")
	require.Contains(t, sqlText, "ALTER TABLE usage_logs ADD COLUMN served_route_source SMALLINT;")
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(line, "ALTER TABLE") {
			require.NotContains(t, line, "IF NOT EXISTS")
			require.NotContains(t, line, "DEFAULT")
			require.NotContains(t, line, "NOT NULL")
			require.NotContains(t, line, "REFERENCES")
		}
	}
}
