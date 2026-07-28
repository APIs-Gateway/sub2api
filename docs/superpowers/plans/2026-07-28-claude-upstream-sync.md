# Claude Upstream Sync Implementation Plan

> **For agentic workers:** Execute the tasks in dependency order. This task is
> explicitly validated by remote CI only; do not run local tests, build, or
> compile commands.

**Goal:** Reconcile all final upstream Claude/Anthropic/Bedrock functionality
and compatibility behavior into the fork, with upstream-equivalent regression
coverage.

**Architecture:** Port only the final functional deltas from `upstream/main`
into the fork's existing service and compatibility layers. Preserve local
architecture and unrelated fork behavior; tests encode each ported upstream
contract for remote CI.

**Tech Stack:** Go, Gin, TypeScript/Vue frontend, GitHub Actions, Codecov.

---

### Task 1: Model, Bedrock, and configuration parity

**Files:** Claude constants, Bedrock request handling, pricing/billing,
frontend model whitelist, and their upstream regression tests.

- [ ] Port model registrations and mappings absent from the fork, including
  Claude Opus 5, its pricing and fallback hierarchy, and Bedrock capability
  recognition for major-only model IDs.
- [ ] Preserve explicit upstream beta and capability behavior for Anthropic,
  Bedrock, and Vertex model ID variants.
- [ ] Port the upstream regression tests that prove pricing fallback,
  capability gates, thinking conversion, and model discovery contracts.

### Task 2: Account authentication and Claude Code forwarding parity

**Files:** Anthropic authentication/account settings, Claude OAuth forwarding,
Claude Code validator/mimicry, request-header configuration, and tests.

- [ ] Port API-key Bearer authentication selection and validated account-level
  header overrides.
- [ ] Port Claude Code current-version and billing-attribution behavior,
  proxied-client detection, system cache-control preservation, and client
  dateline normalization.
- [ ] Port the corresponding upstream tests for request headers, cache
  breakpoints, and Claude Code request identity.

### Task 3: Messages and protocol bridge parity

**Files:** `backend/internal/pkg/apicompat`, Messages forwarders, Responses
forwarders, synthetic gateway responses, and their test suites.

- [ ] Reconcile final Anthropic ↔ Responses ↔ Chat Completions request,
  tool, usage, error, and streaming event behavior.
- [ ] Port reversible client-tool lowering/restoration for Anthropic
  forwarding, including namespaces, additional tools, custom tools, and array
  function outputs.
- [ ] Port official Anthropic synthetic message ID and wire-field behavior.
- [ ] Port upstream regression coverage for streaming finalization, tool
  ordering, stop semantics, cache usage, and response payload accumulation.

### Task 4: Anthropic rate-limit and usage parity

**Files:** rate-limit and account-usage services, model rate-limit helpers,
account usage UI/types, and tests.

- [ ] Reconcile model-scoped Anthropic/Fable limit behavior, fallback cooldown,
  retry handling, and passive/active usage-window reporting.
- [ ] Port upstream regression tests for 429 windows, model isolation, and
  usage serialization.

### Task 5: Publish and validate remotely

- [ ] Inspect the intended diff and stage only the sync, test, and design/plan
  files.
- [ ] Commit and push `codex/sync-claude-compatibility`.
- [ ] Create a fork PR whose body contains `Closes #728`; add the PR URL as
  a comment on Issue #728.
- [ ] Use GitHub Actions and coverage checks only. Iterate on CI failures
  until all required checks pass and coverage is at least 80%.
