//go:build unit

package repository

import (
 "context"
 "database/sql"
 "testing"
 "testing/fstest"

 "github.com/Wei-Shaw/sub2api/migrations"
 "github.com/stretchr/testify/require"
)

func TestSimpleModeImageEligibilityMigrationDialectAndSQLiteRevocation(t *testing.T) {
 for _,name:=range []string{"197_simple_mode_auto_image_eligibility.sql","197_simple_mode_auto_image_eligibility_mysql.sql","197_simple_mode_auto_image_eligibility_sqlite.sql"} {
  for _,dialect:=range []migrationDatabaseDialect{migrationDatabasePostgres,migrationDatabaseMySQL,migrationDatabaseSQLite} {
   want:=name=="197_simple_mode_auto_image_eligibility.sql"&&dialect==migrationDatabasePostgres || name=="197_simple_mode_auto_image_eligibility_mysql.sql"&&dialect==migrationDatabaseMySQL || name=="197_simple_mode_auto_image_eligibility_sqlite.sql"&&dialect==migrationDatabaseSQLite
   require.Equal(t,want,migrationAppliesToDatabase(name,dialect),name)
  }
 }
 db,err:=sql.Open("sqlite",":memory:");require.NoError(t,err);defer db.Close();db.SetMaxOpenConns(1)
 _,err=db.Exec(`CREATE TABLE groups (id INTEGER PRIMARY KEY,status TEXT,is_exclusive INTEGER,deleted_at TIMESTAMP,allow_image_generation INTEGER DEFAULT 0); CREATE TABLE api_keys (id INTEGER PRIMARY KEY,key TEXT,group_id INTEGER,deleted_at TIMESTAMP); CREATE TABLE auth_cache_invalidation_outbox (cache_key TEXT); INSERT INTO groups (id,status,is_exclusive) VALUES (1,'active',0); INSERT INTO api_keys(id,key,group_id) VALUES (1,'sk-current',1);`);require.NoError(t,err)
 data,err:=migrations.FS.ReadFile("197_simple_mode_auto_image_eligibility_sqlite.sql");require.NoError(t,err)
 fs:=fstest.MapFS{"197_simple_mode_auto_image_eligibility_sqlite.sql":&fstest.MapFile{Data:data}}
 require.NoError(t,applyMigrationsFS(context.Background(),db,fs));require.NoError(t,applyMigrationsFS(context.Background(),db,fs))
 var eligible int
 require.NoError(t,db.QueryRow("SELECT simple_mode_auto_image_eligible FROM groups WHERE id=1").Scan(&eligible));require.Zero(t,eligible)
 for _,stmt:=range []string{"UPDATE groups SET simple_mode_auto_image_eligible=1 WHERE id=1","UPDATE groups SET simple_mode_auto_image_eligible=0 WHERE id=1","UPDATE groups SET allow_image_generation=1 WHERE id=1","UPDATE groups SET allow_image_generation=0 WHERE id=1"} {
  _,err=db.Exec("DELETE FROM auth_cache_invalidation_outbox");require.NoError(t,err)
  _,err=db.Exec(stmt);require.NoError(t,err)
  var count int;require.NoError(t,db.QueryRow("SELECT COUNT(*) FROM auth_cache_invalidation_outbox").Scan(&count));require.Equal(t,1,count,stmt)
 }
 _,err=db.Exec("DELETE FROM auth_cache_invalidation_outbox; UPDATE groups SET status='active' WHERE id=1");require.NoError(t,err)
 var count int;require.NoError(t,db.QueryRow("SELECT COUNT(*) FROM auth_cache_invalidation_outbox").Scan(&count));require.Zero(t,count)
}
