-- SQLite variant of the durable auth-cache invalidation outbox.
-- The application registers deterministic sha256(text) for the SQLite driver;
-- plaintext API keys are never inserted into the outbox.

CREATE TABLE IF NOT EXISTS auth_cache_invalidation_outbox (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    cache_key      TEXT NOT NULL CHECK (
        length(cache_key) = 64 AND cache_key NOT GLOB '*[^0-9a-f]*'
    ),
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    available_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivery_stage INTEGER NOT NULL DEFAULT 0 CHECK (delivery_stage IN (0, 1)),
    attempts       INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error     TEXT,
    claimed_at     TIMESTAMP,
    claimed_by     TEXT
);

CREATE INDEX IF NOT EXISTS idx_auth_cache_invalidation_outbox_available
    ON auth_cache_invalidation_outbox (available_at, id);
CREATE INDEX IF NOT EXISTS idx_auth_cache_invalidation_outbox_lease
    ON auth_cache_invalidation_outbox (claimed_at);
CREATE INDEX IF NOT EXISTS idx_auth_cache_invalidation_outbox_cache_key
    ON auth_cache_invalidation_outbox (cache_key);
CREATE INDEX IF NOT EXISTS idx_auth_cache_invalidation_outbox_created_at
    ON auth_cache_invalidation_outbox (created_at);

DROP TRIGGER IF EXISTS trg_api_keys_auth_cache_invalidation;
DROP TRIGGER IF EXISTS trg_api_keys_auth_cache_invalidation_delete;
CREATE TRIGGER trg_api_keys_auth_cache_invalidation
AFTER UPDATE ON api_keys
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(OLD.key)
    WHERE OLD.key IS NOT NULL
      AND OLD.key <> ''
      AND (OLD.key IS NOT NEW.key
           OR OLD.status IS NOT NEW.status
           OR OLD.deleted_at IS NOT NEW.deleted_at
           OR OLD.user_id IS NOT NEW.user_id
           OR OLD.group_id IS NOT NEW.group_id
           OR OLD.ip_whitelist IS NOT NEW.ip_whitelist
           OR OLD.ip_blacklist IS NOT NEW.ip_blacklist
           OR OLD.expires_at IS NOT NEW.expires_at);
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(NEW.key)
    WHERE NEW.deleted_at IS NULL
      AND NEW.key IS NOT NULL
      AND NEW.key <> ''
      AND OLD.key IS NOT NEW.key;
END;

CREATE TRIGGER trg_api_keys_auth_cache_invalidation_delete
AFTER DELETE ON api_keys
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(OLD.key)
    WHERE OLD.key IS NOT NULL AND OLD.key <> '';
END;

DROP TRIGGER IF EXISTS trg_users_auth_cache_invalidation;
DROP TRIGGER IF EXISTS trg_users_auth_cache_invalidation_delete;
CREATE TRIGGER trg_users_auth_cache_invalidation
AFTER UPDATE ON users
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(k.key)
    FROM api_keys AS k
    WHERE k.user_id = OLD.id
      AND k.deleted_at IS NULL
      AND k.key <> ''
      AND (OLD.status IS NOT NEW.status
           OR OLD.role IS NOT NEW.role
           OR OLD.deleted_at IS NOT NEW.deleted_at);
END;

CREATE TRIGGER trg_users_auth_cache_invalidation_delete
AFTER DELETE ON users
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(k.key)
    FROM api_keys AS k
    WHERE k.user_id = OLD.id
      AND k.deleted_at IS NULL
      AND k.key <> '';
END;

DROP TRIGGER IF EXISTS trg_groups_auth_cache_invalidation;
DROP TRIGGER IF EXISTS trg_groups_auth_cache_invalidation_delete;
CREATE TRIGGER trg_groups_auth_cache_invalidation
AFTER UPDATE ON groups
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(k.key)
    FROM api_keys AS k
    WHERE k.group_id = OLD.id
      AND k.deleted_at IS NULL
      AND k.key <> ''
      AND (OLD.status IS NOT NEW.status
           OR OLD.is_exclusive IS NOT NEW.is_exclusive
           OR OLD.deleted_at IS NOT NEW.deleted_at);
END;

CREATE TRIGGER trg_groups_auth_cache_invalidation_delete
AFTER DELETE ON groups
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(k.key)
    FROM api_keys AS k
    WHERE k.group_id = OLD.id
      AND k.deleted_at IS NULL
      AND k.key <> '';
END;

DROP TRIGGER IF EXISTS trg_user_allowed_groups_auth_cache_invalidation;
DROP TRIGGER IF EXISTS trg_user_allowed_groups_auth_cache_invalidation_update;
CREATE TRIGGER trg_user_allowed_groups_auth_cache_invalidation
AFTER INSERT ON user_allowed_groups
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(k.key)
    FROM api_keys AS k
    JOIN groups AS g ON g.id = NEW.group_id AND g.is_exclusive = 1
    WHERE k.user_id = NEW.user_id
      AND k.group_id = NEW.group_id
      AND k.deleted_at IS NULL
      AND k.key <> '';
END;

CREATE TRIGGER trg_user_allowed_groups_auth_cache_invalidation_update
AFTER UPDATE ON user_allowed_groups
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(k.key)
    FROM api_keys AS k
    JOIN groups AS g ON g.id = OLD.group_id AND g.is_exclusive = 1
    WHERE k.user_id = OLD.user_id
      AND k.group_id = OLD.group_id
      AND k.deleted_at IS NULL
      AND k.key <> ''
      AND (OLD.user_id IS NOT NEW.user_id OR OLD.group_id IS NOT NEW.group_id);
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(k.key)
    FROM api_keys AS k
    JOIN groups AS g ON g.id = NEW.group_id AND g.is_exclusive = 1
    WHERE k.user_id = NEW.user_id
      AND k.group_id = NEW.group_id
      AND k.deleted_at IS NULL
      AND k.key <> ''
      AND (OLD.user_id IS NOT NEW.user_id OR OLD.group_id IS NOT NEW.group_id);
END;

CREATE TRIGGER trg_user_allowed_groups_auth_cache_invalidation_delete
AFTER DELETE ON user_allowed_groups
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT sha256(k.key)
    FROM api_keys AS k
    JOIN groups AS g ON g.id = OLD.group_id AND g.is_exclusive = 1
    WHERE k.user_id = OLD.user_id
      AND k.group_id = OLD.group_id
      AND k.deleted_at IS NULL
      AND k.key <> '';
END;
