package repository

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var w6Migrations = []string{
	"200_w6_model_catalog.sql",
	"201_w6_group_model_config_and_prices.sql",
	"202_w6_cost_accounting_rules.sql",
}

// sqlWithoutComments 去掉注释行后的语句文本。
func sqlWithoutComments(sqlText string) string {
	var kept []string
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// W6-M1..M3 只建新表：不改、不删任何现有对象，不写数据，每个文件都带 lock_timeout，
// 并且不使用 CONCURRENTLY（迁移在事务里执行）。
func TestW6MigrationsOnlyCreateNewObjects(t *testing.T) {
	forbidden := regexp.MustCompile(`(?i)\b(ALTER\s+TABLE|DROP\s|TRUNCATE|INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM|CONCURRENTLY)\b`)
	for _, name := range w6Migrations {
		t.Run(name, func(t *testing.T) {
			text := readMigrationForTest(t, name)
			code := sqlWithoutComments(text)
			require.Contains(t, code, "SET LOCAL lock_timeout")
			require.Contains(t, code, "SET LOCAL statement_timeout")
			require.Empty(t, forbidden.FindString(code), "迁移只能建新表和新索引")
			for _, line := range strings.Split(code, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "CREATE TABLE") {
					require.Contains(t, trimmed, "IF NOT EXISTS")
				}
				if strings.HasPrefix(trimmed, "CREATE INDEX") {
					require.Contains(t, trimmed, "IF NOT EXISTS")
				}
			}
		})
	}
}

func TestW6MigrationNumbersAreUniqueAndConsecutive(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	byNumber := map[string][]string{}
	for _, e := range entries {
		name := e.Name()
		if i := strings.Index(name, "_"); i > 0 {
			byNumber[name[:i]] = append(byNumber[name[:i]], name)
		}
	}
	for _, prefix := range []string{"200", "201", "202"} {
		require.Len(t, byNumber[prefix], 1, "迁移号 %s 只能有一个文件：%v", prefix, byNumber[prefix])
	}
}

func TestW6ModelCatalogMigrationShape(t *testing.T) {
	sqlText := readMigrationForTest(t, "200_w6_model_catalog.sql")
	require.Contains(t, sqlText, "CREATE TABLE IF NOT EXISTS model_catalog")
	require.Contains(t, sqlText, "CONSTRAINT model_catalog_uq UNIQUE (platform, model_key)")
	require.Contains(t, sqlText, "CHECK (status IN ('draft', 'active', 'retired'))")
	require.Contains(t, sqlText, "aliases          TEXT[]       NOT NULL DEFAULT '{}'")
	require.NotContains(t, sqlWithoutComments(sqlText), "REFERENCES", "不建外键")
}

func TestW6MatrixMigrationShape(t *testing.T) {
	sqlText := readMigrationForTest(t, "201_w6_group_model_config_and_prices.sql")
	code := sqlWithoutComments(sqlText)

	// group_model_config：billing_model_source 可空（S-1）；阶段与成本模式的取值范围。
	require.Contains(t, code, "CREATE TABLE IF NOT EXISTS group_model_config")
	require.Contains(t, code, "billing_model_source  VARCHAR(16),")
	require.NotRegexp(t, `billing_model_source\s+VARCHAR\(16\)\s+NOT NULL`, code)
	require.Contains(t, code, "pricing_stage         VARCHAR(8)  NOT NULL DEFAULT 'legacy'")
	require.Contains(t, code, "CHECK (pricing_stage IN ('legacy', 'shadow', 'v2'))")
	require.Contains(t, code, "CHECK (cost_mode IN ('account_rate', 'catalog_upstream', 'follow_billing'))")
	require.Contains(t, code, "CHECK (access_mode IN ('open', 'allowlist'))")

	// model_group_prices：三种价格模式互斥，(group_id, model_key, is_pattern) 唯一。
	require.Contains(t, code, "CREATE TABLE IF NOT EXISTS model_group_prices")
	require.Contains(t, code, "CHECK (price_mode IN ('inherit', 'extra', 'custom'))")
	require.Contains(t, code, "CHECK ((price_mode = 'extra') = (extra_multiplier IS NOT NULL)")
	require.Contains(t, code, "CHECK ((price_mode = 'custom') = (custom_price IS NOT NULL))")
	require.Contains(t, code, "UNIQUE (group_id, model_key, is_pattern)")
	require.Contains(t, code, "CHECK (source IN ('manual', 'copied', 'legacy_derived', 'legacy_frozen'))")

	// 历史表只建不写。
	require.Contains(t, code, "CREATE TABLE IF NOT EXISTS model_group_price_history")

	// groups 是软删除：三张表都不建外键。
	require.NotContains(t, code, "REFERENCES")
}

func TestW6CostAccountingRulesMigrationShape(t *testing.T) {
	sqlText := readMigrationForTest(t, "202_w6_cost_accounting_rules.sql")
	code := sqlWithoutComments(sqlText)

	// R2-BK-1：按分组各存一份，带来源渠道与顺序。
	require.Contains(t, code, "CREATE TABLE IF NOT EXISTS cost_accounting_rules")
	require.Contains(t, code, "scope_group_id     BIGINT       NOT NULL")
	require.Contains(t, code, "source_channel_id  BIGINT,")
	require.Contains(t, code, "source_ordinal     INT,")
	require.Contains(t, code, "CHECK (source IN ('legacy_derived', 'legacy_frozen', 'manual'))")
	require.Contains(t, code, "CHECK (source = 'manual' OR (source_channel_id IS NOT NULL AND source_ordinal IS NOT NULL))")

	// 价格行随规则行级联删除；platform 宽度与渠道侧一致。
	require.Contains(t, code, "REFERENCES cost_accounting_rules(id) ON DELETE CASCADE")
	require.Contains(t, code, "platform  VARCHAR(50) NOT NULL DEFAULT ''")
	require.Equal(t, 1, strings.Count(code, "REFERENCES"), "只有价格行引用规则行")
}
