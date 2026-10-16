# Controlled req v3.57.0 security backport

This directory preserves the complete official `github.com/imroc/req/v3`
v3.57.0 module archive, including its tests and shared fixtures. The module path
and public API remain unchanged. The backend uses an explicit local replacement
so that the private HTTP/2 implementation is included in security repairs.

Original archive:
https://proxy.golang.org/github.com/imroc/req/v3/@v/v3.57.0.zip

Archive SHA256:
`7551b781c424d09cbd5823a0d5f9ff7dc5002aadd9ed226a776aa7150a2d186c`

The original archive contains 132 files and 1,109,806 uncompressed bytes.
`UPSTREAM-MANIFEST.json` records every original file's exact SHA256 and size.
The ten declared production adaptations leave 122 of 132 original files
byte-identical to that archive. The manifest lists every adapted path and its
candidate hash. The original module metadata and browser presets are retained.

## Official repair sources

- Initial-window CPU amplification: Go x/net commit
  [cc7d34fd](https://github.com/golang/net/commit/cc7d34fd2dd5864bcafe3e5e0a84919240be98c0),
  [CL 847186](https://go-review.googlesource.com/c/net/+/847186).
  The original private `flow.go` exactly matches the official patch's parent.
  The client transport integration is adapted to req's existing options and
  locking methods. Stream-window adjustments use a shared initial window and
  per-stream delta. Overflow retains the official lazy connection error and
  GOAWAY behavior, and stream errors retain the connection for other streams.
- Malformed response framing headers: Go x/net commit
  [28247830](https://github.com/golang/net/commit/28247830e9fb37184c6be718f483a0c7d0949022).
  Strip connection-specific fields and normalize identical, valid repeated
  Content-Length values before a response can reach an HTTP/1 downstream.
- Shared header/trailer parser budgets: Go x/net commit
  [57f4235f](https://github.com/golang/net/commit/57f4235f83712a299328015548240512213a3852).
  Account for declared trailer expansion separately from ordinary headers.
  Budget arithmetic must remain valid at the uint32 maximum without allocating
  a correspondingly large fixture. The published advisory's server exploit
  does not establish identical client exposure; this is the shared parser port.

The official repairs affect `internal/http2/flow.go`,
`internal/http2/transport.go` and `internal/http2/frame.go`. The transport also
contains the necessary local dump delimiter ordering repair described below;
it is not represented as an unchanged official port. This module has no
HTTP/2 server or server write scheduler; server-only fixes remain in the
backend's upgraded standard library and `golang.org/x/net` dependency.

## Necessary local dump and trace race repairs

[Independent exact-source race findings and original-version control](https://github.com/APIs-Gateway/sub2api/pull/1712#issuecomment-6086271861)
show two production race families in the unchanged official v3.57.0 archive
under the same secured main module graph. Latest official tags/master do not
contain an applicable fix. Reproduction in the original dependency does not
waive these blocking findings. The local repairs additionally adapt
`internal/dump/dump.go`, `request.go`, `response.go` and `trace.go`.

- Actual writes to a shared sink serialize across synchronous/asynchronous
  dumpers, clones and independent NewDumper instances. An active-write registry
  retains no idle writer. Comparable identities receive independent locks;
  non-comparable writer values conservatively serialize by dynamic type.
  Configuration is performed before concurrent requests, as before.
- Async payloads are copied, a bounded queue admits at most20 waiting tasks,
  and workers start on demand and retire when idle. Each clone has an independent
  queue. Stop is idempotent, rejects new work, and drains that dumper's admitted
  writes; it does not join another clone or a network upload. Synchronous
  request dumping requires no Start call.
- Request buffers synchronize Write, String and Reset. Immutable attempt sinks
  capture a generation; reset retires old sinks so canceled upload callbacks
  cannot append into a retry's buffer. Configuring dump options alone does not
  enable dumping, including during retry; changing options after EnableDump
  and before the request keeps the original API's ordering semantics. Final
  options are captured after before-request hooks for each enabled attempt.
  An immutable dump context replaces only its own top-level wrapper, preserving
  caller deadlines/cancellation/values and old transport sink identities without
  retaining an unbounded parent chain during infinite retry.
- Response.Dump snapshots bytes already captured; it does not wait for unknown
  network completion. HTTP/2 publishes the final request delimiter before any
  terminal DATA/trailer HEADERS write can implicitly flush END_STREAM. This
  includes large frames and preserves full duplex/DisableAutoReadResponse.
  The delimiter describes attempted terminal framing: a subsequent socket
  write failure cannot retract bytes already emitted to an external writer.
  Body read/cancellation errors before terminal framing do not add it.
- All nine trace callbacks, completion times and pure-state snapshots share a
  lock. Request completion freezes late callbacks; a streaming body may later
  extend its received timestamp. Sequential request reuse and retries use
  separate trace states. Missing/unreached stages remain zero and elapsed
  durations are nonnegative. Conn address methods run after releasing the lock.

These are controlled local adaptations, with distinct regression tests and
coverage requirements. No existing original assertion, race detector or
coverage threshold is removed. Candidate hashes and pending remote evidence
are not approval.

## Required default HTTP/2 TLS trace correction

The [independent actual exact88 failure review](https://github.com/APIs-Gateway/sub2api/pull/1712#issuecomment-6087548227)
records two failed fresh HTTP/2 TLS trace assertions:
`TestReqTraceSecurity_NormalTLSAndReuse/HTTP2` and
`TestReqTraceSecurity_RequestReuse/HTTP2`. The default private transport used
`tls.Dialer.DialContext` without TLS handshake trace callbacks; custom-handshake
callbacks were already present. This is an additional controlled local repair
in the already-adapted `internal/http2/transport.go`, not an official security
port or a relaxed test expectation.

The default branch follows the [Go1.26.9 TLS dial implementation](https://github.com/golang/go/blob/go1.26.9/src/crypto/tls/tls.go):
TCP connection completes using a zero-value net.Dialer and the caller context;
TLSHandshakeStart precedes only the actual TLS HandshakeContext. Failure closes
the raw connection and reports TLSHandshakeDone with the error; success reports
the actual ConnectionState. Nil/empty-server-name config normalization retains
the original TLS dial semantics. The existing zero dialer timeout/deadline and
caller cancellation apply as before; this correction does not introduce a new
TLSHandshakeTimeout policy. Custom TLS handshakes/dialers, browser fingerprints,
ALPN and connection reuse retain their paths.

The original78 expected leaves remain required, with four additional real-socket
default TLS callback leaves (82 total): explicit/derived SNI successful handshakes,
nil-config certificate verification failure, and caller cancellation during a
blocked handshake. Failure cases require raw connection closure and exact callback
order/error; successful cases require the actual TLS state without input-config
mutation. The failed exact88 run stopped original
all-tests, old-wire-red and final repair-block85; its partial results do not
approve this correction. New exact-head remote evidence and unchanged Codecov85
remain mandatory.

## Additional HTTP/1 lifecycle and proxy regressions

The original82 named leaves remain required. Nine additional HTTP/1 leaves use
the real Client.Do transport and loopback TCP peers to check chunked trailers
and EOF ordering, complete-body connection reuse, truncated framing, early
Body.Close/cancellation, and recovery on a new connection. Expect:100-continue
cases distinguish informational/100 responses, a final closing response that
does not consume the upload, and a final keepalive response that still finishes
the upload before reusing its socket. A gated io.Pipe upload requires its first
chunk to reach the peer before the producer may send its second chunk and final
trailer; it cannot pass by buffering the entire body until EOF.

Six proxy leaves use real HTTP forwarding and CONNECT peers: absolute-form
requests/authentication without mutating caller credentials, failed or malformed
CONNECT responses, callback rejection before TLS, and cancellation before a
held proxy response. The last case preserves the existing detached-dial contract:
request cancellation returns promptly, while a later failed CONNECT retires the
socket. It does not require cancellation to immediately close an in-flight dial.

All97 leaves require actual named JSON RUN/PASS events and the full selected
original-package race suite. These fixtures do not change production code or
waive full-PR Codecov85. The separate per-adaptation repair-block85 gate and raw profiles
remain intact. Candidate tests and their inventories are not passing evidence;
new exact-head remote validation and independent review remain required.

## Public HTTP/3 TLS, response bodies and deflate Close

Real loopback QUIC peers exercise the public optional HTTP/3 path. A verified
certificate failure established that the private transport's TLS field shadowed
the shared public options; the declared `internal/http3/transport.go` adaptation
uses shared TLS options only when no explicit private override exists.

[CI run 38735340631](https://github.com/APIs-Gateway/sub2api/actions/runs/38735340631)
established three actual response body failures under public automatic
decompression: nil unencoded body and empty manually negotiated gzip/deflate
payloads. The declared `internal/http3/stream.go` adaptation retains the wire
body and replaces it only when a supported decoder exists. Unknown and explicit
identity codings preserve their body, encoding, length and Uncompressed state.

[CI run 38737227705](https://github.com/APIs-Gateway/sub2api/actions/runs/38737227705)
then established that partially reading deflate and closing the body left a real
nonfinal QUIC response stream open with the caller context still live. Unread
Close correctly delivered remote H3_REQUEST_CANCELLED. The declared
`internal/compress/deflate_reader.go` candidate always closes the underlying
body after decoder Close, retaining decoder error priority and adding no shared
state. The exact two wire cases remain mandatory. Seven narrow unit controls
cover unread/initialized closure, both error sources and repeated Close.

The189 preceding source contracts (180 plus these nine controls), together
with the149 HTTP/2 frame contracts, require338 actual named/all RUN/PASS events. Remote race suites, all ten repair-block85 results
and independent review remain required. These optional public-library contracts
do not establish default gateway HTTP/3 traffic.

## Cold/warm module-cache identity regression

A real GitHub CI run on the unchanged security source completed named and
original-package race tests but stopped before the old-wire witness because
11 external versioned modules acquired optional cache `Dir` fields between the
two `go list -m -json all` snapshots. No module version or checksum changed.
The graph comparator now records those optional cache-materialization changes
while comparing every other field. A present directory in both snapshots must
remain identical. External local replacements are rejected; main modfile and
controlled req replacement/source paths retain exact validation, including
non-location replacement metadata. Both graphs must contain all 365 unique
module paths. The exact old.mod replacement-only and old.sum byte checks remain.

CI executes regressions against both complete actual 365-module snapshots,
including the 11 observed cache changes and cold/warm directions, and rejects
version, sum, modfile, replacement, unknown-field, duplicate, cardinality and
nonoptional directory drift. The raw graph snapshots and optional Dir changes
are retained in evidence. This verifier correction does not waive the old-wire
assertion, source provenance, races or either unchanged coverage requirement.
New candidate CI execution and independent review remain required.

## Licenses

Retain the original MIT `LICENSE` for req and all original copyright notices.
The Go-derived HTTP/2 files retain their Go Authors copyright notices and BSD
terms in `LICENSE-GO-BSD`, copied from the exact official repair revision.

## Validation and maintenance

Tests run remotely from `backend`, with `GOWORK=off`, under the backend's secured
module graph. Running `go test ./...` in the backend does not select the nested
module. Explicitly select `github.com/imroc/req/v3/internal/http2` and
`github.com/imroc/req/v3/internal/dump` and `github.com/imroc/req/v3/...`;
retain `go list` module replacement and package directory evidence, actual
named HTTP/2/dump/trace test events and raw coverage profiles. The wildcard
selects12 original test files; the two original internal/testdata helper files
remain byte-identical but are not selected or claimed as passing.

The behavioral red witness copies `internal/http2/transport_security_test.go`
to an unchanged req v3.57.0 source tree under the same secured main module
graph and toolchain. `TestReqHTTP2Security_LazySettingsOverflowGOAWAY` must fail
on an observed DATA frame instead of GOAWAY FLOW_CONTROL_ERROR. Compilation
failures, timeouts, fixture packaging omissions and OOM are not this witness.
The arithmetic tests use new private flow types and are not part of that old
version witness.

The existing CI checks and Codecov 85% patch threshold remain required. Exact
upstream hashes identify unchanged imported source; the actual adapted source
lines in each of all ten adapted production files need actual added-block
coverage of at least85% and independent review. A blanket coverage exclusion or a
clean vulnerability database scan does not prove the private copy is fixed.
Stage the complete frozen repository and shared fixtures for remote validation.

When an official maintained req release contains equivalent repairs, compare
its API, browser and transport behavior, remove this local replacement in a
separately reviewed change, and retain the security regressions.

Tracking: [fork issue #1711](https://github.com/APIs-Gateway/sub2api/issues/1711),
[implementation PR #1712](https://github.com/APIs-Gateway/sub2api/pull/1712),
[permanent tracker #291](https://github.com/APIs-Gateway/sub2api/issues/291).
