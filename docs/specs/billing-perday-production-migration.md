# Billing Per-Day Redesign Production Migration Spec

## 0. One Sentence

Ship the billing per-day redesign to the production Sub2API instance with a forward-only, auditable rollout: verify the PR is green, back up production, deploy the embedded binary, let migrations `160`-`165` run on startup, verify the three-window subscription state, and postpone destructive wallet balance de-mixing until a separate dry-run/backfill window.

## 1. Scope

This spec covers the first production rollout of PR `APIs-Gateway/sub2api#8` from branch `feat/billing-perday-redesign`.

In scope:

- Deploy the new binary and frontend bundle.
- Apply embedded SQL migrations that have not yet run in production.
- Verify subscription cards have per-card daily/weekly/monthly limits after migration.
- Verify payment/refund/subscription admin surfaces are operational after deploy.
- Keep rollback possible for binary-level failure.

Out of scope:

- Directly publishing production hostnames, credentials, SSH targets, or private deployment paths.
- Executing destructive wallet balance de-mixing.
- Dropping legacy burn-down columns or group subscription columns.
- Rewriting payment provider configuration; provider credentials remain environment data.

## 2. Current Production Snapshot

Captured by read-only production checks on 2026-07-04:

| Item | Observed value |
|---|---:|
| Current deployed binary | `Sub2API 1.3.21`, commit `458d8386`, built `2026-07-03T12:50:00Z` |
| Latest applied migration | `160_normalize_subscription_overdraft_settings.sql` |
| `schema_migrations` rows | `203` |
| Pending PR #8 migrations | `160_subscription_perday_fields.sql` through `165_subscription_three_window_backfill.sql` |
| Active subscriptions | `176` |
| Active but expired subscriptions | `0` |
| Active cards with `daily_amount_usd > 0` | `176` |
| Active daily amount range | `30` to `90` |
| Payment provider instances | `0` |
| Pending or refund-requested payment orders | `0` |
| `user_subscriptions.group_id` | `NOT NULL` before migration `164` |
| Existing card limit columns | absent before migration `163` |

Interpretation:

- Production is exactly before the PR #8 migration batch.
- The pending batch is 6 files: `160_subscription_perday_fields.sql` plus `161`-`165`.
- Migration `165` will touch the 176 active paid cards by filling `daily_limit_usd`, `weekly_limit_usd`, and `monthly_limit_usd`.
- Because provider instances are empty, payment provider configuration must be restored/created after deployment before live checkout traffic is expected.
- There are no currently pending/refund-requested orders that need operator triage before restart.

## 3. Migration Inventory

Migrations are executed automatically on service startup by the custom runner in `backend/internal/repository/migrations_runner.go`.

The runner:

- sorts embedded `.sql` files by filename;
- records by full `filename` in `schema_migrations`;
- validates SHA256 checksums for already-applied files;
- wraps regular migrations in a transaction;
- serializes concurrent migration runners with a PostgreSQL advisory lock.

PR #8 relevant pending migrations:

| File | Type | Effect | Risk |
|---|---|---|---|
| `160_subscription_perday_fields.sql` | additive | Adds per-day/user monthly overdraft fields used by the redesign. | Low, additive; production already has a different `160_*`, duplicate number is safe because tracking is by full filename. |
| `161_subscription_overdraft_on.sql` | additive | Adds `user_subscriptions.overdraft_on boolean default false`. | Low, additive; legacy-compatible. |
| `162_user_last_change_plan_day.sql` | additive | Adds `users.last_change_plan_day` for one change-plan action per East-Asia natural day. | Low, additive. |
| `163_subscription_card_limits.sql` | additive | Adds nullable card-level `daily_limit_usd`, `weekly_limit_usd`, `monthly_limit_usd`. | Low, additive. |
| `164_subscription_group_id_nullable.sql` | schema change | Allows custom subscription cards with `group_id NULL`; changes FK to `ON DELETE SET NULL`. | Medium; verify raw SQL scans and UI tolerate null group. Covered by local tests and nullable-column note. |
| `165_subscription_three_window_backfill.sql` | data backfill | Fills limits for active legacy cards: `D`, `7D`, `min(T,30)D`. | Medium; affects active cards, but is idempotent and does not change usage, balance, status, or expiry. |

