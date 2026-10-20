# Upstream Group Duplicate Compatibility Spec

This spec adapts upstream commit `c993490a8` to the current fork without importing
unrelated upstream Group fields or generated code.

## §G1 Scope

Add an admin-only group duplication workflow. The implementation copies fields
that exist in the current fork's `Group` model. Upstream-only peak, video,
web-search, and batch-image fields are intentionally excluded.

## §G2 Behavior

`POST /api/v1/admin/groups/:id/duplicate` creates an inactive copy of the source
group. It copies the current Group configuration and the source
`account_groups.priority` values for non-deleted accounts. API keys,
subscriptions, user permissions, usage history, and runtime statistics are not
copied.

The operation must write the group row, account bindings, and scheduler outbox
event in one database transaction.

## §G3 Idempotency

The endpoint uses the existing admin idempotency helper. A persistent operation
digest is derived from the admin scope, source group ID, and `Idempotency-Key`.
Retrying the same request returns the already-created copy and does not create a
second group. The digest is internal metadata and is never exposed in API
responses.

## §G4 Naming and boundaries

The first copy is named `<source> (Copy)` and later copies use `<source> (Copy N)`.
Names are trimmed and truncated by rune count to the existing 100-rune limit.
The copy starts inactive. Only non-deleted source accounts are bound, and each
binding keeps its original priority. A missing source or any failed transaction
step must leave no orphan group or binding rows.

## §G5 Verification

Add service tests for cloning and naming, PostgreSQL repository integration tests
for atomic bindings and idempotent recovery, handler/API contract tests, and
frontend API/UI tests. The remote CI and Codecov checks are the merge gates.
