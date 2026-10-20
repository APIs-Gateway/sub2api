-- 积分流水（append-only 审计）：所有积分增减一笔一行，带前后快照与来源锚点。
-- points 带符号（+入 −出）。earn/clawback 按来源订单 partial-unique 幂等。
CREATE TABLE IF NOT EXISTS user_points_ledger (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind            VARCHAR(32) NOT NULL,
    points          BIGINT NOT NULL,
    peg_at          DECIMAL(20,8) NULL,
    source_user_id  BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    source_order_id BIGINT NULL REFERENCES payment_orders(id) ON DELETE SET NULL,
    withdrawal_id   BIGINT NULL,
    frozen_until    TIMESTAMPTZ NULL,
    available_after BIGINT NULL,
    frozen_after    BIGINT NULL,
    note            VARCHAR(255) NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_points_ledger_user_created ON user_points_ledger(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_user_points_ledger_frozen ON user_points_ledger(user_id, frozen_until) WHERE frozen_until IS NOT NULL;
-- earning 按来源订单幂等：同一邀请人同一来源单只 earn 一次。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_points_ledger_earn_uniq ON user_points_ledger(user_id, source_order_id) WHERE kind = 'earn';
-- clawback 按来源订单幂等：一单一撤。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_points_ledger_clawback_uniq ON user_points_ledger(user_id, source_order_id) WHERE kind = 'clawback';

COMMENT ON TABLE  user_points_ledger IS '积分流水（邀请返利积分增减审计）';
COMMENT ON COLUMN user_points_ledger.kind            IS 'earn|clawback|thaw|to_balance|withdraw_hold|withdraw_paid|withdraw_refund|to_plan|adjust';
COMMENT ON COLUMN user_points_ledger.points          IS '本笔积分变动，带符号（+入 −出）';
COMMENT ON COLUMN user_points_ledger.peg_at          IS '记账时 peg 快照（balance 单位/积分）';
COMMENT ON COLUMN user_points_ledger.source_order_id IS 'earn/clawback 来源法币订单';
COMMENT ON COLUMN user_points_ledger.withdrawal_id   IS '提现相关流水关联的提现单 id（软引用，不设 FK）';
COMMENT ON COLUMN user_points_ledger.frozen_until    IS 'earn 冻结到期时间（NULL=未冻结/已解冻）';
