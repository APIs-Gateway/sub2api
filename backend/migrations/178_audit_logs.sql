-- Append-only security audit records. Do not store raw credentials or bodies.
CREATE TABLE IF NOT EXISTS audit_log_id_sequence (
    id BIGINT NOT NULL
);

INSERT INTO audit_log_id_sequence (id)
SELECT 0
WHERE NOT EXISTS (SELECT 1 FROM audit_log_id_sequence);

CREATE TABLE IF NOT EXISTS audit_logs (
    id BIGINT PRIMARY KEY,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor_user_id BIGINT NULL,
    actor_email VARCHAR(255) NOT NULL DEFAULT '',
    actor_role VARCHAR(32) NOT NULL DEFAULT '',
    auth_method VARCHAR(32) NOT NULL DEFAULT '',
    credential_masked VARCHAR(160) NOT NULL DEFAULT '',
    action VARCHAR(128) NOT NULL DEFAULT '',
    method VARCHAR(16) NOT NULL DEFAULT '',
    path VARCHAR(512) NOT NULL DEFAULT '',
    request_id VARCHAR(128) NOT NULL DEFAULT '',
    client_ip VARCHAR(64) NOT NULL DEFAULT '',
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    request_body TEXT NOT NULL,
    status_code INT NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    extra TEXT NOT NULL
);

CREATE INDEX idx_audit_logs_created_at_id
    ON audit_logs (created_at, id);
CREATE INDEX idx_audit_logs_actor_created
    ON audit_logs (actor_user_id, created_at);
CREATE INDEX idx_audit_logs_action
    ON audit_logs (action);
CREATE INDEX idx_audit_logs_client_ip
    ON audit_logs (client_ip);
