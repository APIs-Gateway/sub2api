# Complete Git coverage evidence

This supplementary remote CI gate preserves the original required Codecov patch
target of 85%, its existing ignore list, and its four-upload notification policy.
Both push and pull request workflows must finish their backend/frontend uploads.
This gate does not replace Codecov or prove frontend coverage.

The PR test job freezes the exact event head, tree, base and computed Git merge
base before tests. It records every tracked input's blob identity and SHA256,
the actual Go build file classification, Go version, environment and run identity.
The final check runs after the original Codecov upload. A freeze failure is
recorded and deferred to that final mandatory check, so it cannot suppress uploads.

The denominator comes from the complete Git diff, with per-file patch additions
checked against the full `--numstat` and path inventories. It includes every changed
backend production Go file except the existing Codecov ignores. All other changed
paths remain in the report with an explicit disposition. No GitHub compare API
data determines the denominator.

Pinned Go 1.26.9 `go tool cover` independently instruments each source file. A
small standard-library helper reads its block metadata and scans physical source
token spans. Before counting coverage, the gate compares that inventory against
**every represented file** in the four actual profiles, including unchanged files.
Any legacy versus modern instrumentation mismatch fails rather than assuming
equivalence. Instrumenter failure is never interpreted as a file with zero blocks.

The four mandatory input profiles are `coverage-unit.out`,
`coverage-integration.out`, `coverage-setup-cli.out` and `coverage-req-http2.out`.
They must be fresh outputs from the current frozen checkout. The gate archives
their exact bytes and SHA256 digests, merges positive hits in memory, and verifies
the original files stayed unchanged. The existing Makefile already normalizes
unit/integration duplicate records; these are called the actual uploaded profiles,
not untouched raw test profiles. The req security artifact separately retains its
original `named.raw.out` and `all.raw.out`.

All four profiles are mandatory, but they cover different test scopes. Expected
source block completeness is checked on their **union**, not separately on each
profile. If a block is absent from one profile but another retains it, that is
valid. The missing-block control deletes exactly one block from all four profiles
of a real compiled multi-block file while keeping that file represented; the
independent instrumenter baseline must then detect the incomplete block universe.

The **single additional threshold** is a conservative lower bound of at least 85%
for complete Git changed backend Go instrumented token lines:

* A changed head line enters the denominator when one of its non-comment Go
  token spans overlaps a block emitted by the independent Go instrumenter.
* A line enters the numerator only if **every** such overlapping block has a
  positive count in at least one of the four profiles. A partial line contributes
  zero to the numerator. Zero-statement instrumented blocks are retained.
* Source files not selected by the actual platform/package build inventory stay
  in scope. Their unprofiled blocks count as misses. This is different from a
  selected/tested file whose expected profile records are missing, which fails
  integrity. No wasm, GODEBUG or otherwise unreachable behavior is activated.
* Comments, whitespace, declarations and global initializers outside Go cover
  blocks remain listed as not instrumented. A file with zero blocks has an
  explicit tool-derived explanation. With no changed instrumented Go token lines,
  the supplementary threshold is not applicable; original Codecov policy still
  applies.

The check uses integer arithmetic without rounding before comparing to 85%.
Block and statement-weight counts are diagnostics only. This metric is explicitly
**not Codecov line coverage** and a passing result cannot establish a terminal
green PR without the original Codecov status and all actual test results.

The always-uploaded artifact includes source bindings, full diff/path/numstat
inventories, original input bytes, all-file instrumenter/profile comparisons,
every changed Go block/line state, and concrete missing-input or integrity errors.
No completion marker is written on integrity or threshold failure.

`test_check.py` runs only in remote GitHub CI. It uses a real Go test profile to
check independent instrumenter coordinates, a Git change with over 100 paths,
and corruption controls for missing profiles, files, blocks, wrong coordinates,
changed source, wrong head/base, unknown paths and malformed input. Real platform
classification and scanner controls verify that uncompiled source counts as a miss
and multiline raw strings remain in the physical line scope. It records exact
expected/passed test identities and the workflow records its actual exit.

References: [Codecov notifications](https://docs.codecov.com/docs/notifications),
[Codecov percentages](https://docs.codecov.com/docs/coverage-percentages), and
[the pinned Go instrumenter](https://github.com/golang/go/blob/go1.26.9/src/cmd/cover/cover.go).

Do not execute these tools locally or on server 87. All executable validation,
including formatting and the verifier's own tests, must run in remote CI.
