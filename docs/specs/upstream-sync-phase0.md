# Upstream Sync Phase 0

Issue: https://github.com/APIs-Gateway/sub2api/issues/62

Snapshot date: 2026-07-08

## Baseline

- Fork baseline: `origin/main` at `becb0f350`.
- Upstream target: `upstream/main` at `6f43986c3`.
- Merge base: `945b9b208b32dac443cf25e1e96f4ac587c93223`.
- Raw divergence: `origin/main...upstream/main` is `146` fork-only commits and `433` upstream-only commits.
- Patch-id divergence after cherry equivalence: `123` fork-only patches and `277` upstream-only patches.
- Diff scale before this phase: `835 files changed, 154470 insertions(+), 60248 deletions(-)`.

## Phase 0 Rules

1. Do not merge `upstream/main` wholesale.
2. Start every sync PR from `origin/main`.
3. Keep each PR reviewable, independently testable, and reversible.
4. Treat existing migration files as immutable, including comments.
5. Preserve fork semantics for subscription billing, payments, affiliate points, public-benefit keys, ops error ownership, and source-build deployment metadata.
6. Prefer cherry-pick only for isolated patches; use manual porting when upstream code crosses fork-owned behavior.

## Classification

| Class | Meaning | Merge style |
| --- | --- | --- |
| A | Low-risk and high-value fixes | Cherry-pick or direct small port |
| B | Valuable but touches fork-owned semantics | Manual port after focused review |
| C | Large product feature or unclear fit | Separate issue/decision first |
| D | Upstream branding/docs/release churn or harmful overlap | Skip unless explicitly requested |

## Progress Snapshot: 2026-07-09

Phase 0 has started producing small PRs instead of a single large sync branch.

| Item | Status | Notes |
| --- | --- | --- |
| Phase 0 matrix + AWS SDK eventstream vuln bump | Merged in fork PR `#63` | This established the first evaluation matrix and absorbed upstream `#3813`. |
| Payment provider NUL-byte cleanup | Merged in fork PR `#64` | Direct small port of upstream `#3714`; CI passed before merge. |
| Codex image_gen namespace guard | Re-opened to `main` as fork PR `#68` | Earlier stacked PR `#65` merged into an intermediate branch; `#68` is the mainline PR to watch. |
| Go 1.26.5 security unblock | Open as fork PR `#69` | New `govulncheck` data reports GO-2026-5856 against `crypto/tls@go1.26.4`; this must land before new backend-security runs can pass on other sync PRs. |
| Responses mapped billing model | Open as fork PR `#66`, base retargeted to `main` | CI was green on the stacked branch; retargeted PR must be judged by its own `main` checks. |
| Promo-code expiry clearing | Open as fork PR `#67`, base retargeted to `main` | Low-risk service/admin fix ported from upstream `#3364`; retargeted PR must be judged by its own `main` checks. |
| Codex function-call item id stripping | Already absorbed by fork PR `#50` | `origin/main` already contains `openai_codex_function_call_id_test.go` and the `fc` prefix guard. No new PR needed. |

## Proposed Child Issue Queue

These are the first child issues worth opening from the Phase 0 investigations. Each item should stay independently reviewable and should name the upstream PRs/commits it absorbs or intentionally skips.

