-- PostgreSQL expression index for the legacy Gmail/Googlemail alias probe in
-- internal/repository/user_repo.go.  Keep this expression byte-for-byte aligned
-- with dotStrippedEmailExpr so equality and anchored LIKE probes remain SARGable.
--
-- This is intentionally a non-transactional migration: CONCURRENTLY avoids
-- blocking writes to users during rollout.  SQLite has an equivalent migration
-- in 191_add_users_email_dot_stripped_index_sqlite.sql. MySQL 5.7 has no
-- functional-index syntax and must not attempt to execute this PostgreSQL DDL.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email_dot_stripped
    ON users (REPLACE(LOWER(TRIM(email)), '.', ''));
