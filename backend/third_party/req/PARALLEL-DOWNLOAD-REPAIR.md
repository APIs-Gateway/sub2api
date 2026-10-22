# Child1748: parallel download contract repair

Tracking: [child1748](https://github.com/APIs-Gateway/sub2api/issues/1748),
[implementation1712](https://github.com/APIs-Gateway/sub2api/pull/1712), and
[permanent291](https://github.com/APIs-Gateway/sub2api/issues/291).
Related public req reports: [417](https://github.com/imroc/req/issues/417)
(dispatch/error/cancellation) and [416](https://github.com/imroc/req/issues/416)
(Windows file ownership). Closed upstream reports do not establish a repair.
The inspected official master parallel_download.go blob
`cdff7288e9fdc71705fd9718d3f99819a547b58f` equals the copied v3.57.0 source.
This is a local production adaptation; it is not an official patch cherry-pick.

## Actual evidence motivating this source

Remote observation39529607848 used frozen aa656edd6f2aed8c595368ee802516fa571e317d
G fixtures. Linux named/full each actually ran32 leaves,23 passed and9 failed;
original401 passed independently. Eight failures concerned bad HEAD status,
ignored Range/200, invalid/missing Content-Range or416 being treated as data.
The ninth was separately classified G_DOWNLOAD_COMPLETION_TIMEOUT after the
peer actually sent and closed a short206 and the caller canceled. It was not
relabeled a completed business assertion. Windows stopped in the original
input checker watchdog before executing G, so Windows Close has no PASS claim.
The gateway currently has no production NewParallelDownload call site; this
repair does not assert a default gateway incident.

## Production boundaries

- Public constructors/setters and configured defaults remain. Do copies only
  lock-free configuration into invocation-local state. Each call owns a unique
  temporary directory and all its workers; dispatch observes cancellation and
  joins all workers before cleanup or output. Mutable caller configuration and
  concurrent writes to the same caller-selected destination remain caller
  responsibilities.
- HEAD must be200 with a positive length and identity representation. Empty or
  unknown HEAD length still returns an explicit error, as before. A206 segment
  must have one byte Content-Range exactly matching the requested span and
  HEAD total; known Content-Length must match. Actual body must contain exactly
  the required bytes followed by EOF. Malformed metadata, short/long bodies,
  nonidentity coding and416 cannot be successfully appended.
- If a server ignores Range and returns200, cancel/join all workers, close their
  owned bodies/files, and remove all partial files. Make one real GET without
  Range or If-Range; require200, no Content-Range, matching known length and an
  exact complete entity. Output remains untouched until this transfer succeeds.
  Client-wide Range/If-Range are removed from a private cloned client only;
  preserve the original cookie jar and other caller configuration. A caller's
  custom or nil/default http.Client.Transport is retained by identity; only the
  exact default req transport uses the private cloned transport. The caller's
  transport is never closed. Typed-pointer comparison avoids panicking on an
  otherwise valid noncomparable RoundTripper implementation. Independent
  protocol/I/O failures take precedence over a recoverable ignored-Range signal.
- Every response body, segment, reopened input and internally opened output is
  closed before return; cleanup is attempted on every completed run path.
  Caller-provided writers are not closed. File writes may still partially fail;
  the API does not promise atomic destination replacement or cancellation of an
  arbitrary blocking caller Writer. Length/range validation is not a server
  checksum or a promise to detect a same-length representation changing between
  requests; no new resumable state machine is invented.

## Preserved fixtures and new control

All seven G-owned source/docs files from aa656 are unchanged in this initial
production candidate. Original G32 Linux plus the additional Windows Close leaf
remain frozen. A separately reviewed future coverage/Windows-evidence repair
may change infrastructure with explicit provenance; no such change is hidden
in this candidate.

`parallel_download_fallback_test.go` adds exactly one parent-process public leaf:
`TestReqParallelDownloadFallback/ordinary_server_full_get`.
A real server ignores Range, observes the actual subsequent header-absent full
GET, authenticates the caller cookie/header and sends the exact97-byte entity.
It checks full bytes/SHA, actual response observation, no output before recovery,
one full GET, caller-writer ownership and unchanged original client settings.
A deliberately noncomparable delegating caller RoundTripper adds an actual
peer-observed authentication header to HEAD, ranges and the full GET. The test
requires all three real request kinds to traverse it and checks it was retained
and not closed. This extends the same leaf, without increasing the then-501 inventory.
It uses its own t.TempDir, not the isolated-child scratch protocol. Peer fixture
errors, timeout and transport failures cannot count as success. This source is
new original test code under this module's existing MIT license; shared helper
attribution remains in DOWNLOAD-CONTRACTS-LICENSE and its provenance.

## Required validation status

RAW source only: no local/87 execution, formatting, build, test or checker run.
Every executable check must run on remote CI after independent source review.
Original401 + reviewed F44 + Digest23 =468 are retained, plus G32 and the2
new full-GET/HTTP3 ownership controls =502 required named/full Linux leaves; Windows G33 is independently
required. Original132 files/89 production Go/14 tests/12 selected+2 retained
helpers, MVS365/25 negative controls and old-wire RED remain. This production
file becomes the13th declared adaptation, with119 unchanged originals. All13
added-block85 gates, Codecov85 and supplementary full-Git85 remain unchanged.
Child raw profiles must not be substituted with parent coverage; insufficient
actual coverage is a blocking result, not permission to synthesize counters.
The future remote formatter must retain this RAW commit and actual output;
no runtime PASS, Windows Close proof, LGTM or merge approval is claimed here.

## Source review correction after24f00d4fd

The initial Header-sharing finding was withdrawn: Client.Headers is promoted
from Transport, whose Clone already deep-copies Header. No redundant Header
clone was added. A separate confirmed issue was corrected: Client.Clone always
replaces http.Client.Transport with its req transport, discarding public caller
custom/nil transport. The local fallback now retains that actual installed
transport while retaining ownership of its own private clone only. This is a
static correction; actual remote test/format/race evidence remains pending.

## Private forced-HTTP3 clone ownership aftera79f93764

A separate confirmed resource leak affected the private fallback clone: forced
HTTP3 creates a new t3 and UDP socket, while the outer CloseIdleConnections
method closes only H1/H2. Only the local clone cleanup now calls its t3.Close in
addition to its normal idle cleanup. Registration precedes the body defer, so
the actual body and segment close first; cleanup still runs after an original
error, and a new close error overrides only nil. The global method and all
other production adaptation sources are unchanged. A caller/custom transport
is never closed. The existing internal t3.Close error propagation is preserved;
a close failure is not proof that every internal sub-resource was disposed.

New leaf: TestReqParallelDownloadHTTP3Cleanup/owned_fallback_socket_released.
New owned file: parallel_download_http3_cleanup_test.go, bringing owned paths
to51 and total Linux mandatory leaves to502 without altering G32/Windows33 or
the existing default HTTP1 fallback leaf. The test package is req solely to
reuse h3WireTLS and its owned-client cleanup fixture; all tested requests use
public client/NewParallelDownload/ForceHTTP3 APIs against real QUIC networking.
No private production socket, dialer or task state supplies the oracle. TLS and
peer setup attribution remains quic-go v0.57.1 MIT under the existing license.

The peer's public quic.Conn identifies each actual client loopback UDP endpoint.
Full GET headers must really arrive over TLS1.3/h3, with body completion held.
The exact active endpoint bind must fail specifically with POSIX EADDRINUSE or
Windows Winsock WSAEADDRINUSE10048; permission/invalid-address failures do not
calibrate occupancy. After the real full body completes, Do succeeds, bytes/SHA
match and the peer observes a normal remote QUIC close, rebinding the same owned
endpoint must succeed. Meanwhile a real later GET must reuse the caller's
original still-occupied connection. This is a network resource witness, not a
Linux FD counter or Windows file Close claim. Windows exclusive-address/other
precondition errors are explicit fixture failures, not a platform skip or PASS.

Windows constants were checked against primary sources:
[Go Windows synthetic errno](https://github.com/golang/go/blob/go1.26.10/src/syscall/zerrors_windows.go)
and [Winsock errors](https://learn.microsoft.com/en-us/windows/win32/winsock/windows-sockets-error-codes-2).
RAW source only: this new contract has not been formatted, compiled or executed;
remote CI and independent source review remain required.
