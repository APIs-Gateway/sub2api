-- 209_usage_log_cost_unit.sql
-- 站内额度改以人民币记账（见 service.CreditUnit）：usage_logs 新增 cost_unit，
-- 标记这一行 actual_cost 等金额的记账单位。
--   NULL = 历史额度（切换前写入的行；CREDIT_CURRENCY=USD 时写入的所有行）
--   1    = 人民币（CREDIT_CURRENCY=CNY 时写入）
-- 可空、无默认值、无约束：PG 11+ 加可空无默认列只改元数据，不重写 9GB 的大表，也不回填历史行。
-- 读路径靠它区分单位（NULL 行按 LEGACY_CREDIT_DIVISOR 换算），所以这一列必须保持 NULL = 历史。
-- 幂等：用 IF NOT EXISTS，重复执行（例如手工先加过列的预览库）不报错。
-- 和 190 / 196 的区别：不再追求 SQLite / MySQL 可移植——同批的 SET LOCAL lock_timeout 本身就是
-- PostgreSQL 专有语法（196 已经如此），这张大表只在 PostgreSQL 上。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cost_unit SMALLINT;
