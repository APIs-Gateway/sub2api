-- 领码流水补记「用量消费」口径。
--
-- 达标从此有两条并列的口径：旧站实付金额，或旧站累计用量消费（USD）。
-- paid_amount 已经留档了前者，这里补上后者——只有两个值都在，
-- 事后才看得出某个人当初是靠哪条口径拿到码的，门槛调整前后的发放情况也才对得上账。
--
-- 精度对齐旧站 usage_logs.actual_cost 的 numeric(20,10)：
-- 用量消费按 token 累加，小数位远多于人民币金额，截断到 2 位会把小额用户的合计抹平。
ALTER TABLE legacy_invite_claims
    ADD COLUMN IF NOT EXISTS usage_cost NUMERIC(20, 10) NOT NULL DEFAULT 0;

COMMENT ON COLUMN legacy_invite_claims.usage_cost IS '领取当时旧站的累计用量消费（USD，口径为 usage_logs.actual_cost 合计）';
