package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthCacheInvalidationMigrationUsesHashedKeysAndDurableTriggers(t *testing.T) {
	content, err := FS.ReadFile("184_auth_cache_invalidation_outbox.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS auth_cache_invalidation_outbox")
	require.Contains(t, sql, "cache_key      CHAR(64)")
	require.Contains(t, sql, "sha256(convert_to(raw_key, 'UTF8'))")
	require.Contains(t, sql, "trg_api_keys_auth_cache_invalidation")
	require.Contains(t, sql, "trg_users_auth_cache_invalidation")
	require.Contains(t, sql, "trg_groups_auth_cache_invalidation")
	require.Contains(t, sql, "trg_user_allowed_groups_auth_cache_invalidation")
	require.Contains(t, sql, "FOR EACH ROW EXECUTE FUNCTION")
	require.NotContains(t, strings.ToLower(sql), "insert into auth_cache_invalidation_outbox (raw_key)")
}
