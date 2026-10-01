package repository

import (
	"sort"
	"strconv"
	"strings"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

// accountSearchPredicate 构造账号列表搜索条件，命中以下任一项即可：
//   - 名称包含搜索词（不区分大小写）
//   - 备注包含搜索词（不区分大小写）
//   - credentials.base_url 包含搜索词（不区分大小写）
//   - 搜索词是纯数字时，账号 ID 等于该数字
//
// 搜索词为空（含纯空白）时返回 nil，表示不加搜索条件。
func accountSearchPredicate(search string) dbpredicate.Account {
	search = strings.TrimSpace(search)
	if search == "" {
		return nil
	}

	predicates := []dbpredicate.Account{
		dbaccount.NameContainsFold(search),
		dbaccount.NotesContainsFold(search),
		accountBaseURLContainsFold(search),
	}
	if id, ok := parseAccountSearchID(search); ok {
		predicates = append(predicates, dbaccount.IDEQ(id))
	}
	return dbaccount.Or(predicates...)
}

// parseAccountSearchID 仅当搜索词全是数字且能放进 int64 时返回账号 ID。
func parseAccountSearchID(search string) (int64, bool) {
	if search == "" {
		return 0, false
	}
	for _, r := range search {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(search, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// accountBaseURLContainsFold 匹配 credentials JSONB 里的 base_url（不区分大小写的子串匹配）。
// 搜索词作为绑定参数传入，并转义 LIKE 通配符（\ % _），不会被当作模式。
func accountBaseURLContainsFold(search string) dbpredicate.Account {
	pattern := "%" + escapeLikePattern(search) + "%"
	return dbpredicate.Account(func(s *entsql.Selector) {
		column := s.C(dbaccount.FieldCredentials)
		s.Where(entsql.P(func(b *entsql.Builder) {
			if b.Dialect() == dialect.Postgres {
				// Postgres 默认以反斜杠为转义符。
				b.WriteString("COALESCE(").WriteString(column).WriteString("->>'base_url', '') ILIKE ")
				b.Arg(pattern)
				return
			}
			// 其他方言（单元测试用的 SQLite）：LIKE 对 ASCII 不区分大小写，需显式声明转义符。
			b.WriteString("COALESCE(json_extract(").WriteString(column).WriteString(", '$.base_url'), '') LIKE ")
			b.Arg(pattern)
			b.WriteString(` ESCAPE '\'`)
		}))
	})
}

func sortedAccountStringSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
