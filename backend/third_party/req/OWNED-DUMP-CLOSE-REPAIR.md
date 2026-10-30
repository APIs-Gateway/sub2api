# Explicit owned dump close (#1752)

This is a controlled local lifecycle repair in the copied req v3.57.0 tree,
required by [#1752](https://github.com/APIs-Gateway/sub2api/issues/1752) for
[the existing security PR](https://github.com/APIs-Gateway/sub2api/pull/1712).
It is not an official upstream backport or a general dependency upgrade.
The permanent tracking issue [#291](https://github.com/APIs-Gateway/sub2api/issues/291)
remains open.

## Evidence and source boundary

The saved Windows run40182645563 reported all526 required leaves passing, but
left `tmp_dumpfile_req` and `tmp_test_dump_file` in the shared test directory.
The original tests ignored os.Remove errors. That run contains no same-process
Remove error or handle identity and does **not** establish a sharing violation.

Fresh official master2affd4787737e8315ff3ffd61a3388eda32d0755 and release
v3.61.0 (de24fbe79422bd92880c0fb5b1d4d7bca2a2cd0f) still open dump files without
an owner-close API. The master/release dump adapters are byte-identical; their
other changes are not imported. The implementation starts from the preserved
bf4dfe25dac427186ff32c93a5c9d45d89530418 source. origin/main is its ancestor at
c3e3eed31c7783d1923e1afd9ce706f3501ece26. Official CloseDump all-state search
still returns zero results; the existing fork issue is1752. These are bounded
source/search observations, not proof that no differently named patch exists.
Fresh open PR547 (82b9729893406adca3911020a0a1213546446293) adds per-dumper
synchronous serialization plus unrelated TLS/header changes. The preserved
candidate already serializes across writers and dumpers;547 has no owned-file
Close. Its other overlap requires separate parent review and is not imported.

## Owner and lifetime

Client, Transport and Request expose `CloseDump() error`. Configuration owns an
admission scope and only internally opened file leases. Every accepted write
gets a permit before the dumper mutex; synchronous I/O or the actual async
worker releases it. Close rejects later admission, drains accepted work, and
releases this owner's file refs. Concurrent/repeated Close returns its stored
close result. No scope lock is held during file Close or output I/O.

Client/Transport clones explicitly retain the same open file identity but have
independent scopes, queues and close state. An arbitrary Dumper.Clone snapshot
borrows its configuration scope; it cannot acquire an owner ref without a
corresponding CloseDump entry point. Disabled clones also retain their configuration
lease. The last owner closes the file. Request attempt snapshots share their
configuration scope without taking a new file ref on every attempt or Do.
The private full-download recovery clone releases its scope after body close,
then attempts the existing H1/H2/H3 cleanup; the original primary error wins.

DisableDump/DisableDumpAll still drain without releasing the configuration's
file. Bare client/request Enable after Close cannot reopen the file. Explicit
ToFile, To or Options configures a new generation; options alone preserves its
prior enabled/disabled state. Manual bodies can dump after Do and until Close.
After Close/retarget their old callbacks become no-ops while underlying body
reads, EOF/errors, cancellation, deadline and unrelated context values remain
unchanged. A closed request context returns an untyped nil for the dump key.

Caller writers (including caller os.File/Closer), stdout/stderr, HTTP bodies and
custom transports are never adopted as internally owned resources.

## Retarget and error policy

A ToFile candidate opens without O_TRUNC and is statted. The old scope pauses
and drains before candidate truncation/activation. A real os.SameFile match
reuses the open file: another clone/owner prevents truncation; a sole paused
owner can restart its file. This covers path aliases without reopening the
pathname for every dump write. Per-field outputs that still reference another
internally opened file retain that lease too.

Open/stat/truncate/seek/candidate-close failures use the existing setter error
channel and try candidate cleanup while retaining the old configuration.
There is no promise to roll back bytes already changed by a partially successful
OS operation. Request preparation errors remain request preparation errors;
client ToFile failures remain logged errors. Retarget cleanup Close errors are
stored in the new scope's eventual CloseDump result, rather than injected into
HTTP/retry state. A scope attempts all owned file closes and joins actual errors;
its repeated Close returns that same result. Standard filesystems do not give a
portable deterministic real close-error trigger; no fabricated PASS is claimed
for that OS error branch.

Arbitrary concurrent mutation of dump options or public transport fields remains
outside the existing configuration contract. Close versus admitted writes and
concurrent Close of a configured owner are synchronized. The bounded queue20,
byte snapshots, output serialization registry, attempt generations and mandatory
internal Options interface are preserved. Public DumpOptions has no new fields.

## Regression and validation boundary

`dump_ownership_contract_test.go` with `dump_ownership_peer_test.go` exercises
real HTTP exchanges and actual files through public request/client/transport
entries. It covers sync/async file lifetime, a peer-held manual suffix after Do,
close before body read, cancellation and real short-body error, request reuse
and retry, both clone close orders, disabled/direct transport clones, never-sent
configuration, caller files/Closer writer, blocked accepted-write drain,
concurrent close, sequential file/writer/options retarget, real hardlink alias,
failed open retaining prior callbacks, multiple owned output fields, pathname
replacement where the OS permits it, and the actual private full-GET recovery.

The same tests compile with OLD through an optional CloseDump capability check.
Missing API is only diagnostic. The Windows os.Create control must actually
return a typed access/sharing denial while open and succeed after real Close;
the dump fixture must then independently observe its real Remove operation in
the same process, with complete peer/body/output prerequisites. Generic test,
compile/race/watchdog failures do not constitute that resource RED witness.
On Linux, unlink success is explicitly not descriptor-close proof. Windows may
reject renaming the open file; that path records its actual denial and does not
claim pathname replacement occurred.

The original `TestEnableDumpToFIle` and `TestEnableDumpAllToFile` names, real
requests, success and all content assertions remain. They add capability-based
real Close plus checked Remove; no cleanup assertion or failure is deleted.
All526 prior mandatory leaves remain required; new leaves and original selected
identities must be registered and verified by remote CI. The original132 archive
entries,89 production references and14 test files (12 selected+2 helpers) stay
unchanged. The candidate partition is114 unchanged+16 production adaptations+
2 original test adaptations. All16 production added-block85 gates, original
Codecov85 and the supplementary complete-Git Go85 gate remain mandatory; four
actual profiles stay separate. No denominator exclusion or infrastructure
exception is added.

No local/87 tests, compilation, build, formatter or project checker was executed.
Exact-head remote formatting, OLD/NEW Windows witness, named/all race suites,
complete fixtures, actual profiles, all85 gates and independent review are
pending. This source document is not approval or runtime evidence.
