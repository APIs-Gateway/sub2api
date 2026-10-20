-- 邀请返利积分制（issue #11）：独立积分账户，与钱包余额 users.balance 完全分开。
-- 整数积分；available 允许为负（退款 clawback 可记负债，后续 earning 先抵扣）。
CREATE TABLE IF NOT EXISTS user_points_accounts (
    user_id         BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    available       BIGINT NOT NULL DEFAULT 0,
    frozen          BIGINT NOT NULL DEFAULT 0,
    lifetime_earned BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE  user_points_accounts IS '用户积分账户（邀请返利积分，与钱包余额分离）';
COMMENT ON COLUMN user_points_accounts.available       IS '可用积分（允许为负=clawback 负债）';
COMMENT ON COLUMN user_points_accounts.frozen          IS '冻结积分（冻结期内，到期转可用）';
COMMENT ON COLUMN user_points_accounts.lifetime_earned IS '历史累计返积分（仅统计）';
