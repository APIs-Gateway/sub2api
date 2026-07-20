-- Persist the operation identity used to recover an admin group duplication.
ALTER TABLE groups
    ADD COLUMN duplicate_operation_id VARCHAR(64);

CREATE UNIQUE INDEX idx_groups_duplicate_operation_id
    ON groups (duplicate_operation_id);
