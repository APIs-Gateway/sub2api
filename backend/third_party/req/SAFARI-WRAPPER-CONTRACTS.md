# Global Safari wrapper repair in existing PR #1712

User approval on 2026-10-29 permits reproduction and repair of this specific
global wrapper within existing PR #1712. Permanent tracking: #291. No new issue
or PR is opened. Independent review and remote CI remain required.

## Defect and minimal repair

The global `ImpersonateSafari()` delegated to `defaultClient.ImpersonateFirefox()`.
The existing instance method configures Safari 16.6 HTTP identity, Safari 16.0
uTLS handshake, Safari HTTP/2 settings and WebKit multipart boundary generator.
The repair calls that existing instance method; no preset or transport policy
is changed. The wrapper's adjacent documentation now names Safari accurately.

## Real request regression and controls

`safari_wrapper_contract_test.go` adds three sequential leaves:

- `TestReqSafariWrapperContract/global_safari`
- `TestReqSafariWrapperContract/instance_safari`
- `TestReqSafariWrapperContract/global_firefox_control`

Every leaf creates its own certificate-verified TLS server, isolated client and
real multipart POST with one field and one file. No mock RoundTripper or fake
handshake replaces the production path. The peer captures the actual received
ClientHello and actual HTTP request, and returns a checked response. The peer
offers HTTP/1.1; the fixture does not force a client HTTP version or claim to
verify HTTP/2 settings/frame ordering. The full ClientHello cipher list is
retained in test output; assertions distinguish the first three non-GREASE TLS
1.3 ciphers and GREASE presence, plus advertised ALPN. They do not claim a full
browser fingerprint or verify random bytes.

Expected HTTP identity and multipart prefixes are literal observations specified
by the original `client_impersonate.go` Safari and Firefox presets. TLS oracles
come from the pinned main application's uTLS v1.8.2 presets `HelloSafari_16_0`
and `HelloFirefox_120`, independently observed via Go's standard TLS server:
<https://github.com/refraction-networking/utls/blob/v1.8.2/u_parrots.go>.
Safari uses GREASE and cipher prefix `1301 1302 1303`; Firefox has no GREASE and
prefix `1301 1303 1302`. Both advertise `h2`, `http/1.1`.

The saved test-only predecessor contains identical fixture bytes but the original
production wrapper. Remote CI must show its global Safari leaf fails with
actual wrong HTTP identity, ClientHello and boundary; the instance Safari and
Firefox controls must pass. The repaired source must show all three actual
RUN/PASS and no race. A green job label alone is insufficient. Formatting and
all executable verification run exclusively in remote CI, using backend main
MVS and the complete frozen repository. These are new tests, not executed yet.

All handlers join on peer close; the complete handshake/exchange ledger must
contain exactly one of each, and every read/write error is checked. Caller
global state is restored, owned client connections/dump and the response body
are closed. Tests cannot run in parallel because the global default is shared.
Original 132-file provenance, original 605 tests, four original coverage profiles
and both original 85% gates remain required and unchanged by this component.
