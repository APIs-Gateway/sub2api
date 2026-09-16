package securityaudit

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type staticResolver struct{ addresses []netip.Addr }

func (r staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, nil
}

func TestNormalizeBaseURLSecurity(t *testing.T) {
	t.Setenv(adminTrustedHostsEnv, "")
	allowed := []string{"https://guard.example.com", "https://guard.example.com/v1", "http://127.0.0.1:8080"}
	for _, raw := range allowed {
		_, err := NormalizeBaseURL(raw)
		require.NoError(t, err, raw)
	}
	blocked := []string{
		"ftp://guard.example.com", "http://guard.example.com", "https://user:pass@guard.example.com",
		"https://guard.example.com?q=secret", "https://guard.example.com/#fragment", "http://169.254.169.254",
		"https://metadata.google.internal", "https://0.0.0.0", "https://224.0.0.1", "https://192.0.2.1",
		"https://[::]", "https://[fe80::1]", "https://[ff02::1]", "https://[2001:db8::1]",
		"http://10.0.0.8:8080", "http://192.168.1.10:8080", "https://172.16.0.5",
	}
	for _, raw := range blocked {
		_, err := NormalizeBaseURL(raw)
		require.Error(t, err, raw)
	}
	url, err := ChatCompletionsURL("https://guard.example.com/v1")
	require.NoError(t, err)
	require.Equal(t, "https://guard.example.com/v1/chat/completions", url)
}

// TestNormalizeBaseURLAdminTrustedHostsAllowScopedPrivateBypass verifies the
// adminTrustedHostsEnv allowlist added for issue #883: it must only unblock
// the exact literal addresses/hostnames an operator names, must not turn into
// a blanket private-network bypass, and must never reach categorical blocks
// like cloud metadata hosts even when someone puts them on the list.
func TestNormalizeBaseURLAdminTrustedHostsAllowScopedPrivateBypass(t *testing.T) {
	t.Setenv(adminTrustedHostsEnv, "10.0.0.8, guard.intranet.corp ,169.254.169.254")

	allowed := []string{"http://10.0.0.8:8080", "http://guard.intranet.corp", "https://guard.intranet.corp/v1"}
	for _, raw := range allowed {
		_, err := NormalizeBaseURL(raw)
		require.NoError(t, err, raw)
	}

	blocked := []string{
		// Not on the allowlist: private-network rejection is unaffected for
		// every other destination.
		"http://10.0.0.9:8080",
		"https://172.16.0.5",
		"http://other-intranet.local",
		// Listed by an admin, but a cloud metadata host is never allowed to
		// bypass the categorical block regardless of the allowlist.
		"http://169.254.169.254",
	}
	for _, raw := range blocked {
		_, err := NormalizeBaseURL(raw)
		require.Error(t, err, raw)
	}
}

func TestNormalizeBaseURLAdminTrustedHostsEmptyPreservesExistingBehavior(t *testing.T) {
	t.Setenv(adminTrustedHostsEnv, "")
	_, err := NormalizeBaseURL("http://10.0.0.8:8080")
	require.Error(t, err)
}

// TestNormalizeBaseURLAdminTrustedHostsIPv6AndCaseInsensitive covers the
// remaining boundary cases called out for issue #883: an IPv6 ULA literal
// (with a port) and a mixed-case hostname allowlist entry.
func TestNormalizeBaseURLAdminTrustedHostsIPv6AndCaseInsensitive(t *testing.T) {
	// Allowlist entries name bare addresses/hostnames, matching how
	// url.URL.Hostname() strips brackets from a bracketed IPv6 literal.
	t.Setenv(adminTrustedHostsEnv, "fd00::1, Guard.Intranet.Corp")

	_, err := NormalizeBaseURL("https://[fd00::2]:8443")
	require.Error(t, err, "unlisted IPv6 ULA address must remain blocked")

	_, err = NormalizeBaseURL("http://[fd00::1]:8443")
	require.NoError(t, err, "listed IPv6 ULA literal must be allowed")

	_, err = NormalizeBaseURL("http://guard.intranet.corp")
	require.NoError(t, err, "hostname matching must be case-insensitive")
}