## 4. Rollout Gates

Do not start production deployment unless all gates pass:

- PR is mergeable and the latest head commit has green required checks.
- Local full verify has passed with:
  - backend build;
  - wireinject compile;
  - go vet;
  - `go test -tags unit ./...`;
  - frontend lint/typecheck/critical vitest;
  - secret scan.
- Production read-only preflight confirms:
  - current latest migration is still before `160_subscription_perday_fields.sql`;
  - no pending or refund-requested payment orders;
  - PostgreSQL, Redis, nginx, and the app service are active;
  - a database backup has completed and the restore artifact is accessible.
- Legacy active subscription inventory confirms either:
  - every active subscribed user has at most one active, non-expired card; or
  - all multi-card users have an approved normalization report and have been consolidated before traffic is restored.
- Operator has a rollback binary path and understands that DB migrations are forward-only.

## 5. Legacy Subscription Inventory

PR #8 moves runtime semantics to a single active subscription card per user. Historical data was group-oriented: a user could hold one active card per group, and older repository helpers deliberately picked the latest row when multiple rows existed. That means historical users who bought multiple daily cards, multiple monthly cards, or cards across several groups need explicit inventory before deployment.

Run these read-only checks immediately before deploy:

```sql
WITH active_cards AS (
  SELECT id,
         user_id,
         group_id,
         daily_amount_usd,
         granted_total_usd,
         consumed_usd,
         start_day,
         expire_day,
         starts_at,
         expires_at,
         created_at,
         CASE
           WHEN daily_amount_usd > 0 AND granted_total_usd > 0 THEN ROUND(granted_total_usd / daily_amount_usd)::int
           WHEN expire_day > 0 AND start_day > 0 THEN expire_day - start_day + 1
           ELSE CEIL(EXTRACT(EPOCH FROM (expires_at - starts_at)) / 86400.0)::int
         END AS inferred_original_days,
         GREATEST(0, CEIL(EXTRACT(EPOCH FROM (expires_at - NOW())) / 86400.0)::int) AS remaining_days
  FROM user_subscriptions
  WHERE status = 'active'
    AND deleted_at IS NULL
    AND expires_at > NOW()
    AND daily_amount_usd > 0
)
SELECT count(*) AS active_cards,
       count(DISTINCT user_id) AS active_users,
       count(*) FILTER (WHERE card_count > 1) AS multi_card_rows,
       count(DISTINCT user_id) FILTER (WHERE card_count > 1) AS multi_card_users
FROM (
  SELECT active_cards.*,
         count(*) OVER (PARTITION BY user_id) AS card_count
  FROM active_cards
) s;

WITH active_cards AS (
  SELECT user_id,
         id,
         group_id,
         daily_amount_usd,
         CASE
           WHEN daily_amount_usd > 0 AND granted_total_usd > 0 THEN ROUND(granted_total_usd / daily_amount_usd)::int
           WHEN expire_day > 0 AND start_day > 0 THEN expire_day - start_day + 1
           ELSE CEIL(EXTRACT(EPOCH FROM (expires_at - starts_at)) / 86400.0)::int
         END AS inferred_original_days,
         GREATEST(0, CEIL(EXTRACT(EPOCH FROM (expires_at - NOW())) / 86400.0)::int) AS remaining_days,
         expires_at,
         created_at
  FROM user_subscriptions
  WHERE status = 'active'
    AND deleted_at IS NULL
    AND expires_at > NOW()
    AND daily_amount_usd > 0
),
multi_users AS (
  SELECT user_id
  FROM active_cards
  GROUP BY user_id
  HAVING count(*) > 1
)
SELECT ac.*
FROM active_cards ac
JOIN multi_users mu USING (user_id)
ORDER BY ac.user_id, ac.expires_at DESC, ac.created_at DESC, ac.id DESC;

WITH active_cards AS (
  SELECT daily_amount_usd,
         CASE
           WHEN daily_amount_usd > 0 AND granted_total_usd > 0 THEN ROUND(granted_total_usd / daily_amount_usd)::int
           WHEN expire_day > 0 AND start_day > 0 THEN expire_day - start_day + 1
           ELSE CEIL(EXTRACT(EPOCH FROM (expires_at - starts_at)) / 86400.0)::int
         END AS inferred_original_days
  FROM user_subscriptions
  WHERE status = 'active'
    AND deleted_at IS NULL
    AND expires_at > NOW()
    AND daily_amount_usd > 0
)
SELECT daily_amount_usd,
       inferred_original_days,
       count(*) AS card_count
FROM active_cards
GROUP BY daily_amount_usd, inferred_original_days
ORDER BY daily_amount_usd, inferred_original_days;
```

