-- 143_subscription_burndown_backfill.sql
-- 把现有「配额上限模式」的活跃订阅一次性迁移到 burn-down 模型：
--   - daily_amount_usd = 套餐每日额度 D（group.daily_limit_usd）
--   - granted_total_usd = D × 剩余天数（CEIL，按当前时刻到 expires_at）
--   - consumed/clawed/last_clawback_day 归零，activated_at = NOW()（对剩余期重新计时，赦免历史用量）
--   - 把 granted_total_usd 一次性加进 users.balance
-- 仅处理 subscription 模式分组、且 daily_limit_usd 非空、活跃未删除、未过期、尚未迁移(granted_total_usd=0)的订阅。
-- 注意：subscription 分组若未配置 daily_limit_usd，则无法确定 D，本次不迁移（保持原状，需人工处理）。
-- 本文件经 schema_migrations checksum 跟踪，仅执行一次；granted_total_usd=0 守卫为二次保险。

WITH migrated AS (
    UPDATE user_subscriptions us
    SET daily_amount_usd  = g.daily_limit_usd,
        granted_total_usd = g.daily_limit_usd * CEIL(EXTRACT(EPOCH FROM (us.expires_at - NOW())) / 86400.0),
        consumed_usd      = 0,
        clawed_usd        = 0,
        last_clawback_day = 0,
        activated_at      = NOW()
    FROM groups g
    WHERE us.group_id = g.id
      AND g.subscription_type = 'subscription'
      AND g.daily_limit_usd IS NOT NULL
      AND g.daily_limit_usd > 0
      AND us.status = 'active'
      AND us.deleted_at IS NULL
      AND us.expires_at > NOW()
      AND us.granted_total_usd = 0
    RETURNING us.user_id, us.granted_total_usd AS granted
)
UPDATE users u
SET balance = u.balance + agg.total
FROM (
    SELECT user_id, SUM(granted) AS total
    FROM migrated
    GROUP BY user_id
) agg
WHERE u.id = agg.user_id;
