-- 积分提现单：用户填支付宝账号+姓名 或 USDT 地址 → 后台人工审核 + 人工打款。
-- 状态机：pending → paid / rejected。提交即扣减积分(写 withdraw_hold)，驳回退回(withdraw_refund)。
CREATE TABLE IF NOT EXISTS user_points_withdrawals (
    id                    BIGSERIAL PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    points                BIGINT NOT NULL,
    gross_amount          DECIMAL(20,8) NOT NULL,
    fee_amount            DECIMAL(20,8) NOT NULL,
    net_amount            DECIMAL(20,8) NOT NULL,
    peg_at                DECIMAL(20,8) NULL,
    fee_percent_at        DECIMAL(8,4) NULL,
    payout_method         VARCHAR(16) NOT NULL,
    payout_alipay_account VARCHAR(128) NULL,
    payout_alipay_name    VARCHAR(64) NULL,
    payout_usdt_address   VARCHAR(128) NULL,
    status                VARCHAR(16) NOT NULL DEFAULT 'pending',
    review_note           VARCHAR(255) NULL,
    reviewed_by           BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    payout_proof          TEXT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at           TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_user_points_withdrawals_user ON user_points_withdrawals(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_user_points_withdrawals_status ON user_points_withdrawals(status, created_at);
-- 反滥用：同一用户至多 1 张待审核提现单。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_points_withdrawals_one_pending ON user_points_withdrawals(user_id) WHERE status = 'pending';

COMMENT ON TABLE  user_points_withdrawals IS '积分提现单（人工审核 + 人工打款）';
COMMENT ON COLUMN user_points_withdrawals.points              IS '提现申请的积分数';
COMMENT ON COLUMN user_points_withdrawals.gross_amount        IS '折合金额 = points × peg（balance 单位）';
COMMENT ON COLUMN user_points_withdrawals.net_amount          IS '应付 = gross − fee';
COMMENT ON COLUMN user_points_withdrawals.payout_method         IS 'alipay|usdt';
COMMENT ON COLUMN user_points_withdrawals.payout_alipay_account IS '支付宝账号（手机号/邮箱，payout_method=alipay）';
COMMENT ON COLUMN user_points_withdrawals.payout_alipay_name    IS '支付宝真实姓名（payout_method=alipay）';
COMMENT ON COLUMN user_points_withdrawals.payout_usdt_address   IS 'USDT 收款地址（payout_method=usdt）';
COMMENT ON COLUMN user_points_withdrawals.status              IS 'pending|paid|rejected';
COMMENT ON COLUMN user_points_withdrawals.reviewed_by         IS '审核管理员 user_id';
COMMENT ON COLUMN user_points_withdrawals.payout_proof        IS '打款凭证（可选）';
