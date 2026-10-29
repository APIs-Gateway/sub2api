# Private HTTP/2 coverage contracts for existing PR #1712

These tests extend the existing private-pool/net.Pipe/independent x/net HPACK
peer contracts. The implementation base is
`29ea1fcb4c2366aee00d8f2a75c0b0edf3676880`. Only three new Go test files and
this document are added. Production semantics, the original 132-case history,
all 605 registered leaves, original four profiles, both original 85% gates and
the 16-file added-block gates are retained. The root agent owns registration.

## Execution status

**RAW SOURCE; NOT FORMATTED OR EXECUTED.** No local or server 87 project code,
formatter, syntax/AST checker, test, build or compiler was run. Independent
source review and exact-head remote CI remain required. The source declares
51 leaf contracts (39 transport, 12 frame); runtime results and coverage gains
are unknown. It does not claim the original coverage gates pass.

## Oracles

- Admission starts with a successful pooled exchange. Malformed requests must
  fail with the specific category without reading their bodies; caller body
  closure is acknowledged. The next unfiltered HEADERS must decode the valid
  same-connection control, detecting leaked malformed headers or HPACK damage.
- Header wire tests inspect independent decoded pseudo/regular ordering,
  cookie splitting and CONNECT authority-only semantics, then reuse the real
  connection. Fork order keys must never appear on the wire.
- Expect/continue uses actual HEADERS and Wait100Continue prerequisites.
  Real 100, cancellation, request deadline, final 417 or the actual continue
  timer provides the release/terminal event. No fixed sleep proves absence.
  Final-rejection body closure/read count is checked before explicit cancellation
  joins the still-waiting upload path. No-body-upload assertions inspect the
  actual frames after a reset and a same-connection control barrier.
- Informational responses check actual first-byte and 1xx trace ordering,
  headers, precise stream code/ID and trace error Cause identity. Malformed
  status and END_STREAM on 1xx must reset only that stream; a valid control
  succeeds on the same HPACK connection. The copied five-1xx limit is untouched.
- Entity tests use real content-length/HEAD responses. A live response body is
  retained for repeated terminal-read checks. HEAD remains live through illegal
  DATA/reset instead of being canceled by an early response Body.Close.
- Decompression checks exact entity bytes, requested wire coding and metadata.
  The fork's opt-in `deflate` decoder uses raw RFC 1951 DEFLATE, so the fixture
  uses `compress/flate`, not a zlib wrapper. Implicit gzip and disabled passthrough
  preserve the fork's existing policy.
- Strict concurrency negotiates MAX_CONCURRENT_STREAMS=1 and observes its ACK.
  A real active stream plus State.StreamsPending proves queuing; completion or
  cancellation must return pending/reserved/active state to the expected values
  and permit a same-connection exchange without an extra dial.
- Connection read metrics use the actual ClientConn obtained from the real
  pool, avoiding an automatic Transport retry adding unrelated metrics. EOF,
  partial-frame EOF and an illegal SETTINGS stream assert exact CountError
  sequences, caller failure, reader joins and closed connection state.
- Request-body failures assert sentinel identity, exact accepted wire prefix or
  length bound, reset code, body closure and valid reuse. No fake clientStream
  state or direct call into a production internal helper is used.
- Dump contracts use an asynchronous per-transport dumper and synchronized,
  caller-owned writer. Stop drains admitted writes. HEADERS/CONTINUATION fields
  appear once in wire order. Invalid field values produce a protocol StreamError
  whose error omits the sensitive value; explicitly enabled raw diagnostic dump
  preserves its exact original bytes. The dumper must never close the writer.
- Frame diagnostics use only per-Framer log flags/logger callbacks. Independent
  wire decoding and exact byte/header/summary assertions include settings,
  DATA preview truncation, connection/stream windows, ping, GOAWAY, reset and
  unknown types/flags. The actual full DATA remains on wire. Decoder failure,
  uint24 read-size clamping with a real 32-KiB extension payload, and preservation
  of a partial writer's primary error are separate observable interactions.

## Reviewed plan corrections

