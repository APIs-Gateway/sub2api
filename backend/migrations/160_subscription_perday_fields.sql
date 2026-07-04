-- Per-day 每日额度模型字段（per-day redesign，Phase 1：纯加性、零行为变化）。
-- 新模型把「套餐余额」从 burn-down 窗口（granted_total/consumed/clawed + daily 窗口）
-- 改为只存 today_remaining（今日剩余，官方刀）+ today_day（它属于哪个东八区自然日序号），
-- 跨天惰性覆盖成 D、过期卡置 0，无发放/撤回/清扣动作；服务区间用绝对自然日序号
-- [start_day, expire_day]，透支即 expire_day−=1。
--
-- 本迁移只 ADD COLUMN（带 IF NOT EXISTS + 默认值），不动任何现有列/索引/外键；
-- 旧 burn-down 字段与新字段并存，存量卡的新字段保持默认值，待上线前的一次性
-- 「balance 解混 + 转 per-day」数据迁移再回填（见 docs/billing-perday-redesign.md「存量数据迁移」）。

-- 1. user_subscriptions：套餐余额（today_remaining）+ 自然日服务区间（start_day/expire_day）。
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS today_remaining numeric(20,10) NOT NULL DEFAULT 0;
-- today_day/start_day/expire_day 均为「东八区绝对自然日序号」= floor((unix+8h)/86400)。
-- today_day 默认 -1 = 未初始化（任何真实日序号 ≥0 都不等于它），强制首次访问惰性覆盖。
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS today_day   integer NOT NULL DEFAULT -1;
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS start_day   integer NOT NULL DEFAULT 0;
ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS expire_day  integer NOT NULL DEFAULT 0;

COMMENT ON COLUMN user_subscriptions.today_remaining IS '套餐余额：今日剩余额度（官方刀，1:1 扣减、永不为负）；跨天且 today≤expire_day 惰性覆盖成 D';
COMMENT ON COLUMN user_subscriptions.today_day  IS 'today_remaining 对应的东八区绝对自然日序号 floor((unix+8h)/86400)；≠当前日即惰性覆盖；-1=未初始化';
COMMENT ON COLUMN user_subscriptions.start_day  IS '激活当天的东八区绝对自然日序号';
COMMENT ON COLUMN user_subscriptions.expire_day IS '最后发放 D 的东八区绝对自然日序号（含）；无透支时=start_day+T−1，每透支 −1；today>expire_day 即到期';

-- 2. users：透支改为用户级月度计数（取代 per-card user_subscriptions.total_overdraft_count）。
ALTER TABLE users ADD COLUMN IF NOT EXISTS monthly_overdraft_count integer     NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS monthly_overdraft_month varchar(6)  NOT NULL DEFAULT '';

COMMENT ON COLUMN users.monthly_overdraft_count IS '当前自然月已透支次数（按用户计，名下多卡共享）；达上限（默认 5）当月禁止透支';
COMMENT ON COLUMN users.monthly_overdraft_month IS 'monthly_overdraft_count 所属东八区月份 YYYYMM；惰性按月重置（模式同 user_subscriptions.daily_spent_day）';
