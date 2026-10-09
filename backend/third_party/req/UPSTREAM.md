# Controlled req v3.57.0 security backport

This directory preserves the complete official `github.com/imroc/req/v3`
v3.57.0 module archive, including its tests and shared fixtures. The module path
and public API remain unchanged. The backend uses an explicit local replacement
so that the private HTTP/2 implementation is included in security repairs.

Original archive:
https://proxy.golang.org/github.com/imroc/req/v3/@v/v3.57.0.zip

Archive SHA256:
`7551b781c424d09cbd5823a0d5f9ff7dc5002aadd9ed226a776aa7150a2d186c`

The original archive contains 132 files and 1,109,806 uncompressed bytes.
`UPSTREAM-MANIFEST.json` records every original file's exact SHA256 and size.
All original files outside the declared three private HTTP/2 implementation
files must remain byte-identical to that archive. The original module metadata,
browser presets, TLS, HTTP/3, proxy, retry and transport option implementations
are retained.

## Official repair sources

- Initial-window CPU amplification: Go x/net commit
  [cc7d34fd](https://github.com/golang/net/commit/cc7d34fd2dd5864bcafe3e5e0a84919240be98c0),
  [CL 847186](https://go-review.googlesource.com/c/net/+/847186).
  The original private `flow.go` exactly matches the official patch's parent.
  The client transport integration is adapted to req's existing options and
  locking methods. Stream-window adjustments use a shared initial window and
  per-stream delta. Overflow retains the official lazy connection error and
  GOAWAY behavior, and stream errors retain the connection for other streams.
- Malformed response framing headers: Go x/net commit
  [28247830](https://github.com/golang/net/commit/28247830e9fb37184c6be718f483a0c7d0949022).
  Strip connection-specific fields and normalize identical, valid repeated
  Content-Length values before a response can reach an HTTP/1 downstream.
- Shared header/trailer parser budgets: Go x/net commit
  [57f4235f](https://github.com/golang/net/commit/57f4235f83712a299328015548240512213a3852).
  Account for declared trailer expansion separately from ordinary headers.
  Budget arithmetic must remain valid at the uint32 maximum without allocating
  a correspondingly large fixture. The published advisory's server exploit
  does not establish identical client exposure; this is the shared parser port.

Only `internal/http2/flow.go`, `internal/http2/transport.go`, and
`internal/http2/frame.go` contain production adaptations. This module has no
HTTP/2 server or server write scheduler; server-only fixes remain in the
backend's upgraded standard library and `golang.org/x/net` dependency.

## Licenses

Retain the original MIT `LICENSE` for req and all original copyright notices.
The Go-derived HTTP/2 files retain their Go Authors copyright notices and BSD
terms in `LICENSE-GO-BSD`, copied from the exact official repair revision.

## Validation and maintenance

Tests run remotely from `backend`, with `GOWORK=off`, under the backend's secured
module graph. Running `go test ./...` in the backend does not select the nested
module. Explicitly select `github.com/imroc/req/v3/internal/http2` and
`github.com/imroc/req/v3/...`; retain `go list` module replacement and package
directory evidence, actual named test events and raw coverage profiles.

The behavioral red witness copies `internal/http2/transport_security_test.go`
to an unchanged req v3.57.0 source tree under the same secured main module
graph and toolchain. `TestReqHTTP2Security_LazySettingsOverflowGOAWAY` must fail
on an observed DATA frame instead of GOAWAY FLOW_CONTROL_ERROR. Compilation
failures, timeouts, fixture packaging omissions and OOM are not this witness.
The arithmetic tests use new private flow types and are not part of that old
version witness.

The existing CI checks and Codecov 85% patch threshold remain required. Exact
upstream hashes identify unchanged imported source; the actual adapted source
lines need coverage and independent review. A blanket coverage exclusion or a
clean vulnerability database scan does not prove the private copy is fixed.
Stage the complete frozen repository and shared fixtures for remote validation.

When an official maintained req release contains equivalent repairs, compare
its API, browser and transport behavior, remove this local replacement in a
separately reviewed change, and retain the security regressions.

Tracking: [fork issue #1711](https://github.com/APIs-Gateway/sub2api/issues/1711),
[implementation PR #1712](https://github.com/APIs-Gateway/sub2api/pull/1712),
[permanent tracker #291](https://github.com/APIs-Gateway/sub2api/issues/291).
