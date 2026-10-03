//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
)

func TestSimpleModeImageEligibilityMySQL84MigrationAndInvalidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithDatabase("image_permission"), tcmysql.WithUsername("root"), tcmysql.WithPassword("images"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "parseTime=true")
	require.NoError(t, err)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.PingContext(ctx))
	for _, stmt := range []string{
		"CREATE TABLE `groups` (id BIGINT PRIMARY KEY,name VARCHAR(100),status VARCHAR(20),is_exclusive BOOLEAN NOT NULL DEFAULT FALSE,deleted_at TIMESTAMP NULL,allow_image_generation BOOLEAN NOT NULL DEFAULT FALSE)",
		"CREATE TABLE api_keys (id BIGINT PRIMARY KEY,`key` VARCHAR(255),group_id BIGINT,deleted_at TIMESTAMP NULL)",
		"CREATE TABLE auth_cache_invalidation_outbox (id BIGINT AUTO_INCREMENT PRIMARY KEY,cache_key CHAR(64) NOT NULL)",
		"INSERT INTO `groups` (id,name,status) VALUES (1,'old-default','active')",
		"INSERT INTO api_keys (id,`key`,group_id) VALUES (1,'sk-image-mysql',1)",
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
	// Existing 184 has an unquoted reserved table identifier. Keep its immutable
	// bytes and demonstrate that baseline limitation before executing new 197.
	old, err := migrations.FS.ReadFile("184_auth_cache_invalidation_outbox_mysql.sql")
	require.NoError(t, err)
	start := strings.Index(string(old), "CREATE TRIGGER trg_groups_auth_cache_invalidation\n")
	require.NotEqual(t, -1, start)
	trigger := string(old)[start:]
	trigger = trigger[:strings.Index(trigger, ";")+1]
	_, err = db.ExecContext(ctx, trigger)
	var syntax *mysql.MySQLError
	require.ErrorAs(t, err, &syntax)
	require.Equal(t, uint16(1064), syntax.Number)
	data, err := migrations.FS.ReadFile("197_simple_mode_auto_image_eligibility_mysql.sql")
	require.NoError(t, err)
	fs := fstest.MapFS{"197_simple_mode_auto_image_eligibility_mysql.sql": &fstest.MapFile{Data: data}}
	require.NoError(t, applyMigrationsFS(ctx, db, fs))
	require.NoError(t, applyMigrationsFS(ctx, db, fs))
	var eligible bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT simple_mode_auto_image_eligible FROM `groups` WHERE id=1").Scan(&eligible))
	require.False(t, eligible)
	for _, assignment := range []string{"simple_mode_auto_image_eligible=TRUE", "simple_mode_auto_image_eligible=FALSE", "allow_image_generation=TRUE", "allow_image_generation=FALSE"} {
		_, err = db.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox")
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, "UPDATE `groups` SET "+assignment+" WHERE id=1")
		require.NoError(t, err)
		var count int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key=LOWER(SHA2('sk-image-mysql',256))").Scan(&count))
		require.Equal(t, 1, count, assignment)
	}
	_, err = db.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE `groups` SET name='renamed' WHERE id=1")
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_cache_invalidation_outbox").Scan(&count))
	require.Zero(t, count)
	_, err = db.ExecContext(ctx, "DELETE FROM `groups` WHERE id=1")
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_cache_invalidation_outbox").Scan(&count))
	require.Equal(t, 1, count)
}
