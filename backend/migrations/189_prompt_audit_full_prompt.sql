-- Retain the full (unredacted) prompt text on prompt_audit_events so admins
-- can review the exact content that triggered a finding, but only when an
-- operator has explicitly opted in via the fork-native
-- security_audit.store_full_prompts setting (default off). Scoped to events
-- only: prompt_audit_jobs keeps storing redacted metadata only.
-- schema_migrations makes this migration idempotent; keep the ALTER TABLE
-- syntax portable, since SQLite and MySQL 5.7 do not support
-- ADD COLUMN IF NOT EXISTS (see 177_add_subscription_plan_currency.sql and
-- 178_usage_log_image_input_tokens.sql for the same pattern).
ALTER TABLE prompt_audit_events ADD COLUMN full_prompt TEXT NOT NULL DEFAULT '';
