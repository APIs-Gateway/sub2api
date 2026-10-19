# G public download contract review handoff

RAW, unformatted and unexecuted. No source defect or passing result is claimed.

## Scope

Four new external-package test files exercise exported req APIs with real loopback
HTTP1 peers. Ordinary SetOutput runs before Do returns; explicit Range/If-Range
and file-append behavior is tested directly, without a caller validation/restart
state machine or an automatic resume claim. Parallel tests check actual complete
entity identity and error/metadata responsibility. No original file, production
source, dependency, F44 fixture, current401 inventory/checker, manifest or coverage
policy is changed. Expected parent inventory is32 Linux cases plus1 Windows-only
case; source hashes and exact names are in DOWNLOAD-CONTRACTS-PROVENANCE.json.

## Independent review focus

1. The peer records actual HEAD/Range/status, controlled body writes/flushes and
   explicit closes/cancellation. Complete relevant evidence precedes business
   assertions. A safe early HEAD rejection need not issue a GET; a correct error
   may abort concurrent bodies, which are logged honestly rather than required to
   finish. Never classify a fixture write failure as proof of a production bug.
2. Error/cancel source paths may strand goroutines: every parallel case runs in a
   separate instance of the same test binary. The parent checks child execution
   and completion markers, actual exit, full output and its independent watchdog.
   Its scratch directory is parent-owned until the child exits, so Windows leaked
   handles do not defeat cleanup or require GC. No watchdog kill is a PASS.
3. The failure-during-dispatch case first verifies an actual valid Range response
   with a fully written/flushed intentional short body and connection close,
   then cancels and checks bounded Do return. The explicit assertion is separate
   from the process watchdog. No private queue/waitgroup state is read.
4. Invalid partial metadata may yield a clear error or a separately observed safe
   full200 recovery; simply concatenating unusable206 parts cannot pass even if
   the bytes happen to match. A server can legitimately ignore Range; that case
   accepts error or exactly one correct entity, without prescribing scheduling.
5. Close proof is Windows-only: successful controlled download, closed-file rename
   control, then rename/remove the library-created destination. No Linux rename,
   FD count, /proc introspection, runtime finalizer or caller-writer Close policy
   is used. Platform exclusion on Linux is not a claimed Windows PASS.
6. io.Writer errors remain identifiable; valid-positive entity bytes/hash and
   existing-file truncation are checked. Output buffers are not read after an
   error that may leave a merger goroutine unwinding.

## Official references / limitations

Official imroc/req416 and417 are CLOSED but do not cite a fix commit in fetched
history/timelines. Fresh master parallel source equals v3.57.0/current fork. The
file has no discovered application call outside imported req, so do not claim a
currently triggered gateway-production failure. Those facts and all41 open PR
filename inventories are retained in the root task's durable g-download evidence.
Ten open PRs include the same imported source; all four relevant production blobs
at their exact heads equal this base, and none implements the G tests. Root must
coordinate any new issue/repair/overlap after genuine remote evidence.

## Execution and integration

All format, compile, race, test and verification execution is remote CI only.
This RAW commit has not been formatted or compiled locally or on87. Root review
and a separately reviewed remote workflow must precede any run/publication. Keep
raw/full evidence and all existing suites; root will own final mandatory names,
source manifest and proof integration. No invented skipped PASS, exclusion,
threshold reduction or substitute coverage metric. No LGTM or merge authorization
is implied by this handoff.
