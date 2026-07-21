# Change-plan Overdraft Reset

## Goal

A successful plan change closes the previous subscription card and opens a new
card. The new plan must start with the user's full monthly manual-overdraft
allowance instead of inheriting overdraft uses consumed under the previous
plan.

## Behavior

- When `ApplyChangePlanFromOrder` succeeds, set
  `users.monthly_overdraft_count` to `0` and
  `users.monthly_overdraft_month` to the current Asia/Shanghai month (`YYYYMM`).
- Reset the user-level count in the same transaction that closes the old card,
  creates the new card, and stamps `last_change_plan_day`.
- Invalidate the user's API-key authentication cache after the change commits,
  because cached authorization snapshots contain the monthly overdraft count.
- Apply the same behavior to fiat-payment and points-redemption plan changes.

## Non-goals

- Renewal does not reset monthly overdraft uses because it extends the existing
  card instead of replacing it.
- Plan purchase, quote creation, failed fulfillment, and rejected downgrade do
  not reset the count.
- Daily, weekly, and monthly subscription usage continues to carry over during
  a plan change to prevent double allowance in the same usage window.
- Wallet balance, plan validity, and the retired per-card overdraft counter keep
  their existing behavior.

## Acceptance checks

1. A user with consumed overdraft uses has count `0` and the current month after
   a successful plan change.
2. The reset rolls back if the plan change transaction fails.
3. A renewal preserves the user's monthly overdraft count and month.
4. The new card still inherits the old card's three-window usage.
5. API-key authorization cache invalidation runs for the changed user, including
   a post-commit invalidation for outer fiat and points transactions.
