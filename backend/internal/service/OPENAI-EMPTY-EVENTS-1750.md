# Empty Responses events and safe replay (#1750)

Source base: c3e3eed31c7783d1923e1afd9ce706f3501ece26.
Upstream main inspected:37067e8e340e9440d173a92b4faa05e8ce58368f.
Sources: https://github.com/Wei-Shaw/sub2api/pull/8305,
https://github.com/Wei-Shaw/sub2api/commit/c591412c42ab195dacdede9eb19f9cbfee221144,
https://github.com/Wei-Shaw/sub2api/pull/8139.
Fork child: https://github.com/APIs-Gateway/sub2api/issues/1750.
Permanent tracking: https://github.com/APIs-Gateway/sub2api/issues/291 (keep open).

## OLD test-only source

A separately frozen test-only commit adds literal12 upstream empty payloads and
queued/metadata plus explicit empty refusal-part completion, each traversing native, passthrough and WS-to-HTTP bridge against
an actual loopback HTTP SSE peer. The bridge test port changes only the owned
network destination, not the production-built method/path/body or real response.
Every case requires actual POST, stream request, full peer Write/Flush and one
actual request before a business RED assertion can be accepted. OLD failures are
not predicted as an allowed list: CI must retain actual named/full events and
exit codes even after failure; setup/compile/fixture/race failures are blockers.

Positive cases require request-scoped same-account failover with no output.
Native/passthrough retain terminal input usage3 and no semantic first token.
No empty-event success creates BillingNoCharge or ForceCacheBilling. Real
nonempty whitespace/arguments/input, tool completion, encrypted reasoning and
unknown events must prohibit replay and preserve the existing server_error
client failure code. An event: label cannot hide a tool completion JSON type.
Separate direct boundary cases keep malformed/missing/null/nonstring/unknown,
contradictory type and duplicate keys conservative, including nested done parts.

All three real production callers derive eventType from JSON payload (gjson or
parseOpenAIWSEventEnvelope), not an SSE event: header. New empty-event handling
must bind the actual JSON type to that argument; old added/error/failed rules
remain unchanged. Missing/malformed typed fields are not evidence of emptiness.

## CI and scope

RAW source only, not formatted/compiled/executed. No local or87 project execution,
syntax/AST/checkers/formatters/tests/builds. Remote CI must use Go1.26.10 and full
frozen Git inputs for identical OLD/NEW fixtures; current main Go1.26.6 security
repair is a separate1712 dependency and cannot be waived for merge. Future OLD
observation must capture actual assertions, and NEW must pass the whole focused
matrix plus original capacity/keepalive/TTFT/timeout/staging/billing-budget tests.
An accepted static source is not a RED/PASS/85/LGTM statement.

This child excludes Retry-After, identity/version settings, public-key/credit/
payment/billing/scheduling changes. Existing per-attempt billing and request
budgets, original added-item behavior and memory limits stay. Preserve and
cross-review shared-file PRs1733/1717/1710/1708/1705/1704/1699/1697/1672/1022 and
1712 before merge. No local resources or remote branch have been published.

## NEW production source preparation

The new source changes only the existing shared classifier module. Added/error/
failed semantics and tool completion remain untouched. New known forms use a
small duplicate-rejecting JSON-object reader, exact payload/event type pairing,
and explicit empty-string bytes; nested done parts use the same object/type
checks. Queued/metadata are exact lifecycle names, not a broad event wildcard.
No production timing, retry count/delay, usage/billing, transport or scheduling
code changes. OLD and NEW retain byte-identical test source. Formatting and
actual OLD/NEW outcomes are pending independent review and remote CI.

## Accepted remote format artifact (OLD source import)

