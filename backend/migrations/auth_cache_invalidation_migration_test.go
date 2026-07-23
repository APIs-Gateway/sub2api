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

func TestAuthCacheInvalidationMigrationProvidesMySQLAndSQLiteVariants(t *testing.T) {
	mysqlContent, err := FS.ReadFile("184_auth_cache_invalidation_outbox_mysql.sql")
	require.NoError(t, err)
	mysqlSQL := strings.ToLower(string(mysqlContent))
	require.Contains(t, mysqlSQL, "sha2(")
	require.NotContains(t, mysqlSQL, "plpgsql")
	require.NotContains(t, mysqlSQL, "is distinct from")
	require.NotContains(t, mysqlSQL, "bigserial")
	require.NotContains(t, mysqlSQL, "timestamptz")
	require.NotContains(t, mysqlSQL, "skip locked")

	sqliteContent, err := FS.ReadFile("184_auth_cache_invalidation_outbox_sqlite.sql")
	require.NoError(t, err)
	sqliteSQL := strings.ToLower(string(sqliteContent))
	require.Contains(t, sqliteSQL, "sha256(")
	require.NotContains(t, sqliteSQL, "plpgsql")
	require.NotContains(t, sqliteSQL, "is distinct from")
	require.NotContains(t, sqliteSQL, "bigserial")
	require.NotContains(t, sqliteSQL, "timestamptz")
	require.NotContains(t, sqliteSQL, "for update skip locked")
}

func TestGroupAuthCacheImageGenerationMigrationProvidesPortableVariants(t *testing.T) {
	postgresContent, err := FS.ReadFile("185_group_auth_cache_image_generation.sql")
	require.NoError(t, err)
	postgresSQL := strings.ToLower(string(postgresContent))
	require.Contains(t, postgresSQL, "old.allow_image_generation is not distinct from new.allow_image_generation")
	require.Contains(t, postgresSQL, "create or replace function enqueue_group_auth_cache_invalidation")

	mysqlContent, err := FS.ReadFile("185_group_auth_cache_image_generation_mysql.sql")
	require.NoError(t, err)
	mysqlSQL := strings.ToLower(string(mysqlContent))
	require.Contains(t, mysqlSQL, "old.allow_image_generation <=> new.allow_image_generation")
	require.NotContains(t, mysqlSQL, "plpgsql")

	sqliteContent, err := FS.ReadFile("185_group_auth_cache_image_generation_sqlite.sql")
	require.NoError(t, err)
	sqliteSQL := strings.ToLower(string(sqliteContent))
	require.Contains(t, sqliteSQL, "old.allow_image_generation is not new.allow_image_generation")
	require.NotContains(t, sqliteSQL, "plpgsql")
}
