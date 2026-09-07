package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsIngressRejectAggregatesMigrationStoresOnlyMaskedAggregates(t *testing.T) {
	content, err := FS.ReadFile("183_ops_ingress_reject_aggregates.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS ops_ingress_reject_aggregates")
	require.Contains(t, sql, "request_count  BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "ops_ingress_reject_aggregates_dimensions_unique UNIQUE")
	require.Contains(t, sql, "idx_ops_ingress_reject_aggregates_bucket")
	require.Contains(t, sql, "idx_ops_ingress_reject_aggregates_reason_bucket")
	require.Contains(t, sql, "idx_ops_ingress_reject_aggregates_ip_bucket")

	// Aggregation-only guarantee: no columns for raw request bodies, headers or
	// credentials should ever be introduced into this rollup table. Only the
	// non-comment SQL statements are checked, matching the pattern used by
	// 181_prompt_audit.sql's migration test: the explanatory comment above
	// intentionally documents this guarantee in prose (using the word
	// "headers"), which must not itself trip the forbidden-keyword check.
	lower := strings.ToLower(sql)
	nonCommentLines := make([]string, 0)
	for _, line := range strings.Split(lower, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			nonCommentLines = append(nonCommentLines, line)
		}
	}
	nonCommentSQL := strings.Join(nonCommentLines, "\n")
	for _, forbidden := range []string{"request_body", "header", "authorization", "credential", "raw_ip", "user_agent"} {
		require.NotContains(t, nonCommentSQL, forbidden)
	}
}

func TestOpsIngressRejectAggregatesMigrationProvidesMySQLAndSQLiteVariants(t *testing.T) {
	mysqlContent, err := FS.ReadFile("183_ops_ingress_reject_aggregates_mysql.sql")
	require.NoError(t, err)
	mysqlSQL := strings.ToLower(string(mysqlContent))
	require.Contains(t, mysqlSQL, "auto_increment")
	require.Contains(t, mysqlSQL, "unique key uk_ops_ingress_reject_aggregates_dimensions")
	require.NotContains(t, mysqlSQL, "bigserial")
	require.NotContains(t, mysqlSQL, "timestamptz")
	require.NotContains(t, mysqlSQL, "inet")

	sqliteContent, err := FS.ReadFile("183_ops_ingress_reject_aggregates_sqlite.sql")
	require.NoError(t, err)
	sqliteSQL := strings.ToLower(string(sqliteContent))
	require.Contains(t, sqliteSQL, "integer primary key autoincrement")
	require.Contains(t, sqliteSQL, "unique (bucket_start")
	require.NotContains(t, sqliteSQL, "bigserial")
	require.NotContains(t, sqliteSQL, "timestamptz")
	require.NotContains(t, sqliteSQL, "inet")
}

func TestOpsIngressRejectAggregatesMigrationFileNamingIsIdempotentAcrossDialects(t *testing.T) {
	for _, name := range []string{
		"183_ops_ingress_reject_aggregates.sql",
		"183_ops_ingress_reject_aggregates_mysql.sql",
		"183_ops_ingress_reject_aggregates_sqlite.sql",
	} {
		content, err := FS.ReadFile(name)
		require.NoError(t, err, name)
		require.Contains(t, string(content), "CREATE TABLE IF NOT EXISTS ops_ingress_reject_aggregates", name)
		require.Contains(t, string(content), "IF NOT EXISTS", name)
	}
}