| Child issue | Class | Scope | Notes |
| --- | --- | --- | --- |
| `gateway-compact-protocol-fixes` | A/B | Compact body signal, compact endpoint alias, compact usage SSE, compact image bridge guard | Keep this separate from messages fallback and quota/scheduler changes. Protect WHAM and issue `#16` error ownership. |
| `gateway-parse-error-observability` | A/B | Request-body parse error logging in gateway/OpenAI handlers | Preserve client response stability, bounded body logging, and `client_via_upstream` attribution. |
| `gateway-messages-chat-fallback` | A/B | `/v1/messages` fallback hardening and ops upstream error logging | High risk around streaming-after-200 and issue `#16`; do not combine with compact fixes. |
| `frontend-sidebar-usage-polish` | A | Sidebar scroll persistence, user usage wording, admin usage token-card display | Hand-port to fork `zh-CN`/`zh-HK` i18n layout; avoid payment/subscription wording changes. |
| `admin-scheduler-score-opt-in` | A/B | Admin account list scheduler score opt-in from upstream `#3811` | Preserve fork account columns, localStorage settings, and scheduler semantics. |
| `ops-system-logs-api-key-id` | B | System log `api_key_id` and related admin filtering | Use a new migration number, cover old NULL rows, and run real PostgreSQL. |
| `usage-log-reliability` | B | Usage log queue overflow/fallback-pricing noise before any large repo split | Prefer behavior fixes first; defer pure file moves unless they reduce review risk. |
| `payment-refund-pending` | B | Refund pending/finalization gaps | Needs a fork-specific refund/points/affiliate spec and real PostgreSQL idempotency tests. |
| `subscription-revoke-restore-fork-semantics` | B | Revoke/restore rewritten for single-card three-window subscriptions | Must define status/cache/restore-conflict semantics before code. |
| `openai-account-scheduler` | B | Scheduler score, quota headroom, reset/sticky behavior | Keep separate from OAuth/shadow-account migrations. |
| `spark-shadow-account` | B/C | Spark shadow account schema/service/UI/import-export | Requires new migration numbers and clean DB plus fork-history DB dry runs. |
| `grok-oauth-quota-media` | B/C | Grok OAuth/quota first, media/group gate later | Split OAuth/quota from media/pricing/group default enablement. |
| `batch-image-product-decision` | C | Batch image foundation, audit hardening, migrations `159..169`, frozen balance | Separate product decision; do not include in upstream sync PRs by default. |

## Initial Evaluation Matrix

Gateway/OpenAI candidates were checked with patch-id and merge conflict probes. They are not already absorbed by the fork, but they should mostly be manually ported: upstream file-level cherry-picks conflict with fork-owned gateway paths and can accidentally drop WHAM or issue #16 error-ownership behavior.

Migration and large-feature candidates were checked separately. The migration runner tracks full filenames in `schema_migrations`, runs migrations in filename order, and rejects checksum mismatches. Duplicate numeric prefixes are allowed, but existing migration files are immutable and semantic ordering still needs real PostgreSQL validation.

