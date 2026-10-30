# Transparent original test / owned trace portability adaptation

This is test-only source preparation from17cbf851d29807fd7bf48c5791790099bb3657cc.
It does not modify production trace/request/response code or any business
request timeout. Child1748/PR1712 and permanent291 remain linked by the existing
repair ledger. No dump-file ownership change is included here.

## Original actual Windows failure

Observation39744585493/controller29ee7f01/candidate836c86fa artifact12411476184
SHA2565a9835fed2cf0e9583a33f697332cb412ed8ffe9b42fa28ccaf37a3720aae9c0
actually failed named/full Windows. The original TestSetFile required Unix
`no such file` text while Windows returned its file-open error text. The raw
old log did not print errors.Is/errors.As identity. Original TestTraceInfo had
ten generic boolean failures, without identifying each field. Owned late-DNS
cancel/reused-HTTP2 trace cases recorded0 durations, and manual EOF recorded
unchanged pre/post durations. These failures are retained; this source alone
is not a runtime PASS or proof of their underlying clock cause.

The H3 fixture occupancy failure and two dump-file leftovers in that artifact
remain separate. Nothing here skips those failures or deletes their evidence.

## Original request_test.go is explicitly adapted

The original official132-file archive, its original14 test-file identities,
12 selected+2 retained helper identities and original reference hashes remain.
The current request_test.go bytes are now different and registered separately
in originalTestAdaptations; they must never be described as untouched upstream
bytes. Original97/338/393/401 plus F44/Digest23, G and parent contracts remain
mandatory. The current partition is118 unchanged originals +13 production
adaptations +1 original test adaptation =132. Production89 and all13 production
source hashes and85 gates are unchanged. Future remote formatting requires a
new transparent current test hash, retaining the original archive hash.

- TestSetFile retains the successful real upload and exact file data. The error
  case uses a missing path under t.TempDir and requires errors.Is(os.ErrNotExist)
  plus an actual *os.PathError with open/exact path. It prints real identity;
  no production error text or generic-error allowance is introduced.
- TestTraceInfo retains its two existing transport modes and three original
  client-enabled/client-disabled/request-enabled requests. An owned real TLS
  peer establishes actual elapsed prerequisites, sends/flushes the first body
  fragment, waits for the client's public GotFirstResponseByte, then starts
  another real elapsed gate before releasing the suffix. Whole body, actual
  protocol/peer completion, public TCP/TLS/GotConn events, real addresses,
  ReceivedAt, reuse, trace enable/disable and TotalTime equality are checked.
  The two existing modes are explicit tc()/expectedHTTP2 and
  tc().EnableForceHTTP1()/expectedHTTP1 without new test/subtest names. Both the
  actual response and real peer must report that expected protocol. Default TLS
  requires actual h2 ALPN; forced H1 accepts its legal empty or http/1.1 ALPN.
  First-request public GotConn.Conn and peer ConnContext identities are saved;
  requests2/3 explicitly require Reused=true and those same two real connection
  objects, matching reversed endpoint addresses and no new TCP/TLS callbacks.
- Every ConnectTime/TCPConnectTime is nonnegative; even a fresh loopback TCP
  phase can have same-tick endpoints. Public successful ConnectStart/Done and
  addresses witness actual fresh connection work. A reused connection must
  have no fresh connect/TLS callbacks, and absent trace phases remain0.
- Fresh TLS positivity has its own real server TLS GetConfigForClient elapsed
  barrier. Positive total/first-response/body time has real peer/body causal
  prerequisites. No production clock assignment or delayed fake trace callback
  supplies a duration. Public observation is composed into the caller context.

## Owned trace fixture prerequisites

The existing trace_concurrency_test.go test names, callback concurrency,
late-callback freeze checks and request deadlines/client timeouts stay.
NormalTLSAndReuse and RequestReuse both retain their strictTotal>0 checks;
each real handler establishes the same narrow elapsed prerequisite before its
original body, including the second actually reused request.
Explicit cancel occurs only after the actual dial-entry event and a measured
real clock advance; the original500ms deadline cases are unchanged. Reused
request handlers establish real elapsed time before writing their actual body.
The manual EOF case keeps the real first fragment flushed/held and now observes
public first-byte completion before taking its snapshot; after the concurrent
snapshot worker starts it requires real elapsed time before releasing suffix.
Strict before/after duration increase, ReceivedAt transition, payload and race
assertions remain. The shared real TLS fixture gates its genuine handshake.

