-- Machine credentials for the admin API ("admin tokens").
--
-- A token is `s2a_` + base64url(32 random bytes). Only the SHA-256 of the full
-- token (hex) is stored; the plaintext is returned exactly once, at creation.
-- token_prefix keeps the first 8 plaintext characters so humans can recognise a
-- token in the UI without being able to use it.
--
-- scope is a strict ladder: read < write < danger. Which admin route needs
-- which scope is decided in code (internal/server/middleware/admin_token_scope.go),
-- not in this table.
--
-- Tokens act as `acting_user_id` (an administrator). Deleting that user removes
-- the token; deleting the user that created a token only clears created_by_user_id.
-- Tokens are never physically deleted by the application: revoking sets
-- revoked_at so the audit trail keeps resolving token ids.
--
-- expires_at is mandatory and capped at 90 days from creation by the
-- application (service.AdminTokenMaxLifetime); the cap is deliberately not a
-- CHECK constraint so a small clock skew between app and database cannot make
-- an otherwise valid insert fail.
CREATE TABLE IF NOT EXISTS admin_tokens (
    id                 BIGSERIAL PRIMARY KEY,
    name               VARCHAR(100) NOT NULL,
    token_hash         VARCHAR(64) NOT NULL,
    token_prefix       VARCHAR(16) NOT NULL,
    scope              VARCHAR(16) NOT NULL,
    acting_user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_by_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    ip_allowlist       TEXT[] NOT NULL DEFAULT '{}',
    expires_at         TIMESTAMPTZ NOT NULL,
    revoked_at         TIMESTAMPTZ NULL,
    last_used_at       TIMESTAMPTZ NULL,
    last_used_ip       VARCHAR(64) NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT admin_tokens_scope_check CHECK (scope IN ('read', 'write', 'danger'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_admin_tokens_token_hash
    ON admin_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_admin_tokens_acting_user_id
    ON admin_tokens (acting_user_id);
