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

The 93abd RAW revision still rejected every successful `range_416` result, including a legitimate full GET/200 recovery. It also required the wrong-start partial body to complete even when a successful full recovery had legitimately discarded that body. auditOps independently identified the 416 boundary; root first identified the wrong-start partial boundary and auditOps cross-reviewed it. Root authorized this narrow revision.

`range_416` now accepts only a clear non-timeout rejection, or a real matching full GET/200 with completed wire bytes and exact complete entity bytes/hash. An error representation is never successful output. `wrong_range_start` permits discarded partial Write/Flush aborts only when that same real safe full recovery is proven; otherwise the necessary partial must complete before entity assertions. FixtureError remains unconditionally rejected. No test names, production sources, inherited gates or profile accounting changed. This is unformatted RAW source; independent re-review and all remote executable evidence remain pending.

## Second independent source review: G6

Explicit cancellation mismatches previously assigned every error to the business assertion marker. A client timeout, deadline, or unrelated I/O failure could therefore masquerade as cancellation contract RED. The ordinary and parallel cancellation paths now accept expected `context.Canceled` first, then classify concrete unrelated causes using the existing chain-aware classifier before any remaining business assertion. A nil cancellation error still violates the cancellation contract. Valid-positive ordinary and parallel download errors use the same unexpected-error classification. Existing 3s timeout, 32+1 names, peer prerequisites, and all production sources remain unchanged; remote execution and independent re-review are pending.

## Remote formatter output imported; runtime validation pending

RAW source `425e03baf1067487842eddab9030ba3e72c48999` remains in Git history. Controller `78db36b3c54e1fd5fab13c5e4deac7decabeb094` ran the pinned Linux Go1.26.9 formatter in remote CI run39356244024/attempt1. Artifact12235142612 (ZIP SHA256 `4adb4ddf148dfe84707c3ed3202c95d119191528b0e69e97a54366b6e1cba4a2`) contains the complete unchanged4481-file source,4482-file controller and exact raw/formatted bytes. All mode/blob/SHA, before/after snapshots and full archive contents were independently checked.

Three raw `-d` commands actually exited1 with nonempty formatting diffs; the Windows file exited0 with no diff. All four `-w` and post-format `-d/-l` commands exited0 with empty stdout/stderr. Root review `3a7f2089783236b934b6986081f7a5738715065a1594237c19f6ce6a96c10c48` and independent review `dbc424b950ae44cf60e99f4f6f4f86b4a800309621561863d2efb7c909473f6d` accepted the actual evidence; the full diff changes only import ordering and whitespace. Four artifact output files are imported byte-for-byte; Windows is unchanged. Historical RAW seals are in provenance alongside the current formatted seals. No local or87 formatter/project execution occurred.

Formatter SUCCESS proves only those formatting operations. Inherited RAW CI39356243844 failed its unchanged controlled-module exact132+declared-owned input preflight before req/G tests; subsequent backend tests were skipped. No runtime PASS or G business RED is inferred, and the input gate is preserved. Security39356243763 succeeded separately. All32+1 names, original132/14/89 sources, mandatory401 inventory, F files and85% policies remain unchanged. This formatted source awaits independent source review and later explicit remote compiler/test authorization; no new application push is authorized by this import.


## Child coverage metadata and Windows full-input infrastructure (pending remote review)

The original abc37 observation and its nine real G failures remain preserved.
Current optional child evidence prepares fresh covdata, passes explicit
test.gocoverdir, replaces child GOCOVERDIR, and records actual auxiliary inputs
before/after. Complete selected definitions include unlinked zero blocks; no
parent/other-child runtime counters are copied or merged. Business assertions,
scenario names, request behavior and original timeouts remain unchanged.

The inputs gate batches HEAD-tree/index metadata, still hashing every tracked raw
blob and SHA256 with untracked/ignored and source-after checks. The preflight
watchdog remains300s. Remote controls use real Go/Git processes; corrupted copies
are labelled negative-control data. No local/87 formatter, syntax/AST, checker,
test or build ran. This raw helper awaits remote formatting and double source/
actual review; historical formatting does not validate these changed bytes.
Formal aggregate85 gates remain HOLD.


## Actual remote formatting39742212554

The exact b9f source and2ea controller were frozen as4513/4514 complete Git inputs.
Go1.26.10 actually ran7 inputs x4 operations; all28 outcomes and original raw
ZIP/tars/index/source before-and-after were independently checked. Only field
alignment/blank lines/operator spacing changed. The helper output is imported
verbatim here; its current hash is in provenance, alongside the historical raw
seal. Production outputs were imported separately in8013 and normally merged.
No local/87 formatter or project code executed. This is formatting evidence only;
new Linux/Windows behavior, metadata controls and all85 gates remain required.


