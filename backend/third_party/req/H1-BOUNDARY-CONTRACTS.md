# Existing PR1712 H1 error and entity boundaries

Source base: `29ea1fcb4c2366aee00d8f2a75c0b0edf3676880`.
This lane adds tests for the current coverage blocker; it changes no production,
upstream source record, existing test, profile, gate or denominator.
Formatting, compilation and execution have not run locally or on server87.
All executable validation and the eventual exact-head results must come from CI.

## Distinct contracts

- `TestReqH1Boundary_PreflightOwnership`: twelve rejected-request cases verify
  primary error identity, redaction of sensitive invalid values, zero entity
  reads, exactly one body close and the exact allowed dial count. URL, method,
  header, trailer, context, proxy and custom-TLS failures are separate causes.
  Every dial is a bounded callback that returns an explicit failure; no DNS or
  external server is contacted.
- `TestReqH1Boundary_HeaderParsing`: eight literal MIME records verify folded
  and repeated values, horizontal whitespace normalization, malformed names and
  values, the first-line continuation rule, header count and memory limits.
  Successful parses must leave the next-message sentinel untouched. This calls
  req's actual private parser, rather than using the standard library parser as
  the implementation under test.
- `TestReqH1Boundary_HeadlessStatusFraming`: six actual private response-parser
  cases distinguish metadata from entity bytes for HEAD, 204, 304, 103 and zero
  length. The following-message sentinel must remain unread.
- `TestReqH1Boundary_ResponseBodySignals`: three ownership cases assert that a
  sticky source error does no more I/O, cleanup happens once, the completion
  callback runs once, and early Close is distinct from completed EOF. These
  extend the existing real-socket lifecycle tests with explicit failing-reader
  and failing-closer oracles.
- `TestReqH1Boundary_GzipReader`: four independent standard-gzip encoded records
  verify exact payload, invalid/truncated header causes, checksum failure,
  sticky initialization errors and closed-body rejection with one owned close.

The intended inventory is five top-level tests with 33 named leaves. Every
leaf must actually RUN/PASS in CI; source declarations are not execution proof.
Existing transfer framing and writer tests are retained and not duplicated.
The public API, private H2 and bisect lanes own separate files.

## Evidence scope

The current formal PR has an independently verified 62.0829315333% complete-Git
lower bound, with 5,169 miss and 729 partial changed lines. The original four
profiles and both original85 gates are retained. These tests do not promise a
projected hit count or establish gate passage before actual execution.

All test bodies and counters are owned by their case. There is no parallel
mutation of shared clients or environment. Transport instances close their own
idle connections. Errors from real body closure are asserted, and expected
protocol failures are separated from fixture failures.
