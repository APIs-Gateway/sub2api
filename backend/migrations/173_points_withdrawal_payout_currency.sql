-- 提现单固化实际打款币种和 USDT 折算汇率，避免支付宝提现显示 USD、USDT 审核时丢失当日汇率口径。
ALTER TABLE user_points_withdrawals
    ADD COLUMN IF NOT EXISTS payout_currency VARCHAR(8) NULL,
    ADD COLUMN IF NOT EXISTS usd_cny_rate_at DECIMAL(20,8) NULL;

UPDATE user_points_withdrawals
SET payout_currency = CASE
    WHEN payout_method = 'usdt' THEN 'USD'
    ELSE 'CNY'
END
WHERE payout_currency IS NULL OR payout_currency = '';

COMMENT ON COLUMN user_points_withdrawals.payout_currency IS '实际打款币种：CNY=支付宝人民币，USD=USDT 折合美元';
COMMENT ON COLUMN user_points_withdrawals.usd_cny_rate_at IS 'USDT 提现使用的有效 USD/CNY 汇率快照（后台日价 + 0.1）';
