package securityaudit

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const maxGuardResponseBytes int64 = 256 * 1024

// adminTrustedHostsEnv names an operator-controlled, comma-separated allowlist
// of literal hostnames or IP addresses that may bypass the private-network
// (RFC1918/ULA) destination-class rejection below. It intentionally does NOT
// bypass the categorical blocks in isBlockedAddress (cloud metadata hosts,
// multicast, link-local, documentation/reserved ranges): those destinations
// are never legitimate Guard node targets, whitelisted or not. Entries are
// exact-match only (no wildcards or CIDR) so an admin must name each trusted
// intranet Guard node explicitly. Empty/unset preserves prior behavior.
const adminTrustedHostsEnv = "SUB2API_PROMPT_AUDIT_TRUSTED_HOSTS"

var (
	errRedirectBlocked = errors.New("prompt guard redirect blocked")
	metadataHosts      = map[string]struct{}{
		"metadata": {}, "metadata.google.internal": {}, "metadata.azure.internal": {},
		"instance-data": {}, "instance-data.ec2.internal": {},
	}
	blockedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/128"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("ff00::/8"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
)

type DNSResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type netResolver struct{ resolver *net.Resolver }

func (r netResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return r.resolver.LookupNetIP(ctx, network, host)
}

func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", infraerrors.BadRequest("prompt_audit_invalid_base_url", "审计节点地址无效")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", infraerrors.BadRequest("prompt_audit_invalid_base_url_scheme", "审计节点仅支持 HTTP(S)")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", infraerrors.BadRequest("prompt_audit_unsafe_base_url", "审计节点地址不能包含凭据、查询参数或片段")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", infraerrors.BadRequest("prompt_audit_invalid_base_url", "审计节点地址无效")
	}
	if _, blocked := metadataHosts[host]; blocked || strings.HasSuffix(host, ".metadata.google.internal") {
		return "", infraerrors.BadRequest("prompt_audit_unsafe_base_url", "审计节点地址不在允许范围")
	}
	trustedHost := isAdminTrustedHost(host)
	allowPrivate := isExplicitPrivateHost(host) || trustedHost
	if addr, err := netip.ParseAddr(host); err == nil {
		if isBlockedAddress(addr) {
			return "", infraerrors.BadRequest("prompt_audit_unsafe_base_url", "审计节点地址不在允许范围")
		}
		// Loopback literals remain available for local Guard nodes and tests.
		// RFC1918/ULA literals are rejected unless the exact address was named
		// by an administrator via adminTrustedHostsEnv; use a hostname or IP
		// allowlist entry instead of loosening this check for every endpoint.
		if addr.IsPrivate() && !trustedHost {
			return "", infraerrors.BadRequest("prompt_audit_unsafe_base_url", "审计节点地址不在允许范围")
		}
		allowPrivate = addr.IsLoopback() || (trustedHost && addr.IsPrivate())
	}
	if parsed.Scheme == "http" && !allowPrivate {
		return "", infraerrors.BadRequest("prompt_audit_https_required", "公网审计节点必须使用 HTTPS")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if strings.EqualFold(path, "/v1") {
		path = ""
	}
	parsed.Path = path
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func ChatCompletionsURL(base string) (string, error) {
	normalized, err := NormalizeBaseURL(base)
	if err != nil {
		return "", err
	}
	return normalized + "/v1/chat/completions", nil
}

func ModelsURL(base string) (string, error) {
	normalized, err := NormalizeBaseURL(base)
	if err != nil {
		return "", err
	}
	return normalized + "/v1/models", nil
}

func NewSecureHTTPClient(endpoint ActiveEndpoint) (*http.Client, error) {
	normalized, err := NormalizeBaseURL(endpoint.BaseURL)
	if err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(normalized)
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	trustedHost := isAdminTrustedHost(host)
	allowPrivate := isExplicitPrivateHost(host)
	allowResolvedPrivate := trustedHost
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		allowPrivate = addr.IsLoopback()
		allowResolvedPrivate = trustedHost && addr.IsPrivate()
	}
	resolver := netResolver{resolver: net.DefaultResolver}
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// Do not inherit HTTP(S)_PROXY. A proxy would move the actual destination
		// dial outside secureDialContext and bypass this module's DNS/IP validation.
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: time.Duration(endpoint.TimeoutMS) * time.Millisecond,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	transport.DialContext = secureDialContext(dialer, resolver, allowPrivate, allowResolvedPrivate)
	timeout := time.Duration(endpoint.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = DefaultTimeoutMS * time.Millisecond
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errRedirectBlocked
		},
	}, nil
}

