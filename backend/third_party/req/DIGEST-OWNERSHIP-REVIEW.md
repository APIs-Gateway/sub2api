# Digest ownership regression source: child1743

NEW raw production candidate after OLD test-source7b0d92fb, on normal composition85a001e5 (latest fork mainc3e3eed31,
formal security2d6 and reviewed F9c0c). Fresh-main scheduling/VERSION changes,
all F8 inputs, original401 and required85 gates remain preserved. OLD production digest.go remains original0695d8c3 at that parent. NEW applies the
minimal repair described below, with Go fixtures byteexact to OLD. No tests,
formatting, compilation or builds have run on either new source locally, on87
or in CI. Source and CI must be independently reviewed before separately
authorized remote execution.

23 new mandatory public contracts cover original GetBody/stream ownership,
first/second clone and auth-int factory body+error boundaries, fresh/cached
hash-reader errors and Close precedence, and actual real-peer response ownership.
Exact names and raw hashes are in DIGEST-OWNERSHIP-PROVENANCE.json. All44 F leaves
and all401 old mandatory leaves remain required, giving468 mandatory leaves.
Original132/89 production/14 tests/12 wildcard selections and old11 adaptation
hashes remain unchanged in both OLD and NEW. OLD has11 adaptations/121 unchanged
originals; NEW transparently adds digest as12th adaptation/120 unchanged. New
ownership inputs and F8 are declared owned.

Every owned request body records every Close call; sync.Once only closes its
independent completion channel. Distinct resources carry independent sentinel
errors; successful auth-int readers are closed by the pinned helper. The helper
intentionally ignores deferred Close errors, which must not replace successful
real HTTP authentication or a primary read error. Unsent-clone cleanup errors
must not replace preparation/factory errors. No exact steady-state factory call
count is asserted; selected failure/hash boundaries are deliberately controlled.

Real httptest peers supply status, body, challenge and wire observations. The
read-only response wrapper is registered before CommonDigestAuth, so it sees
actual inner peer bodies and counts their actual reads/Close calls. Tests never
construct a replacement HTTP response. Request and response cleanup never adds
another Close. Ownership reports collect all resource counts with a bounded
completion wait, accepting asynchronous underlying transport closure. No sleeps
or idempotent-close witness can hide double-close or leaks.

The existing complete diagnostic39355019380 proved five genuine F digestRED and
39 other F PASS while original401 passed. Three earlier failed controllers and
raw evidence remain preserved. That diagnostic is not an application PASS and
does not establish the new23 cases. Their OLD failure disposition remains a
hypothesis until exact remote JSON; unexpected fixture/compiler/race failures
remain blocking. OLD/NEW must use identical Go fixtures and full frozen repos.

The NEW minimal production commit changes digest.go and transparent adaptation
metadata/gates to12 adaptations/120 unchanged originals, preserving all11 prior
records. It parses401 challenges before consuming a response that may belong to
the caller, closes only internally discarded challenge bodies, releases original
GetBody bodies once, closes unsent preparation clones, closes streams even on
ReadAll failure with read-error precedence, and locally wraps GetBody so a
reader returned with an error is released in clone and auth-int paths. Sent
bodies and successful hash readers retain their existing owners. No dependency, Probe, nonce/cache policy, default-client,
billing/payment/credit/public-key/scheduling/deployment behavior is changed.
New digest executable blocks require85; original Codecov85 and supplementary
complete-Git Go85 remain separately mandatory. No approval, LGTM or merge is
claimed. Issue1743 remains linked to1711/1712; permanent291 never closes.
