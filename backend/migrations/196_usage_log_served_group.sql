-- 196_usage_log_served_group.sql
-- Key 级分组回退链：记录实际服务本次请求的分组及其来源。
--   served_group_id     = 实际服务的分组；仅当 served != 主分组时写入，NULL = 主分组/未回退。
--                         usage_logs.group_id 语义不变，仍是主分组。
--   served_route_source = 1 用户自己配置的链，2 管理员隐藏链；NULL = 主分组。
--                         用户端只在 1 时透出 served 分组，2 一律按主分组展示。
-- 两列均可空、无默认值、无外键；PG 11+ 加可空无默认列只改元数据，不重写大表。
-- 历史行无需回填。
-- schema_migrations makes this migration idempotent.  Keep the ALTER TABLE
-- syntax portable: do not use ADD COLUMN IF NOT EXISTS (see 190).
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE usage_logs ADD COLUMN served_group_id BIGINT;
ALTER TABLE usage_logs ADD COLUMN served_route_source SMALLINT;
