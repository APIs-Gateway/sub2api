-- 按用户配置 burn-down 订阅「最多往后透支天数」。
-- NULL = 不限制（默认，维持一次性发放后可立即花光的现状）；
-- 正整数 N = 某订阅卡累计被扣最多到 (已过天数 + N) × 每日额度 D，超出后由该订阅供资的新请求被准入闸门拒绝。
ALTER TABLE users ADD COLUMN IF NOT EXISTS max_overdraft_days integer NULL;

COMMENT ON COLUMN users.max_overdraft_days IS 'burn-down 订阅最多往后透支天数；NULL 表示不限制（默认行为）。';
