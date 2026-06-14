-- 「最多透支天数」从按用户改为按订阅卡 + 用户自助。
-- 1) 每张订阅卡一个透支天数（NULL = 该卡不限制，默认）。
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS max_overdraft_days integer NULL;
COMMENT ON COLUMN user_subscriptions.max_overdraft_days IS '本卡最多往后透支天数：累计被扣最多到 (已过天数+N)×每日额度；NULL = 不限制。由用户在「我的订阅」自助设置。';

-- 2) 准入闸门的廉价触发位：用户是否给任意订阅卡设过透支上限（只置真、不自动清）。
ALTER TABLE users ADD COLUMN IF NOT EXISTS subscription_overdraft_guard boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN users.subscription_overdraft_guard IS '该用户是否给任意订阅卡设过透支上限；为 true 时准入闸门才按卡计算锁定额度。';

-- 3) 废弃上一版的按用户字段（151 引入，此处前向替换为按订阅卡 + guard）。
ALTER TABLE users DROP COLUMN IF EXISTS max_overdraft_days;
