# Retained bisect library contracts for existing PR #1712

## Current authorized cache-race repair (2026-10-30)

The original marker-only concurrent contract actually failed under remote
`-race` on formal source `c82356c8115678ff669e088a3d44dc955482b3c4`,
run40853052089/job123377058265. It retained the real ordinary eviction-cache
read at original bisect.go789 concurrent with atomic.StoreUint64 through
Matcher.stack; the test, parent and package failed. The immutable original
artifact12888267228 SHA256
`c4e9241c2c47274b678a95df545d4a66ead9ef5c5f1aac8e0c8df3664cb88dbf`
and its failure remain evidence; green synthetic controls do not waive it.

The user explicitly authorized this concrete repair within existing #1712.
The only production change reads each eviction-hash slot by index with
`atomic.LoadUint64`, matching the existing atomic slot loads and stores. The
lossy cache still permits duplicates and selects an eviction slot from its
approximate observed contents. It does not add exact-once guarantees, change
matching/output policy or establish ordinary HTTP activation. Original source
and license records below remain immutable historical provenance.

This original file becomes the eighteenth production adaptation, following all
original seventeen in order (Safari wrapper remains index16; bisect is index17).
The current partition is112 unchanged+18 production+2 original test adaptations
=132. The original per-file85 algorithm applies to every adaptation, including
bisect; all880 names, the112 bisect cases, race checks, both original global85
gates and four profiles remain. Remote formatting now also checks bisect.go.

The repair and updated verifier controls are RAW source, runtime **NOTRUN**.
They require independent exact-head review and actual remote named/full race
and coverage results. No local or server87 executable validation was performed.

## Historical original test-source specification

These tests address a concrete untested component in the existing complete-Git
coverage result. They do not alter the production implementation, original
archive entries, original 605 test inventory, source denominator, coverage
thresholds, profiles, or the fork's behavior. Root must compose and independently
review their registration separately. No new issue or PR is created.

## Source and license

The original req v3.57.0 archive internal/bisect/bisect.go is 23,969 bytes, SHA256
de2e6582ffb06e9745167ae4ed820371b88c7b3af5a793d289a9997ebaedf916.
It is one of the original 132 archive files.

