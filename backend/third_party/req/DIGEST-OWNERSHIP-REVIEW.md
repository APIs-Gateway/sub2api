# Digest ownership regression source: child1743

OLD source candidate on normal composition85a001e5 (latest fork mainc3e3eed31,
formal security2d6 and reviewed F9c0c). Fresh-main scheduling/VERSION changes,
all F8 inputs, original401 and required85 gates remain preserved. Production
digest.go is still original0695d8c3. No tests, formatting, compilation or builds
have run on this source locally, on87 or in CI. Source and the later NEW repair
must be independently reviewed before separately authorized remote execution.

23 new mandatory public contracts cover original GetBody/stream ownership,
first/second clone and auth-int factory body+error boundaries, fresh/cached
hash-reader errors and Close precedence, and actual real-peer response ownership.
Exact names and raw hashes are in DIGEST-OWNERSHIP-PROVENANCE.json. All44 F leaves
and all401 old mandatory leaves remain required, giving468 mandatory leaves.
Original132/89 production/14 tests/12 wildcard selections and old11 adaptation
hashes remain unchanged in OLD. New ownership inputs and F8 are declared owned.

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

A subsequent minimal production commit may change only digest.go and transparent
adaptation metadata/gates to12 adaptations/120 unchanged originals, preserving
all11 prior records. No dependency, Probe, nonce/cache policy, default-client,
billing/payment/credit/public-key/scheduling/deployment behavior is changed.
New digest executable blocks require85; original Codecov85 and supplementary
complete-Git Go85 remain separately mandatory. No approval, LGTM or merge is
claimed. Issue1743 remains linked to1711/1712; permanent291 never closes.
