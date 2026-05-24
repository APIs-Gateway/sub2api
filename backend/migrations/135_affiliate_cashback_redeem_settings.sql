CREATE TABLE IF NOT EXISTS affiliate_cashback_face_values (
    redeem_value DECIMAL(20,8) PRIMARY KEY,
    cashback_base_amount DECIMAL(20,8) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE affiliate_cashback_face_values IS '邀请返现兑换码面值映射配置';
COMMENT ON COLUMN affiliate_cashback_face_values.redeem_value IS '余额兑换码原始面值（redeem_codes.value）';
COMMENT ON COLUMN affiliate_cashback_face_values.cashback_base_amount IS '用于计算返现的金额基数';

ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS source_redeem_code_id BIGINT NULL REFERENCES redeem_codes(id) ON DELETE SET NULL;

ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS source_redeem_code_value DECIMAL(20,8) NULL;

ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS cashback_base_amount DECIMAL(20,8) NULL;

ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS cashback_rate_percent DECIMAL(5,2) NULL;

COMMENT ON COLUMN user_affiliate_ledger.source_redeem_code_id IS '产生该返现流水的兑换码 ID';
COMMENT ON COLUMN user_affiliate_ledger.source_redeem_code_value IS '产生返现的兑换码原始面值';
COMMENT ON COLUMN user_affiliate_ledger.cashback_base_amount IS '本次返现使用的计算金额基数';
COMMENT ON COLUMN user_affiliate_ledger.cashback_rate_percent IS '本次返现使用的返现比例（百分比）';

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_affiliate_ledger_redeem_cashback_uniq
    ON user_affiliate_ledger(action, source_redeem_code_id, user_id, source_user_id)
    WHERE action = 'cashback' AND source_redeem_code_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_user_affiliate_ledger_source_redeem_code_id
    ON user_affiliate_ledger(source_redeem_code_id)
    WHERE source_redeem_code_id IS NOT NULL;

INSERT INTO settings (key, value, updated_at)
VALUES
    ('affiliate_cashback_enabled', 'false', NOW()),
    ('affiliate_cashback_rate_percent', '20', NOW())
ON CONFLICT (key) DO NOTHING;
