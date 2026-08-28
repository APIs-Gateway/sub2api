//go:build unit

package service

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 合法的 quota 平台白名单在仓库里存在三份副本：本包的 AllowedQuotaPlatforms（权威源）、
// ent schema 里 platform 字段的 Validate（构建期约束）、以及数据库迁移里的 CHECK 约束。
//
// 2026-08-28 线上事故就是它们跑偏造成的：前两处都加了 grok，只有建表迁移的 CHECK 漏了。
// 后果不是「grok 配额存不进去」这么轻——后台校验用的是 AllowedQuotaPlatforms，所以 grok
// 能被写进 default_platform_quotas，注册时再去插这条快照就会违反 CHECK；而这条插入跑在
// 注册事务内，PostgreSQL 会把整个事务标记为 aborted，于是「初始化返利档案」「绑定邀请人」
// 一并失败，GitHub OAuth 注册直接 500，邀请奖励一次都没发出去。
//
// 这个测试把三份副本钉在一起，让下次新增平台时漏改任何一处都在 CI 里立刻暴露，
// 而不是等到线上注册挂掉才发现。
func TestQuotaPlatformWhitelistStaysInSync(t *testing.T) {
	t.Parallel()

	want := append([]string(nil), AllowedQuotaPlatforms...)
	sort.Strings(want)
	require.NotEmpty(t, want, "AllowedQuotaPlatforms 是权威源，不该为空")

	t.Run("迁移里的 CHECK 约束", func(t *testing.T) {
		got := sortedStrings(platformsFromMigrationCheck(t))
		require.Equal(t, want, got,
			"数据库 CHECK 约束与 AllowedQuotaPlatforms 不一致；新增平台时必须同时补一条 ALTER CONSTRAINT 迁移")
	})

	t.Run("ent schema 的 Validate", func(t *testing.T) {
		got := sortedStrings(platformsFromEntSchema(t))
		require.Equal(t, want, got,
			"ent schema 的 platform Validate 与 AllowedQuotaPlatforms 不一致")
	})
}

// platformsFromMigrationCheck 取当前生效的 CHECK 约束定义。迁移是追加式的，后来的
// ALTER 会覆盖建表时的约束，所以这里取编号最大的那份定义，而不是第一处匹配。
func platformsFromMigrationCheck(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir("../../migrations")
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(migrationNames(names))

	pattern := regexp.MustCompile(`(?is)CHECK\s*\(\s*platform\s+IN\s*\(([^)]*)\)`)
	var latest []string
	for _, name := range names {
		raw, err := os.ReadFile("../../migrations/" + name)
		require.NoError(t, err)
		if m := pattern.FindAllStringSubmatch(string(raw), -1); len(m) > 0 {
			latest = splitQuotedList(m[len(m)-1][1])
		}
	}
	require.NotEmpty(t, latest, "没有在任何迁移里找到 user_platform_quotas 的 platform CHECK 约束")
	return latest
}

func platformsFromEntSchema(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile("../../ent/schema/user_platform_quota.go")
	require.NoError(t, err)

	// Validate 的实现是一条 switch case，把允许的平台并排列在同一行。
	pattern := regexp.MustCompile(`(?m)^\s*case\s+("[a-z]+"(?:\s*,\s*"[a-z]+")*)\s*:`)
	m := pattern.FindStringSubmatch(string(raw))
	require.NotNil(t, m, "没有在 ent schema 里找到 platform 白名单的 switch case")
	return splitQuotedList(m[1])
}

// splitQuotedList 把 `'a', 'b'` / `"a", "b"` 这类列表拆成裸字符串。
func splitQuotedList(s string) []string {
	out := make([]string, 0, 8)
	for _, part := range strings.Split(s, ",") {
		v := strings.Trim(strings.TrimSpace(part), `'"`)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// migrationNames 按迁移文件的数字前缀排序，避免字典序把 99 排到 142 后面。
type migrationNames []string

func (m migrationNames) Len() int      { return len(m) }
func (m migrationNames) Swap(i, j int) { m[i], m[j] = m[j], m[i] }
func (m migrationNames) Less(i, j int) bool {
	return migrationSeq(m[i]) < migrationSeq(m[j])
}

func migrationSeq(name string) int {
	n := 0
	for _, r := range name {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