| Upstream item | Class | Scope | Decision | Required checks |
| --- | --- | --- | --- | --- |
| `#3813` / `317339e71` `fix/bump-aws-sdk-eventstream-vuln` | A | `backend/go.mod`, `backend/go.sum` | Done in fork PR `#63`. It only updates AWS SDK dependency checksums and avoids fork business paths. | `go test` for backend packages that compile S3 code; `git diff --check`. |
| `#3804` / `f407d6a04` compact body signal routing | A/B | OpenAI gateway handler and compact signal helpers | Candidate for next gateway PR, but manual port only. Move endpoint alias/compact constants first, then body `compaction_trigger` promotion, then SSE heuristic. | `go test ./internal/handler ./internal/service -run 'Compact|Endpoint|NormalizeInboundEndpoint|ImageGenerationControls|GatewayService'`; backend build. |
| `#3802/#3795` / `f68f3b86c`, `9643382bd` messages chat fallback hardening | A/B | OpenAI messages fallback service | Candidate, but must be a standalone gateway PR. Preserve fork `handleAnthropicErrorResponse`, `appendOpsUpstreamError`, and `upstreamDetail` behavior. | `go test ./internal/service -run 'Messages|ChatFallback|Anthropic|OpsUpstream'`; `go test ./internal/handler -run 'OpsErrorLogger|UpstreamError'`. |
| `#3788` / `17d991786` Codex function call item id | A | Codex transform service and tests | Already absorbed by fork PR `#50`; no new PR needed. | `go test -tags unit ./internal/service -run 'TestFilterCodexInput_(StripsFunctionCallItemID_WhenPreservingReferences|KeepsFcID_WhenPreservingReferences|StripsItemIDFromAllToolCallInputTypes|OutputTypeKeepsItemID|NonToolCallItemKeepsID)'`. |
| `#3780` / `87bdaf8e6` gateway parse error observability | A/B | Gateway parse error logging/observability | Candidate, but standalone. Client response text should stay stable; logs must be bounded and avoid full body dumps. | `go test ./internal/handler ./internal/service -run 'RequestBodyParse|InvalidJSON|GatewayRequest'`; inspect logging for body truncation. |
| `#3778` / `a98397adb` websearch emulation history blocks | A/B | OpenAI websearch/history filtering | Candidate after comparing the fork's existing filter. Preserve Anthropic official passback behavior. | `go test ./internal/service -run 'WebSearch|HistoryBlock|ThinkingProtocol'`. |
| `#3794` / `af4b75a77` image generation namespace permission bypass | A/B | Codex image-generation intent and transform logic | In progress as fork PR `#68` against `main`. Keep the port limited to namespace `image_gen` and `input[].additional_tools` detection. | `go test ./internal/service -run 'ImageGenerationIntent|Codex|ImageGenerationControls'`; CI on PR `#68`. |
| `#3776` / `aff87d949` compact usage SSE detection | A | OpenAI compact usage detection | Candidate for gateway PR if it does not alter billing ownership. | OpenAI SSE usage tests. |
| `#3761` / `baadb2268` responses alias normalization | A | Responses/OpenAI alias handling | Candidate for gateway PR. | Responses alias unit tests. |
| `#3803` / `f4e8bec54` sidebar scroll persist | A | Frontend app store/sidebar | Candidate for frontend-only PR. Low backend risk. | `pnpm` lint/typecheck and sidebar/app store vitest. |
| `#3771` / `863953321` Redis scan index hardening | B | Redis concurrency and queue caches | Valuable but must run real Redis/testcontainers and inspect hot path behavior. | Repository/cache integration tests with Redis. |
| `#3755` / `913d83877` subscription USD/CNY opt-in rate | B | Subscription and payment amount logic | Do not cherry-pick. Fork has custom subscription pricing and payment behavior. | Billing/payment unit and integration tests with production-shaped data. |
| `#3749` / `3fa08aa93` EasyPay custom methods | B | Payment provider configuration | Review manually against fork KeyingPay/EasyPay confirmation rules. | Payment provider tests and callback idempotency checks. |
| `#3747/#3738` subscription amount and affiliate base | B | Subscription confirmation and affiliate calculations | Manual review only; fork affiliate points and ledger semantics are owned locally. | Payment, affiliate, and points ledger tests. |
| `#3714` / `2e70ec0a1` payment NUL-byte cleanup | A | Payment order provider response sanitization | Done in fork PR `#64`. It was ported without changing amount, subscription, or points semantics. | Create-order coverage for `TradeNo`, `PayURL`, or `QRCode` containing `\x00`; CI passed before merge. |
| `#3364` / `2dc1387b` promo-code expiry clearing | A | Admin promo-code update handler and service | In progress as fork PR `#67` against `main`. This is an isolated clear-vs-omit semantics fix. | `go test ./internal/handler/admin ./internal/service -run 'TestPromo(HandlerUpdateClearsExpiryWhenZeroTimestampProvided|ServiceUpdate(ClearsExpiryWhenZeroTimeProvided|PreservesExpiryWhenOmitted))'`; CI on PR `#67`. |
| `#3699` / `4dd3aee5` responses mapped billing model | A/B | OpenAI Responses usage billing model selection | In progress as fork PR `#66` against `main`. Preserve fork pricing and model mapping semantics. | Targeted OpenAI gateway record-usage tests; CI on PR `#66`. |
| `#3428` / `147c1879` validity plural units | A | Payment validity duration parsing | Good follow-up small service PR if not already equivalent. | Unit test for `week/weeks` and `month/months`. |
| `#3509` / `76e0d9073` refund pending flow | B | Refund state machine and payment UI | Manual port only. Fork has KeyingPay/EasyPay/Kyren safety semantics and subscription card shutdown rules. | Real PostgreSQL refund lifecycle tests, webhook idempotency, and active-card closure checks. |
| `#3586/#3622` subscription revoke/restore | B | Subscription repo/service/cache/admin UI | Manual port only. Fork supports nullable group custom cards and one active-card semantics. | Real PostgreSQL tests for NULL group cards, restore conflicts, and cache invalidation. |
| `#3404` balance reserve / overdraft | B | Billing cache and usage billing repo | Do not cherry-pick. Fork has three-window subscription usage and wallet fallback semantics. | Concurrent real PostgreSQL billing tests with production-shaped data. |
| `#3768` / `44ab690a0` batch image foundation | C | Backend entities, migrations, service, worker, frontend guide | Defer to separate product decision. Do not include in sync PRs. | Separate DB migration plan and real PostgreSQL/Redis tests if accepted. |
| `#3799` / `65ff80036` batch image audit hardening | C | Batch image audit paths | Depends on accepting batch image foundation first. | Same as batch image foundation. |
| Upstream migrations `154..169` | C | Spark shadow, ops logs, Grok quota/media, group peak multiplier, batch image, frozen balance | Exclude from first PR. They do not share filenames with fork migrations, but they overlap numbering and product semantics with fork `154..173`. | If accepted later, validate clean DB and already-migrated fork DB with real PostgreSQL. |
| `#3811` / `6f43986c3` admin scheduler score opt-in | B | Admin account scheduler score | Manual review with fork scheduler and stable-priority semantics. | Account scheduler unit tests. |
| `#3708` / `b5b0ea346` antigravity 401 self-heal | B | Antigravity token refresh and gateway retry | Likely useful, but must preserve fork gateway retry and billing behavior. | Antigravity gateway/token tests. |
| `#3759` / `67e945f8e` account header override | B | Account credential/runtime headers | Manual review because account runtime is fork-sensitive. | Account service and gateway header tests. |
| Sponsor/logo/readme churn | D | `assets/partners`, `README*` | Skip by default. | None. |
| Upstream version/release sync commits | D | `VERSION`, release workflow | Skip by default; fork deployment metadata is source-build oriented. | None. |
| Docker/deploy URL defaults | B/D | `deploy/*`, README deploy docs | Only port narrowly if it improves local/dev safety without changing production semantics. | Config review and deployment metadata check. |

