-- MySQL variant of the group image-generation auth-cache invalidation trigger.

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
       OR NOT (OLD.allow_image_generation <=> NEW.allow_image_generation)
       OR NOT (OLD.deleted_at <=> NEW.deleted_at));
