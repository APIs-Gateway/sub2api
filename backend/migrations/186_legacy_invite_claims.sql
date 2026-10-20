-- 「旧站付费用户领取本站邀请码」的发放流水。
--
-- 达标判定实时查旧站库，所以这张表只记录本站这一侧的结果：谁领过、领到了哪个码。
-- 一个旧站邮箱只能领一次，靠 lower(email) 上的唯一索引兜底 ——
-- 即便两个并发请求同时通过了达标校验，也只有一条能落库，另一条会撞唯一冲突并被拒绝，
-- 不需要额外加锁。
CREATE TABLE IF NOT EXISTS legacy_invite_claims (
    id BIGSERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL,
    -- 旧站的 user_id，只用于对账，不与本站 users.id 有任何关系
    legacy_user_id BIGINT NOT NULL DEFAULT 0,
    -- 领取当时算出的旧站实付金额，留档以便日后核对门槛调整前后的发放情况
    paid_amount NUMERIC(20, 2) NOT NULL DEFAULT 0,
    redeem_code VARCHAR(64) NOT NULL,
    claimed_ip VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uniq_legacy_invite_claims_email
    ON legacy_invite_claims (LOWER(email));
