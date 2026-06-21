-- 每张 burn-down 订阅卡的累计透支请求次数。
-- 实际消费到当天已解锁额度之外的请求计 1 次；达到产品上限后后端会自动关闭本卡透支。
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS total_overdraft_count integer NOT NULL DEFAULT 0;

COMMENT ON COLUMN user_subscriptions.max_overdraft_days IS '本卡最多往后透支天数：累计被扣最多到 (已过天数+N)×每日额度；NULL = 透支关闭。由用户在「我的订阅」自助设置。';
COMMENT ON COLUMN user_subscriptions.total_overdraft_count IS '本卡累计透支请求次数（实际消费到当天额度之外的请求计 1 次）';