A timer is only a clock sampling opportunity: a strictly later real time.Now
reading must be observed. Context cancellation, missing actual event or guard
expiry is fixture failure. New peer/clock watchdogs use5s; existing owned5s fixture guards and500ms
deadlines are retained, not promoted
to accepted business results. The new trace_elapsed_peer_test.go defines no
new test leaf and is original fixture code under this module's MIT license.

## Exact checker and remote negative controls

The packages checker requires the separate one-file registration, original
archive/reference identity, required original TestSetFile/TestTraceInfo names,
exact candidate bytes/hash and actual selected request_test.go file identity.
All other original bytes still match originalFiles unless in the existing13
production list. Original wildcard14/12+2 selection is unchanged. Full actual
JSON additionally requires each of the two adapted original tests RUN/PASS
exactly once, with no failure/skip. Existing525 security inventory stays intact.

The new backend/tools/req-original-test-adaptation-controls.py is an explicit
CI source path, outside any observer infra exception. It runs only in remote
CI after real go list and the normal packages gate. It keeps raw actual
modules/packages input, copies the complete controlled module to external case
directories and remaps only real fixture locations. Four actual CLI controls:
clean passes; removed registration, incorrect candidate hash and missing
selected request_test.go identity must fail at their specific gates. Every
original command exit/stdio/hash/watchdog is retained. No Go PASS events or
coverage counters are manufactured. No production timeout or observer whitelist
is changed. Remote CI source formatting includes the changed/new Go files.

## Status

RAW unformatted/unexecuted source only. No local/87 formatter, project checker,
AST, test or compiler execution. Root plus independent review, remote formatter
and exact-head Linux/Windows named/full/race and all existing security/85 gates
are still required. Source changes do not waive old failures or approve1712.


## Review correction after3d1b7ccf9

Root found that3d1 compared response protocol only with the peer's actual
protocol and branched on the observed Reused flag. A simultaneous downgrade or
three fresh connections could therefore pass despite the stated mode/reuse
contract. That source and finding remain historical evidence. The narrow
follow-up pins expectedHTTP2/HTTP1 and the false/true/true reuse sequence, using
both actual public client connection and independent public server ConnContext
identity. No production path, duration, test identity or timeout changes. Actual
runtime results remain pending; this correction is not an execution PASS.

## Remote formatting and evidence completeness, 2026-10-24

Remote formatter run40075095188 at controller9893c89cdeb5865ba8c0be23fe5c32edc4ed719c
used pinned Go1.26.10 and frozen application sourcee34056a04ab6ade971effac629b96dbb92c58b25.
Its three input Go files and twelve actual format/diff/idempotence operations
were independently audited against both complete frozen Git snapshots. The
original adapted request_test.go output was byte-identical. The two owned
trace test outputs are imported unchanged from that remote artifact. No local
or server87 formatter, test, compiler or project checker was executed. The
original archive hashes and132-file partition remain118+13+1.

Natural CI40075095258 at the same controller actually passed the four original
test adaptation controls (clean0, unregistered1, wrong-hash1, missing-selected1),
then stopped at the expected raw format differences before named/full tests or
profiles. Those unexecuted phases are not runtime evidence. Its uploaded
controlled copies omitted17 hidden fixture files because upload-artifact
defaults to excluding hidden paths. The permanent req evidence upload now
explicitly includes hidden files so subsequent copies retain the complete
189-file module. This changes evidence retention, not the test command or
source acceptance gates. No production secrets or services are included.

The current composition adds the independently reviewed default-output leaf:
57 owned paths and526 mandatory security leaves. Original trace runtime,
Linux/Windows suites, dump-file ownership and both PR85 coverage gates still
require their actual exact-head results; formatting is not approval.
