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
