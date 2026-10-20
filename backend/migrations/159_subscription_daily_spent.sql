-- Burn-down 订阅「每日限速」per-day 计量字段。
-- 模型:整张卡 G 一次性进余额,但每个日历天最多消费 D(开透支当天可突破到 (1+预支天数)×D);
-- 透支=把后续天额度提前花(计费周期前移),卡更早用完,无「还债/次日锁死」一说。
-- daily_spent_usd: 当前 burn-down 日内已消费额度;daily_spent_day: 该值对应的日历天 N。
-- 读写时若 daily_spent_day ≠ 当前 N 即视为 0(惰性重置,不跨天结转)。
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS daily_spent_usd numeric(20,10) NOT NULL DEFAULT 0;
-- daily_spent_day 默认 -1 = 未初始化(任何真实日历天 N≥0 都不等于它),避免与「day0 已消费 0」混淆。
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS daily_spent_day integer NOT NULL DEFAULT -1;

COMMENT ON COLUMN user_subscriptions.daily_spent_usd IS '当前 burn-down 日内已消费额度(配合 daily_spent_day);每日限速 D、不跨天结转';
COMMENT ON COLUMN user_subscriptions.daily_spent_day IS 'daily_spent_usd 对应的日历天 N(自激活起跨过的东八区午夜数);读写时 ≠ 当前 N 即视为 0;-1=未初始化';
COMMENT ON COLUMN user_subscriptions.total_overdraft_count IS '本卡累计预支天数(求和、封顶 5):每天突破当日 D 的天数累加,达上限自动关闭透支';
