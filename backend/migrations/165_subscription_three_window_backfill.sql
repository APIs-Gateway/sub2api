-- 165_subscription_three_window_backfill.sql
-- per-day → 三窗口 redesign：存量活跃卡的三窗口限额一次性回填（163 已加列、留待本迁移回填）。
--
-- 背景：163 为 user_subscriptions 加了 daily/weekly/monthly_limit_usd（可空，NULL=该窗口不限），
-- 但存量卡保持 NULL。三窗口计费下 NULL 三限额全空的卡经 SubRemaining 安全闸返回 0（订阅不覆盖、
-- 回落钱包）——对存量订阅用户即「订阅形同失效、改烧钱包」。本迁移按设计公式回填，使存量卡恢复
-- 三窗口覆盖能力。
--
-- 回填公式（= service.DeriveWindowCaps，见 docs/billing-perday-redesign.md §3/§5）：
--   daily_limit_usd   = D                       （每日额度，卡上 daily_amount_usd）
--   weekly_limit_usd  = 7  × D
--   monthly_limit_usd = min(T, 30) × D          （T = 原始有效天数）
-- 其中 T 稳健推导：优先 round(granted_total_usd / D)（G、D 自 burn-down 迁移 142 即有，避开
-- start_day/expire_day 被透支借天漂移的问题）；G 缺失（=0 的 legacy/standard 卡）时回退
-- expire_day−start_day+1；再缺失回退 30（按整月卡）。LEAST(T,30) 与公式一致。
--
-- 幂等与安全：
--   - 仅回填 status='active'、daily_amount_usd>0、且 daily_limit_usd IS NULL 的卡；
--     三列同时写，`daily_limit_usd IS NULL` 即天然幂等游标（重跑不覆盖、不动上线后新建卡）。
--   - 纯回填既有 NULL 列，不动其它列/usage/窗口起点/有效期，无资损面。
--   - 新 DB（CI）无存量卡 → 空操作；schema_migrations checksum 跟踪，仅执行一次。
--
-- 注：本迁移不含「balance 解混」（订阅赠款 vs 充值余额的拆分），那部分数据相关且具破坏性，
-- 见 docs/billing-perday-prelaunch-cleanup.md，单独在上线前分阶段执行。

UPDATE user_subscriptions
SET daily_limit_usd   = daily_amount_usd,
    weekly_limit_usd  = 7 * daily_amount_usd,
    monthly_limit_usd = LEAST(
        CASE
            WHEN granted_total_usd > 0 THEN ROUND(granted_total_usd / daily_amount_usd)
            WHEN expire_day > 0 AND start_day > 0 THEN (expire_day - start_day + 1)
            ELSE 30
        END,
        30
    ) * daily_amount_usd
WHERE status = 'active'
  AND deleted_at IS NULL
  AND daily_amount_usd > 0
  AND daily_limit_usd IS NULL;
