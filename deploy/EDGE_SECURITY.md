# Edge and HTTP ingress contract

Sub2API supports long-lived SSE and WebSocket requests. Apply request controls
at the edge without applying a response write deadline: a write timeout can
terminate a healthy generation or stream.

This document describes the checked-in `deploy/Caddyfile` as a **direct-to-Caddy
baseline**. It is not a drop-in CDN configuration.

## Limits and streaming

The current Caddy baseline enforces a 100 MB (100,000,000 bytes) request-body limit
through `max_size 100MB`. The application
also defaults `server.max_request_body_size` and `gateway.max_body_size` to 256
MiB, so the smaller Caddy limit wins for this deployment. Raise or lower both
layers deliberately when adding a workload that needs a different limit.

The application defaults `server.read_header_timeout` to 30 seconds and
`server.idle_timeout` to 120 seconds. Set stricter edge header and connection
limits in the reverse proxy or load balancer according to measured legitimate
traffic. Do not add a global request semaphore or a response write timeout;
authenticated concurrency is enforced by the application while an SSE request
can legitimately remain open for minutes.

The Caddyfile keeps `flush_interval` unset, and its explicit compression matcher
does not include `text/event-stream`. Preserve both properties: buffering or
compressing an SSE stream can delay tokens until the response completes.

## Client IP and trusted proxies

The bundled Caddyfile overwrites `X-Real-IP` and `X-Forwarded-For` from
`{remote_host}`. That is correct only when clients connect directly to Caddy.
For security-sensitive client-IP consumers, the default
`security.trust_forwarded_ip_for_api_key_acl: false` uses Gin's trusted-proxy
parsing. In that mode, forwarded addresses are accepted only through the
`server.trusted_proxies` chain. Configure it with the addresses or CIDRs that
connect directly to the application; leave it empty when the application is
directly public.

The explicit compatibility mode
`security.trust_forwarded_ip_for_api_key_acl: true` bypasses that chain for these
consumers. The request's forwarded-IP policy snapshot first considers
`security.forwarded_client_ip_headers`, then the built-in `CF-Connecting-IP`,
`X-Real-IP`, and `X-Forwarded-For` priority. Those header values can therefore
override the socket peer without `server.trusted_proxies` authorizing it.
`GetClientIP` callers without a policy snapshot also retain legacy header parsing;
do not treat `server.trusted_proxies` as a universal forwarded-header filter.

Keep the default mode unless the trusted edge reliably overwrites or removes
every header consumed by the selected policy, including custom header names.
The bundled Caddyfile passes `CF-Connecting-IP` through unchanged, so its
`X-Real-IP`/`X-Forwarded-For` overwrites alone do not make compatibility mode safe
against a client-supplied `CF-Connecting-IP`. An origin firewall and trusted edge
header handling are required; the compatibility flag does not establish trust.

Do not trust `CF-Connecting-IP`, `X-Real-IP`, or `X-Forwarded-For` merely because
the header is present. Trusting an attacker-controlled forwarded header breaks
API-key IP restrictions and collapses ingress-rejection aggregation and
invalid-auth limiting onto incorrect clients.

For a CDN or load balancer deployment:

1. Firewall the origin so only the CDN or load balancer can reach Caddy.
2. Configure Caddy with the provider's current egress CIDRs as trusted proxies
   and derive the client identity from Caddy's parsed client IP.
3. Configure Sub2API `server.trusted_proxies` with the Caddy address or private
   subnet, not public Internet ranges. Keep the security compatibility override
   disabled, or explicitly sanitize all of its consumed headers at the edge.

Example Caddy global options (replace the documentation networks with the
provider's maintained egress ranges):

```caddyfile
{
	servers {
		trusted_proxies static 192.0.2.0/24 2001:db8:1234::/48
		trusted_proxies_strict
		client_ip_headers CF-Connecting-IP X-Forwarded-For
	}
}

api.example.com {
	reverse_proxy localhost:8080 {
		header_up X-Real-IP {client_ip}
		header_up X-Forwarded-For {client_ip}
		header_up CF-Connecting-IP {client_ip}
	}
}
```

This configuration is safe only when direct origin access is blocked. Do not
copy it with example networks, and do not retain `{remote_host}` forwarding
lines behind a CDN: that value is the CDN peer rather than the real client.

## Edge controls

Use the CDN, WAF, load balancer, or host firewall for connection caps,
unauthenticated request rates, header-size limits, bot controls, and volumetric
attack mitigation. Tune the controls to observed traffic; a single universal
rate is not appropriate for every deployment.

At minimum, keep the application port private, terminate TLS at the chosen
edge, and allow origin ingress only from the trusted proxy tier. Application
checks help once a connection reaches Go, but they cannot absorb a TLS flood,
bandwidth saturation, or a distributed volumetric attack.

## Rollout checklist

- Confirm the origin firewall matches the declared trusted-proxy topology.
- Confirm Caddy overwrites, rather than forwards, client-IP headers.
- Set `server.trusted_proxies` only to the proxy addresses that directly call
  Sub2API.
- Verify the live forwarded-IP policy and custom header list; if compatibility
  mode is enabled, verify every consumed header is overwritten or removed.
- Verify an SSE request receives incremental events through every edge layer.
- Verify rejected-request telemetry contains only masked client network prefixes
  and never credentials, authorization headers, or request bodies.
