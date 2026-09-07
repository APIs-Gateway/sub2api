-- Minute-bucketed rollup of ingress-layer request rejections (auth/routing gate
-- failures such as invalid_api_key, ip_restricted, group_disabled, ...), grouped by
-- reject_reason / route_family / protocol / client_ip / user_id / api_key_id.
--
-- This table stores aggregate counters only: no request bodies, headers, or
-- credentials are ever persisted here. client_ip is written already masked to a
-- coarse network prefix (IPv4 /24, IPv6 /64) by the aggregator in
-- internal/service/ops_ingress_reject.go before it reaches this layer, so this
-- table never holds a raw per-device address either. See
-- internal/repository/ops_ingress_reject_repo.go for the writer/reader and
-- internal/handler/admin/ops_ingress_reject_handler.go for the read-only admin API.
--
-- Postgres is the primary target for the ops_* subsystem (see ops_repo.go, which is
-- already Postgres-only). MySQL/SQLite variants of this migration exist purely so
-- migration replay does not fail on non-Postgres deployments; see
-- 183_ops_ingress_reject_aggregates_mysql.sql / _sqlite.sql.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS ops_ingress_reject_aggregates (
    id             BIGSERIAL PRIMARY KEY,
    bucket_start   TIMESTAMPTZ NOT NULL,
    reject_reason  VARCHAR(64) NOT NULL,
    route_family   VARCHAR(64) NOT NULL,
    protocol       VARCHAR(32) NOT NULL,
    client_ip      VARCHAR(64) NOT NULL DEFAULT '',
    user_id        BIGINT NOT NULL DEFAULT 0,
    api_key_id     BIGINT NOT NULL DEFAULT 0,
    request_count  BIGINT NOT NULL DEFAULT 0,
    first_seen     TIMESTAMPTZ NOT NULL,
    last_seen      TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ops_ingress_reject_aggregates_dimensions_unique UNIQUE
        (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)
);

CREATE INDEX IF NOT EXISTS idx_ops_ingress_reject_aggregates_bucket
    ON ops_ingress_reject_aggregates (bucket_start DESC);
CREATE INDEX IF NOT EXISTS idx_ops_ingress_reject_aggregates_reason_bucket
    ON ops_ingress_reject_aggregates (reject_reason, bucket_start DESC);
CREATE INDEX IF NOT EXISTS idx_ops_ingress_reject_aggregates_ip_bucket
    ON ops_ingress_reject_aggregates (client_ip, bucket_start DESC);

COMMENT ON TABLE ops_ingress_reject_aggregates IS
    'Minute-bucketed ingress reject counters; client_ip is pre-masked to a network prefix, never a raw per-device address';
COMMENT ON COLUMN ops_ingress_reject_aggregates.client_ip IS
    'Network-prefix masked (IPv4 /24, IPv6 /64) by the aggregator before insert; never the raw client address';
