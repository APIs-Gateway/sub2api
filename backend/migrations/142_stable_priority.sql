-- 稳定优先（Stable Priority）
-- users.stable_priority_enabled: 用户级全局自助开关，默认关闭。开启后所在组渠道全挂时跨分组逐档兜底。
-- groups.stable_priority_fallback_group_id: 稳定优先下一档兜底目标分组（构成多档链），仅 openai 平台生效。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS stable_priority_enabled BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS stable_priority_fallback_group_id BIGINT;