Decision rules:

- If `multi_card_users = 0`, continue with the normal deployment path.
- If `multi_card_users > 0`, stop and produce a user-level normalization report before deployment. Do not rely on the application to automatically choose among multiple active rows, because the new runtime reads a single active card per user.
- Same-D stacked cards are safe to merge by remaining value, not by blindly summing days: compute each card's remaining value as `remaining_days * daily_amount_usd`, keep one card with the same `daily_amount_usd`, set its new remaining days to `ceil(sum(remaining_value) / daily_amount_usd)`, then close the extra rows. This covers users who bought several daily cards or several monthly cards at the same daily quota while keeping the money-state calculation explicit.
- Different-D stacked cards require operator approval. The conservative default is to keep the highest daily quota card, convert the remaining active cards into additional days at that highest daily quota by `ceil(sum(remaining_days * D) / max(D))`, and close the extra rows. This preserves at least the user's strongest daily entitlement and avoids silently dropping paid value, but it can slightly overgrant because of rounding.
- If a user's history is ambiguous, refund/credit the extra cards manually instead of auto-merging. Money-state normalization must be auditable and reversible from the backup.

After normalization, re-run the inventory query. `multi_card_users` must be `0` before traffic is restored.

## 6. Preflight

Run read-only checks immediately before deploy and save their output with the deployment record.

Required checks:

```sql
SELECT filename, applied_at
FROM schema_migrations
ORDER BY applied_at DESC
LIMIT 10;

SELECT count(*) AS active_subs,
       count(*) FILTER (WHERE expires_at > now()) AS active_not_expired,
       count(*) FILTER (WHERE expires_at <= now()) AS stale_active
FROM user_subscriptions
WHERE status = 'active' AND deleted_at IS NULL;

SELECT min(daily_amount_usd) AS min_d,
       max(daily_amount_usd) AS max_d,
       count(*) AS candidate_count
FROM user_subscriptions
WHERE status = 'active'
  AND deleted_at IS NULL
  AND daily_amount_usd > 0;

SELECT count(*)
FROM payment_orders
WHERE status IN ('pending', 'refund_requested');

SELECT count(*) AS pending_pr8_migrations
FROM (VALUES
  ('160_subscription_perday_fields.sql'),
  ('161_subscription_overdraft_on.sql'),
  ('162_user_last_change_plan_day.sql'),
  ('163_subscription_card_limits.sql'),
  ('164_subscription_group_id_nullable.sql'),
  ('165_subscription_three_window_backfill.sql')
) AS v(filename)
WHERE NOT EXISTS (
  SELECT 1
  FROM schema_migrations sm
  WHERE sm.filename = v.filename
);
```

Expected for the current production snapshot:

- latest migration is `160_normalize_subscription_overdraft_settings.sql`;
- pending PR #8 migration count is 6;
- active subscriptions are all non-expired;
- active backfill candidates match active cards with positive `daily_amount_usd`;
- pending/refund-requested orders count is 0.

If these expectations drift, stop and update this spec before deployment.

## 7. Deployment Procedure

