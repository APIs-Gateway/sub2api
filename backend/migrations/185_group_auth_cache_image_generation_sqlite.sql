-- SQLite variant of the group image-generation auth-cache invalidation trigger.

DROP TRIGGER IF EXISTS trg_groups_auth_cache_invalidation;
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
           OR OLD.allow_image_generation IS NOT NEW.allow_image_generation
           OR OLD.deleted_at IS NOT NEW.deleted_at);
END;