The earlier RAW preparation sections are retained as history. Formatter-only
controller `837aa2bcf725d25c09a99f8dcdba1289e7b8d53c` actually ran Go1.26.10
under [run40183164438](https://github.com/APIs-Gateway/sub2api/actions/runs/40183164438),
attempt1. Its frozen application inputs were RAW OLD
`5052c03eb672b7cd162bf3fa41042aa35d34cc25` and RAW NEW
`a2cebe17c05e5c806bd4019f8085c35864345f09` (complete4307-file Git trees).
Artifact12594229961 is44535218 bytes,134 members; original ZIP SHA256
`943313c109e022414eb587b396ec338a19ef2c04aff82a7d882fee0fb9abb8a3`.
All16 actual formatter operations, nine negative checker controls, two valid
raw-diff controls, full Git archives and unchanged before/after inputs were
reviewed. Root actual receipt SHA256
`5571245999a87b0f33f241ce5a3476261759650c2e4d4ba11b4e0136c41404fc`;
author raw-data receipt SHA256
`778de8ba5b5adb54067242d24878ae1943f3a073cc492d7f5102962830f25e99`.

Only the verbatim test output is imported here:14157 bytes, SHA256
`b3c439eadee03165b218ab2490211574f2207a08af4375874535b4863c619a25`.
OLD and NEW output copies are byte identical. This OLD source retains the
complete original main production bytes; no production formatting was imported.
Both production outputs were already byte identical to their RAW Git inputs.
The formatter controller publication and its format run are actual events;
application OLD/NEW testing, race/build and required formal checks remain pending.
Formatting is not a business RED/PASS or coverage approval.

The separately sealed existing-regression inventory is based on exacta2ce:
159 top tests (90 service,69 handler),193 explicit subtests,352 RUN/terminal
identities; frozen inventory SHA256
`601584014f937747786d07cb8aa76e562a42d54779b04a776693ece1eabcbaa7`.
All21 selected original test files and module inputs are unchanged in this
import. Both OLD and NEW must actually PASS the original regressions. The new98
remain separate: genuine OLD business RED must keep the job failed, while NEW
must PASS all98. Compiler/fixture/race/timeout/incomplete phases are blockers.
No local or87 project execution or future behavior workflow publication occurred.

## NEW normal composition after accepted format import

This NEW source normally merges the formatted OLD parent `16ff3b8d2e4c67dab07df5c44ee3441b0eda775a`
with the original reviewed production commit
`a2cebe17c05e5c806bd4019f8085c35864345f09`. The two append-only provenance
sections conflicted during normal merge; this file preserves the complete RAW
NEW document plus the entire accepted OLD-format record above. The OLD-format
section describes the unchanged main production in that OLD parent. In this
NEW composition, production is instead byte identical to originala2ce, SHA256
`05f064685b60361a46b4e3524ab20c264518b9fc025c9b16a43e3f20ac57bc8b`;
no production format output or new production change is imported. Test output
is the same verbatim14157-byte/b3c439...619a25 artifact as the formatted OLD
parent. All selected original21 regression sources remain unchanged.

The earlier RAW NEW format-pending statement is historical and superseded only
for formatting by run40183164438 and its two actual reviews above. No OLD/NEW
application runtime, test, race, compilation or build evidence is claimed here.
Focused98 and existing159tops/193subnames remain pending exact-head remote CI.
No local/87 project execution; no new application branch publication.

## Actual lint finding and checked peer-body Close (OLD repair)

[PR1751](https://github.com/APIs-Gateway/sub2api/pull/1751) CI
[run40185221526](https://github.com/APIs-Gateway/sub2api/actions/runs/40185221526),
job121144480222, actually rejected formatted NEW source
`affe76ea781e3c9db4cf20867ee5fa6093620eb0`: errcheck reported the unchecked
`defer response.Body.Close()` in openai_empty_event_retry_test.go:180.
The original saved job log SHA256 is `c942e81a5b78c4441b9671cb741b3c8133c49de16340d7060d032af76d85ab77`. This is an actual
lint blocker, not a business OLD RED and not waived by earlier formatting.

This OLD fixture revision only expands that defer into a function which checks
the actual response Body.Close error with require.NoError and an explicit
EMPTY_EVENT_FIXTURE label. It runs in the same helper goroutine before the
helper returns its result; a close failure cannot become a safe-retry business
assertion or an accepted RED. Both OLD and NEW must carry identical revised test
bytes. All98 names, real request/peer/Write/Flush prerequisites, business/usage/
billing/conservative assertions, timeouts, source imports and production bytes
remain unchanged. No additional source hook, mock body or ignored close exists.

The 14157-byte/b3c439 format artifact above is historical evidence for its
previous input. It does not claim to cover this narrow subsequent Close edit.
Formatting/lint/race/tests for the revised exact input remain pending remote CI,
with a read-only format gate in the future proof; no new formatter framework or
local/87 project execution was introduced. Existing selected159tops/193subnames
remain unchanged and must PASS separately on each side.

## NEW checked-Close normal composition

This NEW revision normally merges checked-Close OLD `31e969780424990d022179f17fda0451c18f3aa2`
with prior formatted NEW `affe76ea781e3c9db4cf20867ee5fa6093620eb0`. The only
merge conflict was append-only provenance; the complete prior NEW document and
the entire checked-Close OLD repair record above are retained. This current
NEW keeps the actual production bytes of a2ce/affe unchanged. Both sides carry
the identical three-line deferred actual Close check and unchanged98 contracts.
No production repair, test behavior relaxation or additional request was added.

The only actual evidence for this narrow subsequent revision is static source
review until its exact-head remote checks run. Prior14157-byte format output
and prior affe lint failure remain historical inputs; neither is relabeled as
runtime or formatting acceptance for the revised source. All required checks,
selected original352 identities and new98 actual evidence remain pending.