## This Phase PR Scope

This PR intentionally includes only:

1. The Phase 0 evaluation document.
2. The AWS SDK eventstream vulnerability dependency bump from upstream `#3813`.

It intentionally excludes gateway, billing, payment behavior, batch image, migration, release, README, and branding changes. Those need follow-up PRs with smaller review scopes.

First-batch exclusions are explicit:

- `backend/migrations/*`, especially upstream `154..169` and fork `154..173`.
- Batch image entities, repositories, workers, services, frontend entry points, and docs.
- Grok, Antigravity, OAuth, account scheduler, and platform quota groups.
- Frontend i18n/settings/admin account large file splits.
- Docker, deploy, release workflow, `VERSION`, README, assets, and sponsor churn.
- Payment, subscription, points, and usage billing behavior unless covered by a child spec and real DB verification.

## Follow-up PR Order

1. Codex function-call item id as the next smallest backend PR.
2. Gateway/OpenAI compact and endpoint fixes: compact signal routing, responses alias normalization, compact SSE detection.
3. Websearch/history filter or image namespace permission fix as a small backend PR.
4. Messages/chat fallback hardening as a standalone gateway PR because it sits close to issue #16 error ownership.
5. Parse error observability as a standalone gateway PR.
6. Small payment/service bugfixes: payment NUL-byte cleanup, supported-types null UI fallback, validity plural units.
7. Frontend-only polish: sidebar scroll persistence and small wording fixes.
8. Redis/cache hardening with real Redis integration tests.
9. Payment/subscription/billing review PRs, each with a child spec before code.
10. Separate decision issue for batch image and related migrations.

## Verification Expectations

For this phase:

- `git diff --check`
- `go test` for backend packages that compile the updated AWS SDK dependency
- PR review confirms no historical migration file changes

For follow-up phases, use the broader gates from issue #62.
