-- Extend audit_logs so every state-changing admin request can be attributed.
--
-- Existing columns are reused where the meaning matches: latency_ms is the
-- request duration, method/path/client_ip/user_agent/request_body/status_code
-- keep their meaning, and request_body only ever holds a redacted copy.
-- New columns:
--   actor_label  who acted, e.g. "jwt:a@b.c", "token:ops-bot#7", "legacy_api_key"
--   auth_kind    jwt | admin_token | legacy_api_key
--   token_id     admin_tokens.id for admin_token requests (no FK: audit rows
--                must survive whatever happens to the token row)
--   route        gin route template, e.g. /api/v1/admin/users/:id/balance
--   target_type  collection the request addressed, derived from the route
--   target_id    value of the first path parameter
--   reason       X-Reason supplied by the caller
--   before/after reserved for state snapshots; currently left NULL
ALTER TABLE audit_logs
    ADD COLUMN IF NOT EXISTS actor_label VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS auth_kind VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS token_id BIGINT NULL,
    ADD COLUMN IF NOT EXISTS route VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS target_type VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS target_id VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS "before" JSONB NULL,
    ADD COLUMN IF NOT EXISTS "after" JSONB NULL;

-- Route prefix filter (route LIKE '/api/v1/admin/users%') newest first.
CREATE INDEX IF NOT EXISTS idx_audit_logs_route_created
    ON audit_logs (route varchar_pattern_ops, created_at DESC);
-- "What did this token do?"
CREATE INDEX IF NOT EXISTS idx_audit_logs_token_created
    ON audit_logs (token_id, created_at DESC)
    WHERE token_id IS NOT NULL;
-- "What happened to this object?"
CREATE INDEX IF NOT EXISTS idx_audit_logs_target_created
    ON audit_logs (target_type, target_id, created_at DESC)
    WHERE target_type <> '';
