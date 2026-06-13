-- 142_subscription_burndown_model.sql
-- 订阅计费改造：从「配额上限模式」改为「预充值余额 + 每日清扣（burn-down）」。
-- 1) user_subscriptions 增加 burn-down 账户字段。
-- 2) 删除 (user_id, group_id) 的部分唯一索引，允许同一用户对同一套餐叠加多张独立订阅卡。
-- 幂等：ADD COLUMN IF NOT EXISTS / DROP INDEX IF EXISTS。

-- 1. 新增 burn-down 字段（与 ent schema 对齐：decimal(20,10) 金额、integer 天数、timestamptz 时钟）
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS granted_total_usd decimal(20,10) NOT NULL DEFAULT 0;
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS daily_amount_usd  decimal(20,10) NOT NULL DEFAULT 0;
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS consumed_usd      decimal(20,10) NOT NULL DEFAULT 0;
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS clawed_usd        decimal(20,10) NOT NULL DEFAULT 0;
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS last_clawback_day integer        NOT NULL DEFAULT 0;
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS activated_at      timestamptz;

COMMENT ON COLUMN user_subscriptions.granted_total_usd IS 'burn-down 发放总额 G = D × days';
COMMENT ON COLUMN user_subscriptions.daily_amount_usd  IS 'burn-down 每日额度 D（开通时对 group.daily_limit_usd 的快照）';
COMMENT ON COLUMN user_subscriptions.consumed_usd      IS '本卡累计消费（单调递增）';
COMMENT ON COLUMN user_subscriptions.clawed_usd        IS '本卡累计被清扣（单调递增）';
COMMENT ON COLUMN user_subscriptions.last_clawback_day IS '已对账到的最高日历天 N（重启安全游标）';
COMMENT ON COLUMN user_subscriptions.activated_at      IS '清扣时钟起点（Asia/Shanghai 计第 N 天）；nil 回退 starts_at';

-- 2. 删除 (user_id, group_id) 部分唯一索引（来自 016），允许叠加多卡。
--    保留普通复合索引用于查询（schema Indexes 里仍声明 user_id+group_id）。
DROP INDEX IF EXISTS user_subscriptions_user_group_unique_active;
