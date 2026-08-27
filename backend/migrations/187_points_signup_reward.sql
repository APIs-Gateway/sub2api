-- 邀请注册即得积分：被邀请人注册成功并绑定邀请人后，邀请人立即获得一笔固定积分，
-- 不需要被邀请人产生任何付费行为。
--
-- 唯一索引以「被邀请人」为键：同一个新用户只能为邀请人带来一次奖励，
-- 注册流程被重放、或绑定接口被重复调用，都会撞在这里而不是重复发放。
-- 用 partial index 而不是普通唯一约束，是因为同一个 source_user_id
-- 在 earn / clawback 等其它 kind 上仍然可以各有自己的流水行。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_points_ledger_signup_reward_uniq
    ON user_points_ledger (source_user_id)
    WHERE kind = 'signup_reward';
