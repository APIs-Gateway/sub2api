# G public download contract review handoff, revised RAW source

Unformatted and unexecuted. This revision implements independently reported
G1-G4 and the separately reviewed optional child-profile design. New independent
source review and all remote validation remain pending; no LGTM, defect RED,
coverage increase or passing result is claimed.

## Scope retained

Four external-package test files use only exported req APIs with real loopback
HTTP1 peers. Ordinary SetOutput happens before Do returns; explicit Range/If-Range
and file append are tested directly, without a caller validation/restart state
machine or invented automatic resume API. Exactly32 Linux parent cases and1
additional Windows case retain their original names. All original132 files,
original14 tests,89 production files, F44, current401 inventory/checker, manifest,
dependencies and coverage policy remain untouched. Base remains exact2d6; unrelated
Oct20 scheduling-main composition belongs to root, not this test change.

## G1-G4 source corrections requiring independent re-review

- G1: completion observation expiry uses G_DOWNLOAD_COMPLETION_TIMEOUT, separate
  from G_DOWNLOAD_ASSERT and the parent's G_CHILD_WATCHDOG. Timeout alone is not
  data/metadata business RED. For the known failure-dispatch hang, completed real
  HEAD/Range/short-body flush/Close/cancellation remains manual review evidence;
  the outcome is still a failed completion observation, never a passing fixture.
- G2: FixtureError is rejected unconditionally, including cleanup/error paths.
  A read-only existing Client.OnAfterResponse hook records actual client response
  status/headers, request method/Range and Err; server preparation alone cannot
  establish rejection. Accepted protocol errors need the intended actual target
  response and exclude concrete context/network/timeout/I/O failures and the
  observed no-response transport error. A url.Error wrapper alone is not enough
  to reject a legitimate protocol error: inspect its cause and response evidence,
  without depending on an implementation error string. Likewise http.ProtocolError
  is not blanket-excluded once the actual target response is proven. Correct early
  rejection or a successful complete fallback may abort unused concurrent bodies;
  those write/flush outcomes remain honestly logged, with complete relevant body
  evidence still required before any business assertion.
  Error buffers are not read while merger work may still unwind.
- G3: Windows calibration now opens an owned control with comparable OpenFile
  flags/mode, proves rename fails while open, closes it explicitly, then proves
  rename/remove succeeds. Only successful Do plus actual complete entity reaches
  G_DOWNLOAD_FILE_CLOSE_ASSERT for destination rename/remove. Earlier Do or temp
  deletion errors use G_DOWNLOAD_PRECONDITION and do not prove outputClose.
  No Linux rename, FD count, /proc, forced GC/finalizer or caller-writer Close rule.
- G4: known-zero HEAD permits actual empty success or a clear non-timeout
  rejection. Unknown HEAD permits clear rejection or an observed full GET/200
  response plus correct complete bytes/hash. Neither case pins the old private
  bad-content-length implementation. Missing safe fallback after a proven bad
  partial response and nil error is a business assertion, not a fixture timeout.

## Child isolation and optional raw atomic evidence

Every parallel case owns a child instance of the same test binary. The parent
checks exact execution/body markers, actual process exit and its separate
watchdog; parent-owned scratch survives until child exit, containing failed
merger goroutines or Windows handles without GC or cross-case contamination.

Parent coverprofiles do not include child counters automatically. The optional
G-only SUB2API_G_DOWNLOAD_PROFILE_DIR, when set by a future independently reviewed
remote workflow, requires an atomic-instrumented binary and an existing absolute
external directory. Each child gets a fresh unique directory and only the
standard -test.coverprofile argument. Go testing writes child.atomic.out; the
helper never fabricates hits, rewrites counts/paths, combines profiles or replaces
existing named/all/uploaded inputs. After process exit it preserves raw output
and records actual startup/exit/watchdog/markers/profile hash/size/status in JSON.
The unset option adds no evidence/profile files or extra subprocess flags.

Only normal exit0 with exact execution/completion markers, no failure markers and
a readable nonempty atomic profile is candidate data pending independent full
integrity/source review. Nonzero/race/panic/fixture/completion-timeout/kill/missing
or malformed evidence remains failed forensic material, never passing coverage.
The original child output/error and watchdog state are logged before evidence
write failures; persistence failure also fails the fixture. Exact Git/binary/MVS
binding, complete record validation, actual case results, always-upload and any
later use/merge of child counters remain root workflow/review responsibilities.
No G runtime/profile evidence exists yet and no coverage improvement is claimed.

## References, overlap and next gate

Official imroc/req416 and417 are CLOSED with no linked fix commit in fetched
history/timelines; observed master parallel source equals v3.57.0/current fork.
No application NewParallelDownload call was discovered outside imported req, so
these are API hypotheses rather than claims of a triggered gateway-production
failure. All41 open PR inventories and10 inherited-source overlaps are retained
in durable root evidence; refresh/cross-link before any production repair/merge.

Freeze revised RAW source and exact hashes/names for root plus independent review
before remote formatting or execution. All executable validation is remote CI
only, never local or87. Preserve raw child/named/all profiles, complete actual
results, original401 and every inherited gate, original Codecov85 and complete
Git85. Root owns final inventory/manifest/proof integration; no source publication,
new child issue, production repair, exclusion or threshold reduction is authorized.

## Second independent source review: G5

The 93abd RAW revision still rejected every successful `range_416` result, including a legitimate full GET/200 recovery. It also required the wrong-start partial body to complete even when a successful full recovery had legitimately discarded that body. auditOps identified both boundaries independently; root authorized this narrow revision.

`range_416` now accepts only a clear non-timeout rejection, or a real matching full GET/200 with completed wire bytes and exact complete entity bytes/hash. An error representation is never successful output. `wrong_range_start` permits discarded partial Write/Flush aborts only when that same real safe full recovery is proven; otherwise the necessary partial must complete before entity assertions. FixtureError remains unconditionally rejected. No test names, production sources, inherited gates or profile accounting changed. This is unformatted RAW source; independent re-review and all remote executable evidence remain pending.
