-- SQLite variant of the ops_ingress_reject_aggregates rollup table.
-- No triggers are needed here: the application aggregator in
-- internal/service/ops_ingress_reject.go issues the upserts directly via
-- internal/repository/ops_ingress_reject_repo.go.
--
-- client_ip is written already masked to a coarse network prefix (IPv4 /24, IPv6
-- /64) by the aggregator; this table never stores a raw per-device address, request
-- body, header, or credential.

CREATE TABLE IF NOT EXISTS ops_ingress_reject_aggregates (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    bucket_start   TIMESTAMP NOT NULL,
    reject_reason  TEXT NOT NULL,
    route_family   TEXT NOT NULL,
    protocol       TEXT NOT NULL,
    client_ip      TEXT NOT NULL DEFAULT '',
    user_id        INTEGER NOT NULL DEFAULT 0,
    api_key_id     INTEGER NOT NULL DEFAULT 0,
    request_count  INTEGER NOT NULL DEFAULT 0,
    first_seen     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)
);

CREATE INDEX IF NOT EXISTS idx_ops_ingress_reject_aggregates_bucket
    ON ops_ingress_reject_aggregates (bucket_start);
CREATE INDEX IF NOT EXISTS idx_ops_ingress_reject_aggregates_reason_bucket
    ON ops_ingress_reject_aggregates (reject_reason, bucket_start);
CREATE INDEX IF NOT EXISTS idx_ops_ingress_reject_aggregates_ip_bucket
    ON ops_ingress_reject_aggregates (client_ip, bucket_start);