1. Freeze payment/subscription operator changes.

   Do not manually refund, force-renew, or manually change subscriptions during deploy. The code path and migrations are short, but these are money-state surfaces.

2. Back up production.

   Take a full PostgreSQL dump plus a filesystem copy of the current binary and configuration. The database dump is mandatory because rollback for migrations is forward-only.

3. Build the release candidate.

   Build from the exact PR head commit. The production binary must embed the frontend bundle; the build must use the repository's production embed build path. Record:

   - git SHA;
   - semantic/display version;
   - build time;
   - local `sha256sum` of the binary.

4. Upload and verify the candidate binary.

   Before replacing the running binary, verify the uploaded candidate can print its version and that its checksum matches the local artifact.

5. Stop the service, replace the binary, and start the service.

   On startup the application will run the embedded migrations automatically. Watch the app journal until startup completes.

6. Verify migrations applied.

   Required SQL after startup:

   ```sql
   SELECT filename, applied_at
   FROM schema_migrations
   WHERE filename IN (
     '160_subscription_perday_fields.sql',
     '161_subscription_overdraft_on.sql',
     '162_user_last_change_plan_day.sql',
     '163_subscription_card_limits.sql',
     '164_subscription_group_id_nullable.sql',
     '165_subscription_three_window_backfill.sql'
   )
   ORDER BY filename;

   SELECT column_name, is_nullable
   FROM information_schema.columns
   WHERE table_name = 'user_subscriptions'
     AND column_name IN ('group_id','daily_limit_usd','weekly_limit_usd','monthly_limit_usd','overdraft_on')
   ORDER BY column_name;

   SELECT count(*) AS active_without_daily_limit
   FROM user_subscriptions
   WHERE status = 'active'
     AND deleted_at IS NULL
     AND daily_amount_usd > 0
     AND daily_limit_usd IS NULL;
   ```

   Expected:

   - all listed migrations have one `schema_migrations` row;
   - `group_id` is nullable;
   - card limit columns exist;
   - `active_without_daily_limit = 0`.

7. Verify app and UI health.

   Required smoke checks:

   - app health endpoint returns success;
   - login/admin shell loads embedded frontend;
   - `/payment` loads and shows recharge/subscription panels;
   - `/orders` loads with the redesigned order status style;
   - `/subscriptions` loads and current subscription cards show package details, not group/platform names as the primary plan identity;
   - admin payment settings page can list payment provider instances;
   - admin orders page can open a completed subscription order and display refund affordances.

8. Configure or restore payment provider instances.

   The current production snapshot has zero provider instances. Before live checkout testing, create or verify the intended provider instances in the admin UI/API:

   - provider key;
   - display name;
   - supported payment types;
   - enabled state;
   - refund-enabled state;
   - user-refund allowance;
   - provider config JSON.

   Do not paste provider secrets into this spec, commit history, or PR comments.

9. Run one controlled live checkout.

   Use an operator-owned account and a minimum acceptable subscription package. Verify:

   - order is created as `subscription`;
   - payment amount is CNY on the checkout surface;
   - payment callback completes fulfillment;
   - a new card has `daily_limit_usd = D`, `weekly_limit_usd = 7D`, and `monthly_limit_usd = min(T,30)D`;
   - user concurrency is at least the package-derived concurrency and does not decrease if a higher manual/admin concurrency already exists;
   - an active subscription blocks purchase of a second new package and points the user to renew/change-plan instead.

10. Unfreeze operator changes.

   Re-enable normal admin payment/refund work only after the post-deploy checks pass.

## 8. Post-Deploy Monitoring

For the first hour after deployment, monitor:

- application restart loops;
- migration checksum mismatch errors;
- payment callback errors;
- refund errors;
- `DAILY_LIMIT_EXCEEDED`, `WEEKLY_LIMIT_EXCEEDED`, `MONTHLY_LIMIT_EXCEEDED` rates;
- usage billing errors;
- frontend 404s that indicate a binary built without embedded frontend assets.

Suggested database checks:

