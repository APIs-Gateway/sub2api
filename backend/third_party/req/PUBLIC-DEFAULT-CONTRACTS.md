# Public default API contracts (candidate)

This is locally authored regression source for the public default-client and
request-wrapper API, proposed for the existing #1712 scope and permanent #291.
It is a coverage candidate, not a claim that either required 85% gate passes.
No copied upstream tests or production hooks are used.

Base: `673c3fafa9ff9c4c32a6adbd8cd5f595c25f0c63`.
At batch start, fresh Git fetches recorded `origin/main`
`c3e3eed31c7783d1923e1afd9ce706f3501ece26` and `upstream/main`
`dba770d56a0ee0ae5bad1b3dc5e11614d323163c` (2026-10-22).
The previous Digest author branch remains preserved at
`beed0eb87da3eeb8f88ca7706d0e54a6cb6452d2`.

| Case | Public contract and independent oracle |
| --- | --- |
| `replacement_binds_new_calls_only` | A nil default replacement keeps the active client. After switching real peer A to B, a new package-level GET reaches B and an already-created request still reaches A; distinct literal response bodies, headers, paths and exact request counts identify each peer. |
| `escaped_path_query_and_header_precedence` | Client defaults and per-request overrides produce one literal escaped RequestURI, repeated ordered query values, request header override and retained common header. |
| `form_auth_and_cookie_wire` | A real POST carries a literal form body with special characters and repeated values, literal Basic authorization, combined cookies and form Content-Type; peer returns a distinct 201 response. |
| `json_request_and_success_result` | Literal JSON request bytes and Bearer header reach a real peer; its literal 201 JSON response populates the typed success value and public result API while the error result stays absent. |
| `http_error_result_preserves_body` | A literal 422 JSON response populates only the typed error result, preserves response bytes and leaves the supplied success value untouched; an HTTP error response remains distinct from a transport failure. |
| `context_cancellation_reaches_peer` | Cancellation occurs only after real request arrival. Both the returned error and real peer request context must identify context cancellation, with one request. A separate cleanup release cannot satisfy the cancellation assertion. |

The test group never calls `t.Parallel`. Every installed default client restores
its previous value and closes its own idle connections in cleanup; the original
default client is not mutated. No F44, Digest23 or G test/helper is reused or
changed. Literal wire oracles do not read private request/client fields, mirror
setter implementations, count methods, or inject mock RoundTrippers.

Status: raw source prepared and statically reviewed by the author only. No local
or server87 project execution, formatting, syntax/AST validation, tests or builds
were run. Independent review, authorized remote formatting, remote race/behavior
execution and any later inventory/manifest registration remain pending. Existing
formal test and coverage gates are unchanged by this candidate commit.
