-- 190_usage_log_upstream_model_mismatch.sql
-- 上游返回的 model 与我们实际发给上游的 model 不一致（上游偷换模型）时：
--   upstream_model_mismatch   = TRUE，该行为被拦截（或观察模式下放行）的审计记录
--   upstream_response_model   = 上游响应体里的 model 原文；NULL 表示与 upstream_model/model 一致
-- 注意 upstream_model 列语义不变：仍是我们发给上游的模型（映射后），不是上游返回的。
-- schema_migrations makes this migration idempotent.  Keep the ALTER TABLE
-- syntax portable: SQLite and MySQL 5.7 do not support ADD COLUMN IF NOT EXISTS.
ALTER TABLE usage_logs ADD COLUMN upstream_model_mismatch BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE usage_logs ADD COLUMN upstream_response_model VARCHAR(100);
