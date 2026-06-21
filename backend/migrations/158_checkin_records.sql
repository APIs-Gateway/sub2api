-- 每日签到记录表。
-- daily：每日基础签到，每用户每自然日（Asia/Shanghai）仅一次（由分区唯一索引强约束）。
-- bonus：按当日消费额外解锁的签到（floor(今日消费 / 每满$X)），同一天可多条。
CREATE TABLE IF NOT EXISTS checkin_records (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    checkin_date DATE NOT NULL,
    type VARCHAR(16) NOT NULL,
    amount DECIMAL(20,8) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 每用户每日仅一条 daily（bonus 不受此约束，可多条）。
CREATE UNIQUE INDEX IF NOT EXISTS idx_checkin_records_user_day_daily
ON checkin_records(user_id, checkin_date)
WHERE type = 'daily';

-- 按用户 + 自然日聚合/计数（统计当日 bonus 已领数、历史查询）。
CREATE INDEX IF NOT EXISTS idx_checkin_records_user_date
ON checkin_records(user_id, checkin_date);

COMMENT ON TABLE checkin_records IS '每日签到记录（daily=每日基础签到，bonus=按消费额外签到）';
COMMENT ON COLUMN checkin_records.type IS 'daily|bonus';
COMMENT ON COLUMN checkin_records.checkin_date IS '签到所属自然日（Asia/Shanghai）';
COMMENT ON COLUMN checkin_records.amount IS '本次签到发放到余额的金额（USD）';
