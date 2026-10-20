-- MySQL 5.7.8+ variant of the durable auth-cache invalidation outbox.
-- SHA2(..., 256) is used because MySQL has no PostgreSQL encode/convert_to.
-- Keep each trigger body to one INSERT statement so the migration runner can
-- execute statements independently without requiring multiStatements in DSN.

CREATE TABLE IF NOT EXISTS auth_cache_invalidation_outbox (
    id             BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    cache_key      CHAR(64) NOT NULL,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    available_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivery_stage SMALLINT NOT NULL DEFAULT 0,
    attempts       INT NOT NULL DEFAULT 0,
    last_error     TEXT NULL,
    claimed_at     TIMESTAMP NULL DEFAULT NULL,
    claimed_by     VARCHAR(255) NULL,
    KEY idx_auth_cache_invalidation_outbox_available (available_at, id),
    KEY idx_auth_cache_invalidation_outbox_lease (claimed_at),
    KEY idx_auth_cache_invalidation_outbox_cache_key (cache_key),
    KEY idx_auth_cache_invalidation_outbox_created_at (created_at)
);

DROP TRIGGER IF EXISTS trg_api_keys_auth_cache_invalidation;
CREATE TRIGGER trg_api_keys_auth_cache_invalidation
AFTER UPDATE ON api_keys
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(OLD.`key`, 256))
FROM DUAL
WHERE OLD.`key` IS NOT NULL
  AND OLD.`key` <> ''
  AND (NOT (OLD.`key` <=> NEW.`key`)
       OR NOT (OLD.status <=> NEW.status)
       OR NOT (OLD.deleted_at <=> NEW.deleted_at)
       OR NOT (OLD.user_id <=> NEW.user_id)
       OR NOT (OLD.group_id <=> NEW.group_id)
       OR NOT (OLD.ip_whitelist <=> NEW.ip_whitelist)
       OR NOT (OLD.ip_blacklist <=> NEW.ip_blacklist)
       OR NOT (OLD.expires_at <=> NEW.expires_at))
UNION ALL
SELECT LOWER(SHA2(NEW.`key`, 256))
FROM DUAL
WHERE NEW.deleted_at IS NULL
  AND NEW.`key` IS NOT NULL
  AND NEW.`key` <> ''
  AND NOT (OLD.`key` <=> NEW.`key`);

DROP TRIGGER IF EXISTS trg_api_keys_auth_cache_invalidation_delete;
CREATE TRIGGER trg_api_keys_auth_cache_invalidation_delete
AFTER DELETE ON api_keys
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(OLD.`key`, 256))
WHERE OLD.`key` IS NOT NULL AND OLD.`key` <> '';

DROP TRIGGER IF EXISTS trg_users_auth_cache_invalidation;
CREATE TRIGGER trg_users_auth_cache_invalidation
AFTER UPDATE ON users
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(k.`key`, 256))
FROM api_keys AS k
WHERE k.user_id = OLD.id
  AND k.deleted_at IS NULL
  AND k.`key` <> ''
  AND (NOT (OLD.status <=> NEW.status)
       OR NOT (OLD.role <=> NEW.role)
       OR NOT (OLD.deleted_at <=> NEW.deleted_at));

DROP TRIGGER IF EXISTS trg_users_auth_cache_invalidation_delete;
CREATE TRIGGER trg_users_auth_cache_invalidation_delete
AFTER DELETE ON users
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(k.`key`, 256))
FROM api_keys AS k
WHERE k.user_id = OLD.id
  AND k.deleted_at IS NULL
  AND k.`key` <> '';

DROP TRIGGER IF EXISTS trg_groups_auth_cache_invalidation;
CREATE TRIGGER trg_groups_auth_cache_invalidation
AFTER UPDATE ON groups
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(k.`key`, 256))
FROM api_keys AS k
WHERE k.group_id = OLD.id
  AND k.deleted_at IS NULL
  AND k.`key` <> ''
  AND (NOT (OLD.status <=> NEW.status)
       OR NOT (OLD.is_exclusive <=> NEW.is_exclusive)
       OR NOT (OLD.deleted_at <=> NEW.deleted_at));

DROP TRIGGER IF EXISTS trg_groups_auth_cache_invalidation_delete;
CREATE TRIGGER trg_groups_auth_cache_invalidation_delete
AFTER DELETE ON groups
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(k.`key`, 256))
FROM api_keys AS k
WHERE k.group_id = OLD.id
  AND k.deleted_at IS NULL
  AND k.`key` <> '';

DROP TRIGGER IF EXISTS trg_user_allowed_groups_auth_cache_invalidation;
CREATE TRIGGER trg_user_allowed_groups_auth_cache_invalidation
AFTER INSERT ON user_allowed_groups
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(k.`key`, 256))
FROM api_keys AS k
JOIN groups AS g ON g.id = NEW.group_id AND g.is_exclusive = 1
WHERE k.user_id = NEW.user_id
  AND k.group_id = NEW.group_id
  AND k.deleted_at IS NULL
  AND k.`key` <> '';

DROP TRIGGER IF EXISTS trg_user_allowed_groups_auth_cache_invalidation_update;
CREATE TRIGGER trg_user_allowed_groups_auth_cache_invalidation_update
AFTER UPDATE ON user_allowed_groups
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(k.`key`, 256))
FROM api_keys AS k
JOIN groups AS g ON g.id = OLD.group_id AND g.is_exclusive = 1
WHERE k.user_id = OLD.user_id
  AND k.group_id = OLD.group_id
  AND k.deleted_at IS NULL
  AND k.`key` <> ''
  AND (NOT (OLD.user_id <=> NEW.user_id) OR NOT (OLD.group_id <=> NEW.group_id))
UNION ALL
SELECT LOWER(SHA2(k.`key`, 256))
FROM api_keys AS k
JOIN groups AS g ON g.id = NEW.group_id AND g.is_exclusive = 1
WHERE k.user_id = NEW.user_id
  AND k.group_id = NEW.group_id
  AND k.deleted_at IS NULL
  AND k.`key` <> ''
  AND (NOT (OLD.user_id <=> NEW.user_id) OR NOT (OLD.group_id <=> NEW.group_id));

DROP TRIGGER IF EXISTS trg_user_allowed_groups_auth_cache_invalidation_delete;
CREATE TRIGGER trg_user_allowed_groups_auth_cache_invalidation_delete
AFTER DELETE ON user_allowed_groups
FOR EACH ROW
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT LOWER(SHA2(k.`key`, 256))
FROM api_keys AS k
JOIN groups AS g ON g.id = OLD.group_id AND g.is_exclusive = 1
WHERE k.user_id = OLD.user_id
  AND k.group_id = OLD.group_id
  AND k.deleted_at IS NULL
  AND k.`key` <> '';
