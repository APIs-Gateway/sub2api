-- USDT 提现需要明确链路，避免同一个地址字段无法区分 TRC20/ERC20/BEP20。
ALTER TABLE user_points_withdrawals
    ADD COLUMN IF NOT EXISTS payout_usdt_chain VARCHAR(16) NULL;

COMMENT ON COLUMN user_points_withdrawals.payout_usdt_chain IS 'USDT 收款链路（TRC20|ERC20|BEP20，payout_method=usdt）';
