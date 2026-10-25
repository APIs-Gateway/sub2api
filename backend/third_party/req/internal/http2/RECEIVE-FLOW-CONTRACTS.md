# HTTP/2 receive-flow wire contracts

## Preparation scope

These are RAW, unformatted, uncompiled and unexecuted test sources. They are one
small behavior batch toward the independently observed coverage gap, not proof
of a security fix or of either85 threshold. All executable validation is remote
CI only; no local or server87 formatter, parser, checker, test or build was run.

Base: `6cbaddae9795d6a46c752e20fffd68fac2b49aa2`, the explicit PR1712 dependency
head containing the copied req implementation and existing HTTP2 contract peer.
Fresh batch refs were origin/main `c3e3eed31c7783d1923e1afd9ce706f3501ece26` and
upstream/main `37067e8e340e9440d173a92b4faa05e8ce58368f`. Main does not supply
this copied private implementation; starting from main alone would test a
different dependency. The previous Windows trace branch remains preserved.

Only this new test and this document are source-owned additions. The manifest
appends their two paths and a provenance record. All original132 reference
entries,89 production-Go files,12 production adaptation records,14 original
test identities, existing468 mandatory leaves and original four coverage
profiles remain unchanged from this explicit base. Later normal composition
must union this batch with the newer1748/526/57owned source; it must not replace
that newer manifest with the older6cb manifest.

## Official primary source

Behavioral source:
[golang/net v0.48.0 http2/transport_test.go](https://github.com/golang/net/blob/v0.48.0/http2/transport_test.go).
Retrieved original bytes:164358; SHA256
`a7592ef60371241ef909b01f6c3887e5180210e5fac0ad177ef59a4e935978cb`.
The full original file and retrieval seals are retained in task evidence, not
copied wholesale into the module. v0.48.0 is the controlled req module's current
x/net dependency. Its newer synctest/internal harness is not imported into the
fork. Attribution and BSD terms are retained in the source header and existing
module-root `LICENSE-GO-BSD`.

| New actual test leaf | Official source | Observable contract |
|---|---|---|
| `TestReqHTTP2ReceiveFlowContract_UnreadRefund/single_frame` | `TestTransportReturnsUnusedFlowControlSingleWrite`, `testTransportReturnsUnusedFlowControl` | Read one real byte, close with4999 unread, observe CANCEL and the5000 DATA-octet connection refund; subsequent stream completes on the same actual connection. |
| `TestReqHTTP2ReceiveFlowContract_UnreadRefund/late_frame_after_cancel` | `TestTransportReturnsUnusedFlowControlMultipleWrites`, same helper | Read1 and close; send remaining4999 only after the peer sees CANCEL; actual late DATA still returns connection credit without poisoning a later request. |
| `TestReqHTTP2ReceiveFlowContract_PaddedData/read_to_eof` | `TestTransportReturnsDataPaddingFlowControl` | A real padded response yields precisely the entity bytes through public Body, never padding. No over-credit; a final small update may legitimately be deferred. |
| `TestReqHTTP2ReceiveFlowContract_PaddedData/read_one_then_close` | padding source plus unused-credit source | Read one actual entity byte then retire all buffered data. The peer sees CANCEL and receives credit for entity plus actual Pad Length/padding octets. |
| `TestReqHTTP2ReceiveFlowContract_PaddedData/close_unread` | same sources | Retire the padded body without a caller read. No entity or padding credit leaks; later actual request completes. |
| `TestReqHTTP2ReceiveFlowContract_DataBeforeHeaders` | `TestTransportResponseDataBeforeHeaders` | A valid first stream cannot initialize away per-stream validation. DATA before response HEADERS on the second stream produces its PROTOCOL_ERROR/RST_STREAM; a third actual stream completes. |

Protocol basis: [RFC9113 section6.1](https://www.rfc-editor.org/rfc/rfc9113.html#section-6.1)
defines DATA padding/flow-controlled length; sections5.1/5.2/6.9 define stream
states and flow-control credit, and section8 defines HTTP message sequencing.
The intended refund behavior is specifically the official transport regression
contract. The test does not assert an exact number/order of update frames.

## Independent wire oracle

The refund peer uses `net.Pipe` for a real byte stream passed to the copied
transport's public `NewClientConn`; all requests use its public `RoundTrip`.
This exercises copied production HTTP2 framing/body machinery but is not a TLS,
TCP-connect or deployed-gateway witness. x/net supplies only the peer's independent
frame and HPACK codec. No private window, stream map, body buffer or scheduler
field is inspected, assigned or recomputed.

The peer checks the actual client preface, exchanges actual SETTINGS/ACK and
records connection WINDOW_UPDATE increments from decoded client bytes. A baseline
snapshot excludes initial connection-window credit. Assertions sum later wire
increments rather than counting update frames or constraining coalescing.
Server PING/ACK is a causal read-loop barrier after preceding actual DATA. Body
read/Close returns before the final barrier; it is not a sleep-based assertion
that a goroutine probably ran.

Padding uses16 genuine DATA frames,313 entity bytes and255 padding bytes per
frame. That includes4096 wire octets of padding plus Pad Length fields, separate
from5008 entity bytes. The full-read positive control checks exact entity bytes
and rejects over-credit but permits a deferred final small batch. Unread-close
cases follow the upstream retirement/refund contract and check all9104 received
DATA octets are accounted for after closure and the causal barrier. No private
refresh threshold is read or duplicated by a test implementation.

Each refund/padding case performs another actual200/entity exchange on stream3
using the same public ClientConn and connection. The malformed-response case
reuses the already reviewed `reqH2ContractFixture`, with completed streams1/5
surrounding the rejected stream3. Expected stream numbers follow HTTP2 request
identities; no internal pool scheduling/callback counts are asserted.

The peer's existing-style20-second connection deadline,12-second request context,
5-second observation guard and3-second cleanup guard are fixture bounds, not
production timeout changes. A guard, incomplete target response, peer I/O or
setup failure remains a failed prerequisite; it is never an expected business
RED. Cleanup closes only test-owned response bodies/connections and joins actual
RoundTrip/peer goroutines. Caller-provided production writers are not involved.

## Existing test overlap and future execution

Existing `flow_security_test.go` and `transport_security_test.go` cover outbound
SETTINGS arithmetic/overflow, GOAWAY and stream-window error isolation; they do
not test these inbound DATA/body-credit refunds. Existing transport contract
tests cover retry, trailers, timeout, cancel-other-stream, shutdown and ping.
Their peer is reused only for the new DATA-before-HEADERS leaf and stays byte
unchanged. Original source/fixture files are not rewritten.

Fresh all-state issue/PR searches and all41 open fork PR filename inventories
are retained in durable task evidence. PR1712 and nine dependent implementation
PRs share the copied private HTTP2 tree/manifest; none lists this new test or
document. This is supplementing the existing security implementation, not
opening a duplicate production-bug issue or claiming an upstream bug fix absent
actual RED evidence. Future merge preparation must recheck all current heads
and preserve the dependent security whole-source changes.

New prefix `TestReqHTTP2ReceiveFlowContract_` is separately enumerated here. The
current base468 mandatory named inventory is deliberately unchanged; it does
not automatically include these six leaves. The normal full package suite
discovers them. Before accepting this batch, root must explicitly bind the exact
six new names and actual raw RUN/PASS/FAIL in its remote plan, normally compose
any additional mandatory inventory/controller registration and keep all existing
526/newer leaves and gates. No unreviewed CI/workflow or counter merge is hidden
in this source preparation. Named/full/race/format/build results are all pending.