The immutable earlier design specified 52 leaves. On source inspection, the
root agent approved these explicit corrections; neither is a runtime waiver:

1. Remove `ReadErrorMetrics/oversized_frame`. This Transport constructs its
   Framer with the uint24 maximum read size and never lowers it. The wire length
   field itself is uint24, so that planned transport error is unreachable under
   the actual default. Do not lower private state or fabricate a CountError to
   obtain coverage. The genuine standalone Framer API clamp contract remains.
2. Rename `HeaderDump/invalid_value_not_leaked` to
   `HeaderDump/invalid_value_error_redacted_raw_dump_retained`. The earlier plan
   incorrectly inferred raw-dump redaction from the upstream returned-error
   redaction comment. Official req diagnostic documentation and existing dump
   tests describe explicit raw diagnostics; no raw-dump redaction contract was
   found. Preserve strict rejection, error redaction, exact raw bytes and caller
   ownership without changing production behavior.

The independent static dump-order receipt is SHA256
`30c04dad91a6b6e828f782375c84334cea05117ced243382e2eb0e9403a48cac`;
the official-contract followup is SHA256
`765700f1f793f81f2227d2ffccd3348ca2a87aa53e5ba7496a873e51cde43702`.
Both are saved externally under
`custom-tls-race-1712-oct28/independent-h2-dump-order-candidate/`.
The old spec is preserved externally rather than rewritten.

## Primary sources and license

The behavioral subsets are adapted to this older req copy and its existing
wire helper, rather than importing modern production or synctest code:

- [golang/net transport tests at 18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5](https://github.com/golang/net/blob/18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5/http2/transport_test.go),
  SHA256 `f7d6f62c8aa2bc03fe605ac5e814021d988897c04083e3243b33ba774ade95d9`.
  Relevant upstream contracts include TestTransportChecksRequestHeaderListSize,
  TestTransportCookieHeaderSplit, TestTransportRejectsContentLengthWithSign,
  TestTransportResponseContentLength, TestTransportFailsOnInvalidHeadersAndTrailers,
  TestTransportReturnsErrorOnBadResponseHeaders, TestTransportExpectContinue and
  TestTransportBodyLargerThanSpecifiedContentLength_len3/len2.
- [golang/net frame tests at the same commit](https://github.com/golang/net/blob/18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5/http2/frame_test.go),
  SHA256 `f4d911738bcdc395e9e6cb0a252940776ee0f9c17d8f6e054bb8e4ad00ed2186`.
  Independent read/write frame/header contracts provide the wire basis; exact
  logger interactions are additional assertions for this retained private copy.
- [golang/net frame implementation at that commit](https://github.com/golang/net/blob/18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5/http2/frame.go),
  SHA256 `4466f8ba7f851901df67721935d75f526f9f91184bd93c899307e577b97d9bd7`.
  Its invalid-field comment promises returned-error redaction. It has no req
  dump wrapper; it cannot establish a raw-dump redaction contract.
- Historical [v0.48.0 transport tests](https://github.com/golang/net/blob/35e1306bddd863f360fb94480c5fed84229953f0/http2/transport_test.go),
  SHA256 `a7592ef60371241ef909b01f6c3887e5180210e5fac0ad177ef59a4e935978cb`,
  were read alongside existing receive-flow provenance. The old six receive-flow
  leaves are not duplicated. Modern 1xx-limit, TLS, defaults and encoding policy
  are not imported.
- The fork-specific custom header ordering, opt-in raw DEFLATE and diagnostic
  ownership assertions follow the frozen req sources, not modern x/net policy.
  [Official req raw diagnostics](https://req.cool/docs/tutorial/debugging/) explain
  the opt-in dump use case; source/license snapshots and metadata remain external.

Adapted Go files retain the Go Authors copyright/BSD notice. Existing
[`../../LICENSE-GO-BSD`](../../LICENSE-GO-BSD) at the req module root is
the governing BSD license; SHA256
`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`.
No new upstream production dependency or platform-specific coverage profile is
introduced, and no currently unused production function is called for coverage.