func TestSecureDialRejectsDNSRebindingToPrivateAddress(t *testing.T) {
	dial := secureDialContext(nil, staticResolver{addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, false, false)
	_, err := dial(context.Background(), "tcp", "guard.example.com:443")
	require.Error(t, err)
}

// TestSecureDialAdminTrustedHostRejectsCategoricalBlockRegardless verifies
// allowResolvedPrivate never bypasses isBlockedAddress: a resolved cloud
// metadata-range address must still be blocked even for an admin-trusted
// destination.
func TestSecureDialAdminTrustedHostRejectsCategoricalBlockRegardless(t *testing.T) {
	dial := secureDialContext(nil, staticResolver{addresses: []netip.Addr{netip.MustParseAddr("169.254.169.254")}}, false, true)
	_, err := dial(context.Background(), "tcp", "guard.intranet.corp:443")
	require.Error(t, err)
}

// TestSecureDialAdminTrustedHostAllowsResolvedPrivateAddress verifies that,
// unlike the default mode (which rejects any loopback/private resolved
// address) and the localhost-only mode (which requires loopback), the
// allowResolvedPrivate admin-allowlist mode permits dialing a resolved
// address in the private/loopback class.
func TestSecureDialAdminTrustedHostAllowsResolvedPrivateAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)

	dialer := &net.Dialer{Timeout: time.Second}
	dial := secureDialContext(dialer, staticResolver{addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, false, true)
	conn, err := dial(context.Background(), "tcp", net.JoinHostPort("guard.intranet.corp", port))
	require.NoError(t, err)
	_ = conn.Close()
}

func TestSecureHTTPClientDoesNotBypassDestinationValidationThroughEnvironmentProxy(t *testing.T) {
	client, err := NewSecureHTTPClient(ActiveEndpoint{BaseURL: "https://guard.example.com", TimeoutMS: 1000})
	require.NoError(t, err)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, transport.Proxy)
}

func TestOpenAICompatibleScannerRequestContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, DefaultGuardModel, payload["model"])
		require.Equal(t, float64(0), payload["temperature"])
		require.Equal(t, float64(64), payload["max_tokens"])
		require.Equal(t, float64(42), payload["seed"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Safety: Safe\nCategories: None"}}]}`))
	}))
	defer server.Close()
	scanner := NewOpenAICompatibleScanner()
	result, err := scanner.Scan(context.Background(), ActiveEndpoint{ID: "one", BaseURL: server.URL, Model: DefaultGuardModel, Token: "token", TimeoutMS: 1000}, "hello", AllScannerIDs)
	require.NoError(t, err)
	require.Equal(t, EventPass, result.Decision)
}

func TestOpenAICompatibleScannerRejectsRedirectAndOversize(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/other", http.StatusFound)
	}))
	defer redirect.Close()
	_, err := NewOpenAICompatibleScanner().Scan(context.Background(), ActiveEndpoint{ID: "redirect", BaseURL: redirect.URL, Model: DefaultGuardModel, TimeoutMS: 1000}, "hello", AllScannerIDs)
	require.Error(t, err)
	oversize := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxGuardResponseBytes)+1)))
	}))
	defer oversize.Close()
	_, err = NewOpenAICompatibleScanner().Scan(context.Background(), ActiveEndpoint{ID: "large", BaseURL: oversize.URL, Model: DefaultGuardModel, TimeoutMS: 1000}, "hello", AllScannerIDs)
	require.Error(t, err)
}

func TestOpenAICompatibleScannerClassifiesHTTPConnectionAndTimeoutFailures(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{name: "authentication", status: http.StatusUnauthorized, retryable: false},
		{name: "forbidden", status: http.StatusForbidden, retryable: false},
		{name: "rate limited", status: http.StatusTooManyRequests, retryable: true},
		{name: "server failure", status: http.StatusBadGateway, retryable: true},
		{name: "other client error", status: http.StatusBadRequest, retryable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer server.Close()
			_, err := NewOpenAICompatibleScanner().Scan(context.Background(), ActiveEndpoint{ID: "status", BaseURL: server.URL, Model: DefaultGuardModel, TimeoutMS: 1000}, "hello", AllScannerIDs)
			var guardErr *GuardError
			require.ErrorAs(t, err, &guardErr)
			require.Equal(t, ErrorCodeUnavailable, guardErr.Code)
			require.Equal(t, tt.status, guardErr.HTTPStatus)
			require.Equal(t, tt.retryable, guardErr.Retryable)
			require.NotContains(t, err.Error(), server.URL)
		})
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	_, err := NewOpenAICompatibleScanner().Scan(context.Background(), ActiveEndpoint{ID: "closed", BaseURL: closedURL, Model: DefaultGuardModel, TimeoutMS: 100}, "hello", AllScannerIDs)
	var connectionErr *GuardError
	require.ErrorAs(t, err, &connectionErr)
	require.True(t, connectionErr.Retryable)

	timeout := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer timeout.Close()
	_, err = NewOpenAICompatibleScanner().Scan(context.Background(), ActiveEndpoint{ID: "timeout", BaseURL: timeout.URL, Model: DefaultGuardModel, TimeoutMS: 20}, "hello", AllScannerIDs)
	var timeoutErr *GuardError
	require.ErrorAs(t, err, &timeoutErr)
	require.True(t, timeoutErr.Retryable)
	require.True(t, timeoutErr.Timeout)
}
