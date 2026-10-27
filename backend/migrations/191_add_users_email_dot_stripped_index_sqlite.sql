-- SQLite counterpart of the PostgreSQL 191 expression index.  The expression
-- must remain aligned with dotStrippedEmailExpr in internal/repository/user_repo.go
-- so alias equality and anchored LIKE probes can use it.
CREATE INDEX IF NOT EXISTS idx_users_email_dot_stripped
    ON users (REPLACE(LOWER(TRIM(email)), '.', ''));