The behavioral reference is the official Go source at
[go1.23.0, commit 6885bad7dd86880be6929c02085e5c7a67ff2887](https://github.com/golang/go/blob/6885bad7dd86880be6929c02085e5c7a67ff2887/src/internal/bisect/bisect.go).
That file is 23,489 bytes, SHA256
978d518986ba6867f5f96356531f35bfa59b865fbb363b98eb9052e862a2160c.
The package documentation specifies suffix sets, addition before subtraction,
negated enable selection, quiet/verbose reporting, marker text, and accepted
Hash types. These are the basis of the portable tests.

The inspected official Go directory at that tag contains no test file.
The official tools tests at immutable commit
84bd18765c7adef571477370ea5431b6556bb4fc are:

| Official source | Bytes | SHA256 |
| --- | ---: | --- |
| [internal/bisect/bisect_test.go](https://github.com/golang/tools/blob/84bd18765c7adef571477370ea5431b6556bb4fc/internal/bisect/bisect_test.go) | 833 | ec5831471bbfcbc0e7e877fc39b281b32b04133389fa676253df402bb505083d |
| [cmd/bisect/main_test.go](https://github.com/golang/tools/blob/84bd18765c7adef571477370ea5431b6556bb4fc/cmd/bisect/main_test.go) | 4263 | 43554146a4754ea3d2ff635fea393fc2f8cbbaebc9056c46bb08b54c587609b2 |

The internal test checks that the tools implementation imports nothing. That
test cannot be copied into this original runtime-aware req copy, which already
imports runtime, sync, atomic, and unsafe. The command tests exercise the whole
CLI and depend on its Bisect harness, txtar, diffp, and a newer ShouldReport
API. They are not transplanted into this package. No internal/testenv,
toolchain-private test helper, CLI implementation, or new module is imported.

The source follows the Go Authors BSD three-clause license. The official tools
LICENSE is 1,453 bytes, SHA256
911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad,
and equals the existing top-level LICENSE-GO-BSD byte for byte. The Go1.23.0
LICENSE is 1,479 bytes, SHA256
2d36597f7117c38b006835ae7f537487207d8ec407aa9d9980794b2030cbc067.
Both notices and the source reference are recorded in the provenance JSON.

### Original copy differences

Compared with the pinned official Go implementation, this req original keeps
the bootstrap unsafe.Pointer/atomic Load/CompareAndSwap wrapper instead of
atomic.Pointer[dedup], and prints runtime.Frame.Func.Name() instead of
runtime.Frame.Function. Tests call the existing public API and real runtime
stack; they do not replace either implementation or depend on its private fields.
The tools command's newer ShouldReport name is not used; this copy exposes
ShouldPrint and MarkerOnly.

## Behavioral specification and independent oracles

| Group | Source-declared leaves | Observable contract |
| --- | ---: | --- |
| Matcher | 26 | Explicit documented truth sets for 0–255 plus full-width/high-bit IDs; enable and print are checked separately, including inversion, quiet and repeated verbose prefixes |
| InvalidPattern | 18 | Malformed terms, forbidden operator order, unsupported characters and width overflow return nil matcher and the original diagnostic text |
| HashEncoding | 22 | Declared typed values serialize into independently fixed bytes and agree with standard hash/fnv FNV-1a, including signed values, 32/64-bit widths, slices and mixed arguments |
| HashUnsupported | 5 | Unsupported argument types panic with the original diagnostic, instead of silently supplying an ID |
| MarkerFormat | 4 | Zero, one, mixed and maximum uint64 IDs retain fixed-width hexadecimal format, append prefixes and one output newline |
| MarkerWriterError | 1 | A real caller-owned writer failure is returned, with the exact attempted report recorded |
| CutMarker | 20 | Literal binary/hex markers, width and binary errors, first-marker selection, Unicode and at-most-one adjacent-space removal |
| FileLine | 8 | Real writer output, standard-FNV file/line identity, zero/negative line decimal text, quiet/excluded/disabled selection and nil-matcher fast path |
| StackReports | 8 | Real call stacks, visible function diagnostics, same-site suppression, precise concurrent suppression and approximate marker-only concurrency with a safe writer |

The 112 names are listed in BISECT-CONTRACTS-PROVENANCE.json. They are source
declarations, not actual test runs or an added PASS count.

The matcher oracle uses arithmetic truth predicates derived from the documented
examples. It does not implement another pattern scanner, masks, or condition
list. Hash tests declare bytes per case and use the standard library FNV
implementation, rather than reproducing the production hash loop. Marker and
error expectations are literal data or standard fmt output. FileLine hashes
the independently encoded file and signed line bytes using that standard FNV
oracle. Stack tests use actual runtime.Callers through the existing API, without
mocking PCs, mutating the matcher, or forcing unreachable branches.

The marker-only concurrent case checks API results and valid report text. It
does not require exact-once output or a global count of distinct stacks: the
approximate cache explicitly permits duplicates. The verbose precise case uses
the same real call context and does require suppression. The writer itself is
protected by a mutex so that a test-writer race is not confused with a library
race.

These tests retain the existing accepted API and diagnostics. They do not add a
new malformed-hex policy to CutMarker, force a runtime stack shorter than its
real caller, claim ASLR/deployment proof, or assert a particular total statement
coverage percentage.

## Production calling boundary and race concern

The direct importer is private internal/godebug. The copied transfer parser
references godebug.Value for the lax Content-Length setting. That method has
an optional bisect.Stack branch. However, this inspected copy has no caller
for its private update/parse functions and no runtime environment-update
bridge. Ordinary HTTP production activation of that matcher is therefore not
established. These tests exercise a retained private library, not a claimed
HTTP production reproduction or a new setting feature.

The lossy deduplicator reads a cache range without atomic loads while other
calls can store atomically. The concurrent marker-only contract preserves this
concrete static concern for actual remote -race execution. A reproduced race
remains a blocking failure even if inherited from the original Go copy. This
test-only change neither repairs production nor waives a failure.

## Execution status

RAW source only, **NOTRUN**. No Go, formatter, syntax/AST validator, project
checker, test, compiler, or build has run locally or on server 87. Root must obtain
non-author exact-head review, actual remote formatting, actual named and full
race results, and the unchanged original coverage/profile gates before claiming
PASS or approval. No local test cache or generated project file is created.

The base is reviewed source
29ea1fcb4c2366aee00d8f2a75c0b0edf3676880. All original archive and production
bytes, original 605 registry, both original 85 gates and all four original
profiles remain unchanged by this standalone test source.
