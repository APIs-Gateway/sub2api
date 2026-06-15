-- Migration: 154_channel_monitor_response_format
-- 为渠道监控和请求模板增加响应解析模式 response_format：
--   json -> 默认；把上游响应当作单个 JSON 文档，用 textPath 抽取文本（现有行为）
--   sse  -> 把上游响应当作 SSE 事件流逐行聚合文本（与「测试账号连接」一致），
--           用于 stream:true 返回事件流、单次 JSON 抽取得到空文本的场景
-- 历史数据默认 json，保持现有监控行为不变。

ALTER TABLE channel_monitors
    ADD COLUMN IF NOT EXISTS response_format VARCHAR(10) NOT NULL DEFAULT 'json';

ALTER TABLE channel_monitor_request_templates
    ADD COLUMN IF NOT EXISTS response_format VARCHAR(10) NOT NULL DEFAULT 'json';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_name = 'channel_monitors_response_format_check'
          AND table_name = 'channel_monitors'
    ) THEN
        ALTER TABLE channel_monitors
            ADD CONSTRAINT channel_monitors_response_format_check
            CHECK (response_format IN ('json', 'sse'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_name = 'channel_monitor_request_templates_response_format_check'
          AND table_name = 'channel_monitor_request_templates'
    ) THEN
        ALTER TABLE channel_monitor_request_templates
            ADD CONSTRAINT channel_monitor_request_templates_response_format_check
            CHECK (response_format IN ('json', 'sse'));
    END IF;
END $$;