## RAW parent525 observer composition (2026-10-23; not executable)

Normal merge of production `b29760194991dbc88c6d2ae985a117a1e28aa4db`
into observer `29ee7f01467489c5c16b73840123fc1c9eae4560` combines the actual
formatted836 metadata helper with the exact parent-suite dispatch and temp-dir
guards. The original case tables and business assertions, parallel/H3 production
behavior, old mandatory502 and retained468 remain unchanged. The only new23 executions
reuse the existing real HTTP contracts under their explicit new testing names;
their counters arise in the parent runtime. The helper's combined RAW hash is
current in provenance, with both input seals and actual formatter history kept.

The observer now requires57 Linux /58 Windows G leaves. Both named/full phases
record and require one start/completion marker for each new parent leaf and
reject child-delegation markers under those names. Full execution additionally
requires all525 mandatory leaves, reporting original502 separately. The old
32 Linux/33 Windows fixtures and23/24 actual isolated child profiles keep their
existing names, metadata associations, watchdogs and assertions. All raw parent
and child profiles remain separate; original four formal profiles and13-file85
checks cannot gain child counters. MVS, full frozen Git input checks, metadata/
parser/input/wrapper controls, readonly modules and race execution stay required.

The eight infrastructure paths are unchanged in scope; every other mode/blob
must match production b297. SOURCE/TREE and workflow checkout use explicit
PENDING markers until authorized remaining source composition, remote formatter
output import and final exact source/controller freezes are independently read.
This branch has not been pushed or executed. Historical Linux502 observation
PASS does not validate these changed bytes, Windows behavior or any85 gate.
Current per-file parallel coverage and original aggregate85 failures remain
blocking. No LGTM or merge approval is issued by this authoring step.


## RAW calibrated probe composition (2026-10-23; pins remain pending)

The normal merge now includes production anchor
`0abbb69afed23db92501b4e5abf170780a66dc9a`, whose e7 parent/probe merge and
one-line remote-only gofmt-list addition were independently reviewed. Manifest
and repair document are exact production-anchor bytes. All other noninfra
modes/blobs are also exact0ab, with the same eight infrastructure paths; the
probe helpers do not expand that allowed set. There are54 declared owned paths,
132 original inputs,13 production adaptations and525 mandatory leaves. Native
Windows probe calibration is a changed test fixture awaiting actual CI, not a
waiver of the prior calibration or original test failures. G57/58 and23/24
isolated child profiles remain as registered above.

A separate proposed Go1.26.10 formatter reuses the previously reviewed remote
formatter framework for five files only: current combined wire helper, new
parent contract, H3 cleanup test and both platform probe helpers. It will stage
the complete exact Git source/controller, keep raw/formatted external copies and
all20 operation outcomes, and preserve both checkouts before/after. Proposal
preparation does not execute formatting. The original raw profile boundaries,
controls, old-red/MVS and all85 gates stay unchanged. DOM1749 is separate. Final
observer source/controller pins and any publication still require root review.


## RAW default output registration526 (2026-10-23; no execution)

A single separately named public HTTP contract now tests URL-derived output
filename/default settings, long-file truncation, complete content/hash, adjacent
caller-file preservation and temp cleanup after Do. It runs directly in the test
process with fresh peer/client/directories and bounded cancellation; no original
fixture/helper or production source changes. The original525 identities remain,
with526 mandatory leaves/55 owned paths and58Linux/59Windows observation scope.
Its real completion marker is checked in raw per-test JSON; child delegation
cannot substitute. Original23 parent markers and23/24 isolated child profiles,
all old controls and raw-counter boundaries remain unchanged.

Natural16bf actual188/222 coverage remains a failed historical result. No local/
87 executable validation ran, and no new coverage PASS is asserted. Current
PRODUCTION0ab is an earlier anchor; root must integrate the new noninfra files
and freeze a new production anchor before this pending observer can execute.
SOURCE/TREE/checkout remainPENDING. Independent review and remote formatting
must precede any runtime publication. No LGTM or merge approval is issued here.


## Production1cbe source binding (2026-10-23; formatter pending)

Root imported the exact five noninfra files from bdb into production-only
`1cbe4fcc134dcf00637116f4209fad1118c26919`, parent0ab. Independent narrow
import review verified exactly those five mode/blob/raw-byte inputs and no other
delta. A normal merge now retains this anchor in source ancestry; all noninfra
mode/blob values match1cbe and the original eight-path boundary remains. The
observer PRODUCTION pin is1cbe; SOURCE/TREE/checkout deliberately remainPENDING
until independent final source/controller review and remote formatting. No
fixture/production inputs were rewritten by the binding update. Newfile actual
formatting/runtime and all526/85/Windows obligations still require remote CI.
