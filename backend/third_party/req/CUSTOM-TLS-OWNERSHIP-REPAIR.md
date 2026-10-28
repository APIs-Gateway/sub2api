# Custom TLS handshake result ownership (#1712)

This repair is limited to the existing custom callback branches in root
`Transport.customTlsHandshake` and private HTTP/2 `Transport.dialTLSWithContext`.
The Firefox production client reaches these through `TLSHandshakeContext` and
its configured handshake timeout. The old timeout owner read/closed a connection
variable while a late successful worker replaced that same variable. The original
private/default TLS path, TLS/header implementations and caller pool policy are
preserved. No other #547 changes are imported.

## Result and trace boundary

Each callback uses immutable raw input and local result variables. A local mutex
serializes terminal selection. A completed result can claim the terminal only
while the child context is still live. Once selected it remains selected even if
the owner is not scheduled until after the former deadline. The worker stops the
child timer after selection. If timeout or supplied-context cancellation wins,
the owner closes raw and the worker closes a late returned connection, including
a distinct real connection. All Close operations happen outside the mutex;
secondary Close errors never replace the primary outcome.

The child own deadline preserves the existing typed TLS timeout. Inherited
cancellation or deadline preserves `ctx.Err()` identity, including when the caller
has its own custom context cause. Zero timeout still permits a real success.
Invalid nominal successes (nil/typed-nil pointer connection, nil state, or private
H2 non-TLS connection) return an explicit error and close the available resources.
No arbitrary extra goroutine attempts to stop a user callback that never returns.

Start stays in the worker, preserving timeout return while a user Start hook is
blocked. Done is claimed once after Start returns. If timeout wins before Start
finishes, the owner returns without waiting; the worker subsequently emits the
selected timeout Done after Start returns, then retires the canceled callback.
A Start that never returns therefore also prevents its Done hook, while the
owner still returns and closes raw. User hooks and connection Close never run
under the terminal mutex. A slow Done runs after the decision and timer stop,
so it cannot convert an already accepted success into timeout.

## Source regression identities (not executed evidence)

The five new fixture files declare 31 leaves:

- Parent package: public real Firefox `PublicSuccessAndReuse`,
  `LatePublicationRace`, `LateDistinctConnection`, each HTTP1/HTTP2 (6 leaves).
- Parent package: `TestReqCustomTLS_Boundaries` (12 leaves), directly calling the
  real H1 function with a real TCP/TLS peer.
- Private H2: `TestReqCustomTLS_Boundaries` (13 leaves), calling the actual custom
  `dialTLSWithContext` path, with the additional non-TLS return-shape control.

Both boundary matrices cover no timeout, callback error, caller cancellation and
custom-cause deadline, own typed timeout, late error disposal, slow Start,
already expired deadline, slow Done success, nil connection, typed-nil pointer,
and nil state. Direct H2 boundary peers negotiate HTTP/1.1 to exchange and check an
actual body through the returned TLS connection; they test this function's
callback contract. The public Firefox ForceHTTP2 cases separately establish the
real HTTP/2 route, ALPN, entity and reuse.

The OLD/NEW race witness is the identical public
`TestReqCustomTLS_LatePublicationRace/{HTTP1,HTTP2}` source on old and candidate
production. A real Firefox TLS handshake precedes an independent timed delay.
Neither request return nor Close releases that delay. OLD is accepted as red
only with actual race frames on the old live-connection write/read and the real
handshake prerequisite. A compiler failure, setup timeout, missing resource ack,
watchdog or generic failed assertion is not proof of that race. NEW must show
actual named RUN/PASS, no race, late wrapper Close ack, real peer closure and no
HTTP request publication. The separate distinct-socket case is an ownership
stress, not a claim that normal Firefox callbacks create a second connection.

Slow Start requires the owner to return typed timeout before hook release and
Done to remain absent while Start is blocked, then exactly Start/Done in order.
The already-expired deadline also checks ordering when cancellation predates
worker launch. Slow Done holds a genuine selected result past its former
deadline and then exchanges the actual request/response body. These assertions
do not pretend to force a particular Go scheduler interleaving of an unscheduled
owner; the mutex-protected immutable terminal also requires static review.

## Preserved validation boundaries

Original archive 132 entries and 114 unchanged + 16 production adaptations + 2
original test adaptations are preserved. Only two already adapted production
hashes change. Prior 574 mandatory leaves remain; these 31 would make 605 after
root registers them. All four complete atomic profiles, full-repository/MVS input,
per-file and aggregate 85 gates, original test identities and independent review
remain required. No local/87 formatter, compiler, test, syntax check, build or
project checker has been executed. All source declarations are NOTRUN; only
root-coordinated remote CI can produce execution or formatting evidence.