```sql
SELECT status, count(*)
FROM payment_orders
WHERE created_at > now() - interval '1 hour'
GROUP BY status
ORDER BY status;

SELECT count(*) AS active_with_null_limits
FROM user_subscriptions
WHERE status = 'active'
  AND deleted_at IS NULL
  AND daily_amount_usd > 0
  AND (daily_limit_usd IS NULL OR weekly_limit_usd IS NULL OR monthly_limit_usd IS NULL);
```

## 9. Rollback

Binary rollback:

- Safe if startup fails before traffic is restored or if app behavior is wrong but migrations have already completed.
- Restore the previous binary backup and restart the service.
- Keep the database at the forward-migrated schema. The old binary must be compatible enough to read the additive columns; if the failure is caused by `group_id` nullability, do not continue old-binary operation with newly created `group_id NULL` cards.

Database rollback:

- There is no automatic down migration.
- Use the pre-deploy PostgreSQL dump only for severe rollout failure where data loss/incorrect money state is worse than downtime.
- Restoring the dump discards all writes after the backup; this requires an explicit operator decision and user-impact review.

Operational fallback:

- Disable payment provider instances to stop new money-path writes.
- Leave existing subscriptions active if billing remains correct.
- If subscription billing is suspect, temporarily disable affected API keys or route traffic to wallet-only users after an operator decision.

## 10. Deferred Destructive Migration

The wallet balance de-mixing described in `docs/billing-perday-prelaunch-cleanup.md` remains deferred.

Do not run it as part of this deployment because:

- it is destructive;
- it requires a production-copy dry run and per-user audit output;
- it may create negative or clamped wallet balances for historical edge cases;
- it should be run only after the service is confirmed to be using three-window subscription limits.
- legacy multi-card subscription consolidation is a separate pre-deploy normalization concern; do not hide that work inside the wallet balance de-mixing window.

Required before that separate window:

- dry-run SQL on a production copy;
- per-user delta report;
- sampling and product approval for users where `balance < subscription_remnant`;
- explicit rollback/accounting plan.

## 11. Acceptance Checklist

- [ ] Latest PR head is green in GitHub CI.
- [ ] Production DB backup completed and restore artifact location recorded privately.
- [ ] Legacy active subscription inventory has been saved.
- [ ] `multi_card_users = 0`, or every multi-card user has an approved normalization record and the inventory has been re-run cleanly.
- [ ] New binary built with embedded frontend and verified by checksum.
- [ ] Service starts successfully after replacement.
- [ ] Migrations `160_subscription_perday_fields.sql` through `165_subscription_three_window_backfill.sql` are recorded in `schema_migrations`.
- [ ] `user_subscriptions.group_id` is nullable.
- [ ] Active paid cards have non-null `daily_limit_usd`, `weekly_limit_usd`, and `monthly_limit_usd`.
- [ ] User `/payment`, `/orders`, and `/subscriptions` pages load.
- [ ] Admin payment settings, orders, and subscriptions pages load.
- [ ] Provider instances are configured before live checkout.
- [ ] Controlled live subscription checkout creates the expected three-window card.
- [ ] Active-card users cannot buy an additional new package; they can renew or change plan.
- [ ] Refund request and admin refund close the related subscription card.
- [ ] First-hour monitoring shows no migration, payment, refund, or usage-billing error spike.

## 12. Self-Review Notes

- This spec intentionally excludes secrets, hostnames, and private SSH/database commands so it can live in the public repository.
- The production snapshot is time-sensitive. Re-run preflight immediately before deployment and update the observed values if they drift.
- The binary rollback plan is not a schema rollback plan. Once `group_id NULL` cards exist, old binaries may be logically unsafe even if they start.
- Payment provider count being zero is a production readiness blocker for live payment testing, not for schema migration itself.
- The destructive balance de-mixing remains a separate spec/runbook item and must not be folded into the first binary rollout.
- Historical multi-card users are a data-normalization blocker for single-card runtime semantics. Do not let `165_subscription_three_window_backfill.sql` make several rows look healthy while only one row is actually used at runtime.
