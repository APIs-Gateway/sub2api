-- 156_api_key_stable_priority.sql
-- per-key 稳定优先：每把 API Key 独立的稳定优先开关。
-- 与用户级 users.stable_priority_enabled 取「或」生效（任一开即对该 key 的请求启用跨分组逐档兜底）。
-- 幂等：ADD COLUMN IF NOT EXISTS。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS stable_priority_enabled BOOLEAN NOT NULL DEFAULT FALSE;
COMMENT ON COLUMN api_keys.stable_priority_enabled IS 'per-key 稳定优先开关；与 users.stable_priority_enabled 取或生效';
