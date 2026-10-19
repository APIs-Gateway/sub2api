# F public client protocol source candidate

Formatted candidate based on security PR1712 head
`c541f5a171fe85a053a6199e10304a386e852d22`, tree
`09e841970f893e41c4a8e4a02c8059545a03f238`. Child1711 and permanent291
remain the governing issue links. This is source for independent review;
no candidate test, formatter, compiler or build has executed locally or on87.
The implementation branch remains unpushed. Only the separately sealed remote
formatter controller has run; candidate tests and builds remain unexecuted.

Initial raw candidate2fdf1e56 is retained in history. Independent review revised
source-body closure timing, removed exact auth-int factory call count, added
completed peer write/flush observations before401 body assertions, and clarified
the charset coalescing fixture. Only the remote formatting described below has
subsequently run; no candidate test execution occurred.

The existing sole untracked peer draft was preserved before the worktree was
fast-forwarded. Its original5588 bytes and SHA256
`0e6458474cec81c56a1fc29141811a02c7b256952d45c1118dd3c1934da9870b`
remain unchanged in this candidate; the preservation copy is
`/tmp/sub2api-req-f-preserved-oct18/client_behavior_peer_test.go`.
Freshly fetched refs at implementation start were origin/main
`5d44961f525e82cdafb63a130ef64448db44cffa` and upstream/main
`b0a02a3304a0a78cdd003ddda1dd23ba02c03084`.

## Candidate behavior and source anchors

`CLIENT-BEHAVIOR-PROVENANCE.json` freezes44 candidate leaves, exact raw source
hashes and pinned primary source URLs/Git blobs/hashes. These are behavioral
subsets of icholy/digest v1.1.0, Go go1.26.9, x/net v0.60.0 and the already
preserved req v3.57.0 tests. They do not claim upstream suite equivalence.
Digest's MIT and Go/x/net BSD terms are included in CLIENT-BEHAVIOR-LICENSE.

*13 digest leaves: actual MD5/auth and SHA256/auth-int challenge/replay, escaped
request target, exact POST body, bodyless and nonreplayable source body, cached
nonce counts, nonce rotation, bounded wrong-password replay, initial/second
GetBody failure, malformed/unsupported challenge handling, ordinary401 cache
eviction and8 concurrent cached-nonce requests. The peer computes expected
hashes using stdlib hashes from observed method, target, entity and credentials;
it does not call digest.Digest as its oracle. Factory body creation is observed
under a mutex for auth-int replay, with
nonzero factory use and completion signals for every observed resource. No
exact factory call count is required. Source closure uses a bounded context
wait because a RoundTripper may close request bodies after returning. The
concurrent peer validates each hash
and the test requires unique counts2 through9 without sleep ordering.

*7 redirect leaves: each public allow policy reaches the actual destination;
sensitive credentials/cookies stay absent after a cross-host hop and return hop;
explicit authorization copying affects only the configured sensitive field and
preserves a destination value already installed by an earlier policy.

*3 cookie leaves: actual Set-Cookie on a redirect replaces/deletes matching manual
cookies while preserving unrelated cookies; factory-backed clones isolate real
jar sessions and ClearCookies clears the owner's jar/common cookies;
caller-provided jars intentionally remain shared and survive ClearCookies.
Real client wrapper middleware must run for requests from both the source and
clone. No pointer identity assertions substitute for HTTP behavior.

*21 response leaves: manual XML and JSON fallback, exact real parser errors,
automatic result/error publication, custom ErrorState on204, sticky truncated
response read error with retained bytes, transformer sentinel ordering and body
closure, and short Windows1252/UTF16 BOM/unknown/no-encoding streams using64/512
byte caller buffers. Each stream requires exact bytes and terminal EOF. For
metadata sniffing only, the first production decoder input chunk is coalesced
to the owned short fixture length and its bytes are checked, so TCP packet
boundaries cannot choose whether full metadata reaches the decoder. This
io.ReadFull fixture controls first-chunk error semantics; later reads delegate
to the actual body. Exact decoded bytes and final EOF are still required.

