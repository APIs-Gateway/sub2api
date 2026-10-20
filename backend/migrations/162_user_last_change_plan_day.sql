-- 162_user_last_change_plan_day.sql
-- per-day 转套餐限频（规格第 7 节）：每用户每自然日最多转套餐 1 次。
-- 新增用户级字段，存上一次转套餐所在「东八区绝对自然日序号」（同 start_day/expire_day/today_day
-- 口径，见 EastDayNumber）；0 = 从未转过。判定：当前 today == last_change_plan_day 则禁止再转；
-- 次日 today 自然 ≠ 该值，限制自动解除，无需显式重置。
-- 纯加性、幂等（ADD COLUMN IF NOT EXISTS）。

ALTER TABLE users ADD COLUMN IF NOT EXISTS last_change_plan_day INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN users.last_change_plan_day IS
    '上一次转套餐所在东八区绝对自然日序号(EastDayNumber);0=从未转过;与当前 today 相同时当日禁止再转';
