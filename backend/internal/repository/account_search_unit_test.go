//go:build unit

package repository

import (
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
)

func TestParseAccountSearchID(t *testing.T) {
	tests := []struct {
		search string
		wantID int64
		wantOK bool
	}{
		{"42", 42, true},
		{"0042", 42, true},
		{"9223372036854775807", 9223372036854775807, true},
		{"9223372036854775808", 0, false}, // 超出 int64
		{"0", 0, false},
		{"", 0, false},
		{"4 2", 0, false},
		{"42a", 0, false},
		{"-42", 0, false},
		{"4.2", 0, false},
		{"１２", 0, false}, // 全角数字不算数字 ID
	}
	for _, tt := range tests {
		t.Run(tt.search, func(t *testing.T) {
			id, ok := parseAccountSearchID(tt.search)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantID, id)
		})
	}
}

func buildAccountSearchSQL(search string) (string, []any) {
	selector := entsql.Dialect(dialect.Postgres).Select("*").From(entsql.Table("accounts"))
	predicate := accountSearchPredicate(search)
	if predicate == nil {
		return "", nil
	}
	predicate(selector)
	return selector.Query()
}

func TestAccountSearchPredicateBlankSearchAddsNoCondition(t *testing.T) {
	require.Nil(t, accountSearchPredicate(""))
	require.Nil(t, accountSearchPredicate("   "))
}

func TestAccountSearchPredicateCoversNameNotesBaseURL(t *testing.T) {
	query, args := buildAccountSearchSQL("relay.example.com")

	require.Contains(t, query, `"name"`)
	require.Contains(t, query, `"notes"`)
	require.Contains(t, query, `->>'base_url'`)
	require.Contains(t, query, "ILIKE")
	require.Equal(t, []any{"%relay.example.com%", "%relay.example.com%", "%relay.example.com%"}, args)
	require.NotContains(t, query, "relay.example.com", "the search term must be a bound parameter")
}

func TestAccountSearchPredicateAddsIDMatchOnlyForNumericSearch(t *testing.T) {
	_, args := buildAccountSearchSQL("42")
	require.Len(t, args, 4)
	require.Equal(t, int64(42), args[3])

	_, args = buildAccountSearchSQL("42x")
	require.Len(t, args, 3)
}

func TestAccountSearchPredicateEscapesLikeWildcards(t *testing.T) {
	query, args := buildAccountSearchSQL(`50%_off\x`)

	require.Len(t, args, 3)
	for _, arg := range args {
		require.Equal(t, `%50\%\_off\\x%`, arg)
	}
	require.NotContains(t, query, "50%")
}

func TestAccountSearchPredicateSQLiteFallback(t *testing.T) {
	selector := entsql.Dialect(dialect.SQLite).Select("*").From(entsql.Table("accounts"))
	accountBaseURLContainsFold("a_b")(selector)
	query, args := selector.Query()

	require.Contains(t, query, "json_extract")
	require.Contains(t, query, `ESCAPE '\'`)
	require.Equal(t, []any{`%a\_b%`}, args)
}
