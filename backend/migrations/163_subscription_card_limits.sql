-- 三窗口限额：把订阅「日/周/月限额」从 group 搬到订阅卡自身（per-day → 三窗口 redesign）。
-- 实现「订阅与分组解耦」的地基：限额今后挂在 user_subscriptions 上，不再依赖
-- groups.daily_limit_usd / weekly_limit_usd / monthly_limit_usd。
--
-- 本迁移纯加性（ADD COLUMN IF NOT EXISTS，可空、无默认 = NULL），不动任何现有列/索引/外键。
-- NULL = 该窗口不限。存量卡保持 NULL，待计费核心切换 + 上线前的一次性数据迁移再回填
-- （把 D 写入 daily_limit_usd、W=7×D、M=min(T,30)×D，见 docs/billing-perday-redesign.md「存量数据迁移」）。
--
-- 精度与同表其它金额字段（*_usage_usd / today_remaining / daily_amount_usd）一致用 numeric(20,10)，
-- 便于与 *_usage_usd 直接做 limit − usage 比较，避免跨精度比较的舍入歧义。

ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS daily_limit_usd   numeric(20,10);
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS weekly_limit_usd  numeric(20,10);
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS monthly_limit_usd numeric(20,10);

COMMENT ON COLUMN user_subscriptions.daily_limit_usd   IS '每日额度 D（官方刀）；NULL=日窗口不限。与 daily_usage_usd 配对：usage<limit 才由订阅覆盖';
COMMENT ON COLUMN user_subscriptions.weekly_limit_usd  IS '每周封顶 W（官方刀，默认 7×D）；NULL=周窗口不限';
COMMENT ON COLUMN user_subscriptions.monthly_limit_usd IS '每月封顶 M（官方刀，默认 min(T,30)×D）；NULL=月窗口不限';
