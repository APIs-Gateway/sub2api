-- 修复 clawback↔thaw 复活资损（issue #11 post-review，Bug 1/HIGH）。
--
-- 病灶：account.frozen 是聚合值，clawback 扣它但不动来源 earn 行的 frozen_until/待解冻量；
-- ThawDuePoints 又按 earn 行原始 points 整额解冻，对「该笔是否已被 clawback」无感 → 冻结期内
-- 被撤回的积分会在冻结到期时复活进 available（可提现/换余额），反套利冻结闸门被冲破。
--
-- 对策：给 earn 行加「仍冻结、待解冻」余额列。clawback 消费冻结时按来源行递减它；
-- thaw 改为按此列解冻并归零。不变式恢复为 account.frozen == Σ(未到期 earn 行 frozen_remaining)。
ALTER TABLE user_points_ledger
    ADD COLUMN IF NOT EXISTS frozen_remaining BIGINT NOT NULL DEFAULT 0;

-- 回填：当前仍处于冻结期、未解冻的 earn 行，其待解冻额初始 = 原始 points。
-- （本特性尚未上线，正常无存量；幂等加性，重跑安全。）
UPDATE user_points_ledger
   SET frozen_remaining = points
 WHERE kind = 'earn' AND frozen_until IS NOT NULL AND frozen_remaining = 0 AND points > 0;

COMMENT ON COLUMN user_points_ledger.frozen_remaining IS 'earn 行仍冻结、待解冻的积分余额（clawback 消费冻结时递减；thaw 时按此额解冻并归零）。修复 clawback↔thaw 复活资损。';
