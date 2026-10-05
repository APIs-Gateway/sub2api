package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 203：usage_logs.cost_unit 可空、无默认值、无约束（否则 PG 会重写 9GB 的表），幂等，带 lock_timeout。
func TestUsageLogCostUnitMigrationShape(t *testing.T) {
	sqlText := readMigrationForTest(t, "203_usage_log_cost_unit.sql")

	require.Contains(t, sqlText, "SET LOCAL lock_timeout")
	require.Contains(t, sqlText, "SET LOCAL statement_timeout")
	require.Contains(t, sqlText, "ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cost_unit SMALLINT;")

	stmts := 0
	for _, line := range strings.Split(sqlText, "\n") {
		if !strings.HasPrefix(line, "ALTER TABLE") {
			continue
		}
		stmts++
		require.NotContains(t, line, "DEFAULT", "带默认值会触发整表重写")
		require.NotContains(t, line, "NOT NULL", "NOT NULL 需要默认值或回填，会触发整表重写")
		require.NotContains(t, line, "REFERENCES")
		require.NotContains(t, line, "CHECK")
	}
	require.Equal(t, 1, stmts, "这个迁移只该有一条 ALTER，不要顺手加别的")
	for _, forbidden := range []string{"UPDATE ", "CREATE INDEX", "DROP "} {
		require.NotContains(t, sqlText, forbidden)
	}
}
