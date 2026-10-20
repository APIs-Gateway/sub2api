-- 155_subscription_plan_decouple_additive.sql
-- 分组(Group)与订阅套餐(SubscriptionPlan)解耦 —— Phase 0：纯加性。
-- 目标：把套餐"日额 D / platform"收归 SubscriptionPlan，并给 UserSubscription、
--       RedeemCode 增加 plan_id 一等关联，为后续把发放入口从 group 切到 plan 做准备。
--
-- 本迁移严格加性，零行为变化：
--   - 只 ADD COLUMN / CREATE INDEX，并对新列回填；
--   - 不动任何现有列的 NOT NULL，不改任何现有外键（user_subscriptions.group_id
--     仍 NOT NULL + ON DELETE CASCADE）。可空化 / FK 改 SET NULL 留到 Phase 3 的迁移 B。
-- 幂等：ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS / 回填带 WHERE 守护。

-- 1. subscription_plans：套餐成为日额 D 的 source of truth；platform 仅展示/筛选用。
ALTER TABLE subscription_plans ADD COLUMN IF NOT EXISTS daily_amount_usd DECIMAL(20,10) NOT NULL DEFAULT 0;
ALTER TABLE subscription_plans ADD COLUMN IF NOT EXISTS platform         VARCHAR(50)    NOT NULL DEFAULT '';

COMMENT ON COLUMN subscription_plans.daily_amount_usd IS 'burn-down 套餐每日发放额度 D（source of truth，取代从 group.daily_limit_usd 推导）';
COMMENT ON COLUMN subscription_plans.platform         IS '套餐平台标识，仅用于展示/筛选；不构成路由或权限约束';

-- 回填：从所挂 group 把 daily_limit_usd / platform 快照到套餐（仅对尚未回填的行）。
UPDATE subscription_plans p
SET daily_amount_usd = COALESCE(g.daily_limit_usd, 0),
    platform         = g.platform
FROM groups g
WHERE p.group_id = g.id
  AND p.daily_amount_usd = 0;

-- 2. user_subscriptions：增加 plan_id 一等关联（可空，FK 即用 SET NULL，因是新列）。
--    group_id 保持 NOT NULL + 现有 FK 不动。
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS plan_id BIGINT REFERENCES subscription_plans(id) ON DELETE SET NULL;
COMMENT ON COLUMN user_subscriptions.plan_id IS '订阅来源套餐 ID；存量按 group 直接分配或无对应套餐者为 NULL';
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_plan_id ON user_subscriptions(plan_id);

-- 回填 plan_id（尽力而为）：按订阅当初所挂 group 匹配同 group 的套餐，取最小 id 保证可复现。
-- 管理员当初直接按 group 分配、且该 group 无对应套餐的存量卡，plan_id 保持 NULL，
-- 靠已快照的 daily_amount_usd 继续正常工作。
UPDATE user_subscriptions us
SET plan_id = (
    SELECT p.id FROM subscription_plans p
    WHERE p.group_id = us.group_id
    ORDER BY p.id
    LIMIT 1
)
WHERE us.plan_id IS NULL
  AND EXISTS (
    SELECT 1 FROM subscription_plans p2 WHERE p2.group_id = us.group_id
  );

-- 3. redeem_codes：增加 plan_id（照搬 group_id 范式：可空 + ON DELETE SET NULL）。
ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS plan_id BIGINT REFERENCES subscription_plans(id) ON DELETE SET NULL;
COMMENT ON COLUMN redeem_codes.plan_id IS '订阅型兑换码关联的套餐 ID（取代仅靠 group_id 推导）';
CREATE INDEX IF NOT EXISTS idx_redeem_codes_plan_id ON redeem_codes(plan_id);
