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

## Initial Evaluation Matrix

Gateway/OpenAI candidates were checked with patch-id and merge conflict probes. They are not already absorbed by the fork, but they should mostly be manually ported: upstream file-level cherry-picks conflict with fork-owned gateway paths and can accidentally drop WHAM or issue #16 error-ownership behavior.

Migration and large-feature candidates were checked separately. The migration runner tracks full filenames in `schema_migrations`, runs migrations in filename order, and rejects checksum mismatches. Duplicate numeric prefixes are allowed, but existing migration files are immutable and semantic ordering still needs real PostgreSQL validation.

| Upstream item | Class | Scope | Decision | Required checks |
| --- | --- | --- | --- | --- |
| `#3813` / `317339e71` `fix/bump-aws-sdk-eventstream-vuln` | A | `backend/go.mod`, `backend/go.sum` | Included in this phase. It only updates AWS SDK dependency checksums and avoids fork business paths. | `go test` for backend packages that compile S3 code; `git diff --check`. |
| `#3804` / `f407d6a04` compact body signal routing | A/B | OpenAI gateway handler and compact signal helpers | Candidate for next gateway PR, but manual port only. Move endpoint alias/compact constants first, then body `compaction_trigger` promotion, then SSE heuristic. | `go test ./internal/handler ./internal/service -run 'Compact|Endpoint|NormalizeInboundEndpoint|ImageGenerationControls|GatewayService'`; backend build. |
| `#3802/#3795` / `f68f3b86c`, `9643382bd` messages chat fallback hardening | A/B | OpenAI messages fallback service | Candidate, but must be a standalone gateway PR. Preserve fork `handleAnthropicErrorResponse`, `appendOpsUpstreamError`, and `upstreamDetail` behavior. | `go test ./internal/service -run 'Messages|ChatFallback|Anthropic|OpsUpstream'`; `go test ./internal/handler -run 'OpsErrorLogger|UpstreamError'`. |
| `#3788` / `17d991786` Codex function call item id | A | Codex transform service and tests | Best next tiny gateway PR. Manually port only the illegal `item_*` id stripping in `PreserveReferences=true` call-input paths. | `go test ./internal/service -run 'Codex|FunctionCall|Transform'`. |
| `#3780` / `87bdaf8e6` gateway parse error observability | A/B | Gateway parse error logging/observability | Candidate, but standalone. Client response text should stay stable; logs must be bounded and avoid full body dumps. | `go test ./internal/handler ./internal/service -run 'RequestBodyParse|InvalidJSON|GatewayRequest'`; inspect logging for body truncation. |
| `#3778` / `a98397adb` websearch emulation history blocks | A/B | OpenAI websearch/history filtering | Candidate after comparing the fork's existing filter. Preserve Anthropic official passback behavior. | `go test ./internal/service -run 'WebSearch|HistoryBlock|ThinkingProtocol'`. |
| `#3794` / `af4b75a77` image generation namespace permission bypass | A/B | Codex image-generation intent and transform logic | Candidate for a small backend PR if limited to namespace `image_gen` and `input[].additional_tools` detection. Do not bring account/UI tool-policy changes. | `go test ./internal/service -run 'ImageGenerationIntent|Codex|ImageGenerationControls'`. |
| `#3776` / `aff87d949` compact usage SSE detection | A | OpenAI compact usage detection | Candidate for gateway PR if it does not alter billing ownership. | OpenAI SSE usage tests. |
| `#3761` / `baadb2268` responses alias normalization | A | Responses/OpenAI alias handling | Candidate for gateway PR. | Responses alias unit tests. |
| `#3803` / `f4e8bec54` sidebar scroll persist | A | Frontend app store/sidebar | Candidate for frontend-only PR. Low backend risk. | `pnpm` lint/typecheck and sidebar/app store vitest. |
| `#3771` / `863953321` Redis scan index hardening | B | Redis concurrency and queue caches | Valuable but must run real Redis/testcontainers and inspect hot path behavior. | Repository/cache integration tests with Redis. |
| `#3755` / `913d83877` subscription USD/CNY opt-in rate | B | Subscription and payment amount logic | Do not cherry-pick. Fork has custom subscription pricing and payment behavior. | Billing/payment unit and integration tests with production-shaped data. |
| `#3749` / `3fa08aa93` EasyPay custom methods | B | Payment provider configuration | Review manually against fork KeyingPay/EasyPay confirmation rules. | Payment provider tests and callback idempotency checks. |
| `#3747/#3738` subscription amount and affiliate base | B | Subscription confirmation and affiliate calculations | Manual review only; fork affiliate points and ledger semantics are owned locally. | Payment, affiliate, and points ledger tests. |
| `#3714` / `2e70ec0a1` payment NUL-byte cleanup | A | Payment order provider response sanitization | Good follow-up small payment PR. It can be ported without changing amount, subscription, or points semantics. | Add create-order test with `TradeNo`, `PayURL`, or `QRCode` containing `\x00`. |
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
