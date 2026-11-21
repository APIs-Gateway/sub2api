-- C1: 渠道状态页二期 —— 分组真实成功率聚合表（Phase 2 Plan §1.2）
--
-- 新增 channel_monitor_group_stats 10 分钟桶聚合表，记录各分组的真实流量成功率。
-- 写入路径完全脱离请求主链，由后台定时任务填充（每 5 分钟聚合最近 30 分钟窗口）。
--
-- 同时给 channel_monitors 加 group_id 列，用于关联聚合数据。NULL = 纯探测模式。
--
-- 探测 key 的排除：采用配置文件显式列出 API key ID 列表的方案（而非 user_id=1 全排除，
-- 因为 user 1 是内部聚合账号上有真实流量）。配置项由 C2 实现，C1 聚合任务中预留占位。
--
-- 注意：nginx 边缘伪造响应的识别标准实际是
--   output_tokens IN (86, 116) AND upstream_model_mismatch = true AND total_cost = 0
-- 而非方案中的 model='gpt-5.6-luna'（model 是用户请求模型，86/116 token 在真实计费行也常见）。
--
-- 此 PR 建表、加索引、写填充逻辑、写单测；定时任务注册、合并判定逻辑由 C2 实现。

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS channel_monitor_group_stats (
    id BIGSERIAL PRIMARY KEY,

    -- 分组维度
    group_id BIGINT NOT NULL,

    -- 时间桶（按分钟对齐，聚合时以 10 分钟桶为基本单位）
    bucket_start TIMESTAMPTZ NOT NULL,  -- 精确到分钟，date_trunc('minute', ...)
    bucket_minutes SMALLINT NOT NULL DEFAULT 10,

    -- 成功 / 失败计数（仅算 5xx；4xx、排除类不计入分母）
    success_count  INT NOT NULL DEFAULT 0,   -- usage_logs 写入行数（排除探测 key + nginx 伪造响应）
    error_5xx_count INT NOT NULL DEFAULT 0,  -- ops_error_logs 中 status_code>=500 且非排除类

    -- 便于告警和展示的冗余派生列（写入时计算，免得查询层每次除）
    total_count INT NOT NULL DEFAULT 0,      -- success_count + error_5xx_count
    success_rate NUMERIC(5,4),               -- success_count / total_count，NULL when total=0

    -- 元信息
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_cmgs_bucket UNIQUE (group_id, bucket_start, bucket_minutes)
);

CREATE INDEX IF NOT EXISTS idx_cmgs_bucket_start
    ON channel_monitor_group_stats (bucket_start DESC);

-- channel_monitors 加 group_id 列，关联 groups 表。NULL = 纯探测模式（无真实流量统计）。
ALTER TABLE channel_monitors
    ADD COLUMN IF NOT EXISTS group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_cm_group_id
    ON channel_monitors (group_id)
    WHERE group_id IS NOT NULL;
