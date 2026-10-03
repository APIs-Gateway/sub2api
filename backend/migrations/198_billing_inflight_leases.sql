-- Bounded admission leases only; settled costs still use the existing billing transaction.
CREATE TABLE billing_inflight_leases (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    owner_id TEXT,
    phase TEXT NOT NULL CHECK (phase IN ('owner', 'attempt', 'pending', 'settled')),
    amount NUMERIC(20,8) NOT NULL CHECK (amount >= 0),
    estimate NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (estimate >= 0),
    known_charge NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (known_charge >= 0),
    exclusive BOOLEAN NOT NULL DEFAULT FALSE,
    request_id TEXT,
    api_key_id BIGINT,
    request_fingerprint TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_id, request_id, api_key_id)
);
CREATE INDEX billing_inflight_leases_user_expiry ON billing_inflight_leases(user_id, expires_at);
CREATE INDEX billing_inflight_leases_owner ON billing_inflight_leases(owner_id);
