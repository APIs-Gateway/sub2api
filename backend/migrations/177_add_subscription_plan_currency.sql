-- Display-only ISO 4217 currency label for subscription plan prices; empty
-- keeps existing plans rendering without any label.
-- The migration runner records this file in schema_migrations, so the
-- dialect-neutral ADD COLUMN form is sufficient and keeps MySQL/SQLite
-- compatibility instead of relying on PostgreSQL-only IF NOT EXISTS syntax.
ALTER TABLE subscription_plans
    ADD COLUMN currency VARCHAR(3) NOT NULL DEFAULT '';
