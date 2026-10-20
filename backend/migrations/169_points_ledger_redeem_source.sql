-- 方案 C（issue #11 post-review）：兑换码返积分。
-- 旧「兑换码/订阅返现（cashback → $ aff_quota）」迁移为「返积分」；积分流水需要一个
-- 兑换码来源锚做幂等（兑换码 earning 不是 payment_orders，不能复用 source_order_id）。
ALTER TABLE user_points_ledger
    ADD COLUMN IF NOT EXISTS source_redeem_code_id BIGINT NULL REFERENCES redeem_codes(id) ON DELETE SET NULL;

-- 兑换码 earning 幂等：同一邀请人同一来源兑换码只 earn 一次（与 source_order_id 的 earn 唯一索引并列）。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_points_ledger_earn_redeem_uniq
    ON user_points_ledger(user_id, source_redeem_code_id) WHERE kind = 'earn';

COMMENT ON COLUMN user_points_ledger.source_redeem_code_id IS 'earn 来源兑换码（兑换码返积分，方案 C；与 source_order_id 二选一）';
