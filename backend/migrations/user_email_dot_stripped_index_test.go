package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserEmailDotStrippedIndexMigrationsMatchAliasLookupExpression(t *testing.T) {
	const expression = "REPLACE(LOWER(TRIM(email)), '.', '')"

	for _, name := range []string{
		"191_add_users_email_dot_stripped_index_notx.sql",
		"191_add_users_email_dot_stripped_index_sqlite.sql",
	} {
		content, err := FS.ReadFile(name)
		require.NoError(t, err)

		sql := strings.Join(strings.Fields(string(content)), " ")
		require.Contains(t, sql, "idx_users_email_dot_stripped")
		require.Contains(t, sql, expression)
	}

	postgresContent, err := FS.ReadFile("191_add_users_email_dot_stripped_index_notx.sql")
	require.NoError(t, err)
	require.Contains(t, string(postgresContent), "CREATE INDEX CONCURRENTLY IF NOT EXISTS")
}
