# Edge and HTTP ingress contract

Sub2API supports long-lived SSE and WebSocket requests. Apply request controls
at the edge without applying a response write deadline: a write timeout can
terminate a healthy generation or stream.

This document describes the checked-in `deploy/Caddyfile` as a **direct-to-Caddy
baseline**. It is not a drop-in CDN configuration.

## Limits and streaming

The current Caddy baseline enforces a 100 MiB request-body limit. The application
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
Sub2API accepts forwarded addresses only when `server.trusted_proxies` names the
addresses or CIDRs that connect directly to the application. Leave that setting
empty when the application is directly public.

Do not trust `CF-Connecting-IP`, `X-Real-IP`, or `X-Forwarded-For` merely because
the header is present. Trusting an attacker-controlled forwarded header breaks
API-key IP restrictions and collapses ingress-rejection aggregation and
invalid-auth limiting onto incorrect clients.

For a CDN or load balancer deployment:

1. Firewall the origin so only the CDN or load balancer can reach Caddy.
2. Configure Caddy with the provider's current egress CIDRs as trusted proxies
   and derive the client identity from Caddy's parsed client IP.
3. Configure Sub2API `server.trusted_proxies` with the Caddy address or private
   subnet, not public Internet ranges.

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
- Verify an SSE request receives incremental events through every edge layer.
- Verify rejected-request telemetry contains only masked client network prefixes
  and never credentials, authorization headers, or request bodies.
