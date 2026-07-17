-- 178_usage_log_image_input_tokens.sql
-- usage_logs 单独记录图片输入 token 数与费用，便于图片编辑/图生图场景对账。
-- image_input_tokens 从 input_tokens 中拆出，image_input_cost 从 input_cost 中拆出，
-- total_cost 口径不变。
-- schema_migrations makes this migration idempotent.  Keep the ALTER TABLE
-- syntax portable: SQLite and MySQL 5.7 do not support ADD COLUMN IF NOT EXISTS.
ALTER TABLE usage_logs ADD COLUMN image_input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN image_input_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;
