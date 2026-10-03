-- Legacy and manually created rows receive no image exception. Do not infer provenance.
ALTER TABLE groups ADD COLUMN simple_mode_auto_image_eligible INTEGER NOT NULL DEFAULT 0;

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
           OR OLD.deleted_at IS NOT NEW.deleted_at
           OR OLD.allow_image_generation IS NOT NEW.allow_image_generation
           OR OLD.simple_mode_auto_image_eligible IS NOT NEW.simple_mode_auto_image_eligible);
END;

