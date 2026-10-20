-- Per-day 透支开关（per-day redesign，Phase 1 续：纯加性）。
-- 新模型透支上限改为用户级月度（users.monthly_overdraft_count，见迁移 160），
-- 本卡只保留「开/关」一个布尔，取代旧 max_overdraft_days 的 nil/非nil 语义。
-- 用户在「我的订阅」自助开启；转套餐/续费产生的新卡默认关闭。
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS overdraft_on boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN user_subscriptions.overdraft_on IS 'per-day 模型本卡是否开启透支（开/关）；上限按用户级月度 users.monthly_overdraft_count 计';