## Ownership and overlap

All HTTP peers use httptest loopback listeners on port0. Redirect authorities
are virtual and resolved only by a DialContext allowlist to the owned listener.
Unexpected authorities fail before DNS. The common client timeout is5 seconds;
concurrent digest work is bounded to8 requests. Peer observations are copied
under a mutex, request bodies are completely read before each response, connection
identities are captured by ConnContext, and test cleanup closes idle transports
before the peer. Ordinary/unsupported401 peers record exact payload, written
length, write error and actual Flush error; bounded observation and exact
request count are required before REQ_DIGEST_401_BODY_ASSERT can be emitted.
Handlers never call testing.Fatal and contain no ordering sleeps.

Response close observers wrap the real transport body and preserve all reads and
errors. The Content-Length short-response fixture drives public Response.ToBytes,
Bytes, Unmarshal and sticky-error behavior. It does not add transport framing,
pool or retry assertions already owned by the HTTP1 lifecycle contracts.
Existing GBK boundary coverage is not repeated. Deprecated request-level digest
and Go308/GetBody redirect variants are deferred; these are documented subsets.

## Hypotheses requiring genuine CI RED before production changes

1. digest.go drains/closes a401 response before FindChallenge, including when
   there is no usable Digest challenge. Ordinary401, Basic-only and unsupported
   Digest responses should remain readable HTTP responses. The candidate
   requires the actual wire body; it does not accept a closed-body read error.
2. GetBody clone failures may leave the original caller's Body unclosed. The
   public RoundTripper contract requires closure including on errors. The
   candidate checks both the original body and any replay body already opened.

These are source hypotheses only. No production edits have been made and no
upstream defect is claimed from an unexecuted test. Exact-head remote RED,
all-state duplicate/PR overlap inspection and root review must precede a fix.
A real reachable defect or race remains blocking regardless of its age.

## Remote formatter evidence

Raw revisionc5fb66a3 supplied five exact Go blobs to a formatter-only orphan
controller. Initial run39191156507 failed because its verifier wrongly rejected
gofmt-d exit1 with a real diff on unformatted input; reportFALSE and no complete
marker remain preserved. The corrected controller4e0ab97f run39191408664 passed
with artifact12159319004, SHA256
`e99b29b6801c491ee0af12d02fe62881d26c06070cf5883ca3a92d32fc15ae4b`.
All five originals matched rawc5fb Git bytes; all gofmt-w and post-format-d/-l
commands returned0 with empty post-format output/stderr. Formatted outputs were
byteexact to those from the first run. Root independently checked the new
artifact before authorizing these exact copies. Provenance retains raw seals,
formatted seals and both runs. No source, compiler or test failure is inferred
from the formatter controller defect. No test/build result is claimed.

## Independent review and integration gate

The candidate changes only new F test/license/provenance/review files. All
original req tests are byteexact against the original132 manifest:14 test files,
comprising6 root files and8 internal files (including1 charset test file). The
existing401 mandatory cases,89 production
files,11 adaptations,29 owned files, frontend/build/race checks and both85 coverage
gates are untouched. This candidate makes no coverage percentage promise.

Next steps belong to root: independent review of this frozen formatted source,
then a separately reviewed genuine F44 baseline RED controller using the complete
frozen repository and unchanged401 baseline. Actual named/full/race results,
exact source inputs and unchanged MVS must be observed. Only genuine body-loss or
closure markers with completed fixture prerequisites can justify a production
fix; other failures require investigation. Integrate the44 reviewed leaf names,
owned file list and final hashes into the existing inventories before requiring
exact evidence. Any CI failure is evidence to inspect, not a passing phase.
No approval, LGTM, merge or closing action is included here. Permanent291 must
remain open.
