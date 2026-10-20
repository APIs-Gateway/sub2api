CREATE TABLE IF NOT EXISTS affiliate_cashback_subscription_mappings (
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    validity_days INTEGER NOT NULL,
    cashback_base_amount DECIMAL(20,8) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, validity_days)
);

COMMENT ON TABLE affiliate_cashback_subscription_mappings IS '邀请返现订阅兑换码映射配置';
COMMENT ON COLUMN affiliate_cashback_subscription_mappings.group_id IS '订阅分组 ID';
COMMENT ON COLUMN affiliate_cashback_subscription_mappings.validity_days IS '订阅有效天数';
COMMENT ON COLUMN affiliate_cashback_subscription_mappings.cashback_base_amount IS '用于计算返现的金额基数';

ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS source_redeem_code_type VARCHAR(20) NULL;

ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS source_subscription_group_id BIGINT NULL REFERENCES groups(id) ON DELETE SET NULL;

ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS source_subscription_validity_days INTEGER NULL;

COMMENT ON COLUMN user_affiliate_ledger.source_redeem_code_type IS '产生返现的兑换码类型';
COMMENT ON COLUMN user_affiliate_ledger.source_subscription_group_id IS '订阅兑换码所属分组 ID';
COMMENT ON COLUMN user_affiliate_ledger.source_subscription_validity_days IS '订阅兑换码有效天数';
