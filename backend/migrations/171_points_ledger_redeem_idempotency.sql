-- 修复换套餐不幂等（issue #11 post-review，Bug 2/MEDIUM；spec §5.2.4/§9）。
--
-- 病灶：RedeemToPlan 原子但无幂等——双击 / 客户端重试 / 网络重发会扣两次积分、把卡延两次 T。
-- 对策：to_plan 台账行带客户端幂等键（exchange_id），同一 (user_id, idempotency_key) 只兑一次；
-- 重复请求命中 partial-unique → 服务层判为重复、整事务回滚、不二次扣分。台账行本身即审计锚。
ALTER TABLE user_points_ledger
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_points_ledger_redeem_idem_uniq
    ON user_points_ledger(user_id, idempotency_key)
    WHERE kind = 'to_plan' AND idempotency_key IS NOT NULL;

COMMENT ON COLUMN user_points_ledger.idempotency_key IS '换套餐幂等键（客户端 exchange_id；仅 to_plan 行用，NULL 表示未提供→不去重）。';