// secureDialContext gates outbound Guard dials by resolved address class.
// allowPrivate is the pre-existing "localhost family" trust: it only ever
// permits loopback-resolved addresses, guarding against a hosts/DNS mapping
// that resolves "localhost" to an RFC1918 address. allowResolvedPrivate is
// the new, narrower admin-allowlist trust (see adminTrustedHostsEnv): it
// permits private and loopback resolved addresses for that one named
// destination, but never bypasses isBlockedAddress's categorical blocks
// (cloud metadata, multicast, link-local, documentation/reserved ranges).
func secureDialContext(dialer *net.Dialer, resolver DNSResolver, allowPrivate bool, allowResolvedPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("prompt guard dial address invalid")
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("prompt guard dns unavailable")
		}
		var lastErr error
		for _, addr := range addresses {
			if isBlockedAddress(addr) {
				lastErr = fmt.Errorf("prompt guard resolved address blocked")
				continue
			}
			switch {
			case allowPrivate:
				// localhost / *.localhost may only resolve to loopback. A hosts or
				// DNS mapping from localhost to RFC1918 must not become an SSRF pivot.
				if !addr.IsLoopback() {
					lastErr = fmt.Errorf("prompt guard resolved address blocked")
					continue
				}
			case allowResolvedPrivate:
				// Admin explicitly named this destination as trusted; private and
				// loopback resolved addresses are both acceptable for it.
			default:
				if addr.IsPrivate() || addr.IsLoopback() {
					lastErr = fmt.Errorf("prompt guard resolved address blocked")
					continue
				}
			}
			if !addr.IsGlobalUnicast() && !addr.IsLoopback() {
				lastErr = fmt.Errorf("prompt guard resolved address blocked")
				continue
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("prompt guard no allowed resolved address")
		}
		return nil, lastErr
	}
}

// adminTrustedHosts parses adminTrustedHostsEnv into a lookup set, normalizing
// IP literals to their canonical netip.Addr string form and hostnames to
// lowercase with any trailing dot trimmed (matching the normalization already
// applied to the host before these helpers are consulted).
func adminTrustedHosts() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv(adminTrustedHostsEnv))
	if raw == "" {
		return nil
	}
	set := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		entry := strings.ToLower(strings.TrimSpace(part))
		if entry == "" {
			continue
		}
		if addr, err := netip.ParseAddr(entry); err == nil {
			set[addr.String()] = struct{}{}
			continue
		}
		set[strings.TrimSuffix(entry, ".")] = struct{}{}
	}
	return set
}

// isAdminTrustedHost reports whether host (already lowercased/dot-trimmed by
// the caller) was explicitly named by an administrator via adminTrustedHostsEnv.
func isAdminTrustedHost(host string) bool {
	trusted := adminTrustedHosts()
	if len(trusted) == 0 {
		return false
	}
	if _, ok := trusted[host]; ok {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		_, ok := trusted[addr.String()]
		return ok
	}
	return false
}

func isExplicitPrivateHost(host string) bool {
	// Only the localhost name family is trusted for private/loopback dials.
	// A bare "*.local" suffix is too broad (mDNS/intranet names) and would
	// re-open RFC1918 SSRF after literal private IPs were rejected.
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func isBlockedAddress(addr netip.Addr) bool {
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
