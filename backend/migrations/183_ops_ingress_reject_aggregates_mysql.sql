-- MySQL 5.7.8+ variant of the ops_ingress_reject_aggregates rollup table.
-- No triggers are needed here (unlike 184_auth_cache_invalidation_outbox): the
-- application aggregator in internal/service/ops_ingress_reject.go issues the
-- upserts directly via internal/repository/ops_ingress_reject_repo.go.
--
-- client_ip is written already masked to a coarse network prefix (IPv4 /24, IPv6
-- /64) by the aggregator; this table never stores a raw per-device address, request
-- body, header, or credential.

CREATE TABLE IF NOT EXISTS ops_ingress_reject_aggregates (
    id             BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    bucket_start   TIMESTAMP NOT NULL,
    reject_reason  VARCHAR(64) NOT NULL,
    route_family   VARCHAR(64) NOT NULL,
    protocol       VARCHAR(32) NOT NULL,
    client_ip      VARCHAR(64) NOT NULL DEFAULT '',
    user_id        BIGINT NOT NULL DEFAULT 0,
    api_key_id     BIGINT NOT NULL DEFAULT 0,
    request_count  BIGINT NOT NULL DEFAULT 0,
    first_seen     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_ops_ingress_reject_aggregates_dimensions
        (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id),
    KEY idx_ops_ingress_reject_aggregates_bucket (bucket_start),
    KEY idx_ops_ingress_reject_aggregates_reason_bucket (reject_reason, bucket_start),
    KEY idx_ops_ingress_reject_aggregates_ip_bucket (client_ip, bucket_start)
);
