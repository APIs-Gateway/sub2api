-- 164_subscription_group_id_nullable.sql
-- per-day → 三窗口 redesign，P5e：user_subscriptions.group_id 可空化。
--
-- 背景：限额已从 group 搬到订阅卡自身（卡级 *_limit_usd），订阅可在任意 group 下使用。
-- 自定义 D+T 购买的订阅卡不再有 group 归属 —— group_id 改 NULL 表示「无 group 的自定义卡」；
-- 历史按 group 分配/兑换的卡仍写其来源 group，仅作历史快照，不再构成权限约束。
--
-- 与迁移 155 的 Phase 3 计划一致（155 注释明确「可空化 / FK 改 SET NULL 留到 Phase 3」）。
-- 对应 ent schema：UserSubscription.group_id 改 Optional().Nillable()，group 边去 Required()
-- （可空边 → ON DELETE SET NULL）。
--
-- 幂等：DROP NOT NULL / DROP CONSTRAINT IF EXISTS / ADD CONSTRAINT 守护，可重复执行。

-- 1. 列可空化：允许自定义卡的 group_id 为 NULL。
ALTER TABLE user_subscriptions ALTER COLUMN group_id DROP NOT NULL;

COMMENT ON COLUMN user_subscriptions.group_id IS '订阅卡来源分组 ID；自定义 D+T 卡无 group 归属为 NULL；仅作历史快照，限额读卡级 *_limit_usd';

-- 2. 外键 ON DELETE CASCADE → SET NULL：删除 group 不再级联删订阅卡，仅置空其历史快照。
--    003 内联建表自动命名为 user_subscriptions_group_id_fkey；先删后建以幂等切换语义。
ALTER TABLE user_subscriptions DROP CONSTRAINT IF EXISTS user_subscriptions_group_id_fkey;
ALTER TABLE user_subscriptions
    ADD CONSTRAINT user_subscriptions_group_id_fkey
    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE SET NULL;

-- 注：(user_id, group_id) 部分唯一索引 user_subscriptions_user_group_unique_active 保持不变。
-- Postgres 唯一索引中 NULL 互不相等，故自定义卡（group_id NULL）不受该约束限制，
-- 「至多一张 active 自定义卡」由购买入口在应用层保证（见 AssignOrExtendSubscription）。
