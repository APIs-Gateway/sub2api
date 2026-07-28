package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnforceCodexIdentityHeaders(t *testing.T) {
	tests := []struct {
		name           string
		originator     string
		userAgent      string
		version        string
		wantOriginator string
		wantUserAgent  string
		wantVersion    string
	}{
		{name: "official user agent repairs originator", originator: "codex_cli_rs", userAgent: "codex-tui/0.144.1", wantOriginator: "codex-tui", wantUserAgent: "codex-tui/0.144.1"},
		{name: "trailer restores overridden identity", originator: "codex_cli_rs", userAgent: "cccc/0.142.0 (Linux) (codex-tui; 0.142.0)", wantOriginator: "codex-tui", wantUserAgent: "codex-tui/0.142.0 (Linux) (codex-tui; 0.142.0)"},
		{name: "third party falls back", originator: "opencode", userAgent: "curl/8.0", wantOriginator: "codex_cli_rs", wantUserAgent: codexCLIUserAgent},
		{name: "old version upgrades", originator: "codex_cli_rs", userAgent: codexCLIUserAgent, version: "0.137.0", wantOriginator: "codex_cli_rs", wantUserAgent: codexCLIUserAgent, wantVersion: codexCLIVersion},
		{name: "new version remains", originator: "codex_cli_rs", userAgent: codexCLIUserAgent, version: "0.144.1", wantOriginator: "codex_cli_rs", wantUserAgent: codexCLIUserAgent, wantVersion: "0.144.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("originator", tt.originator)
			headers.Set("user-agent", tt.userAgent)
			headers.Set("version", tt.version)

			enforceCodexIdentityHeaders(headers)

			require.Equal(t, tt.wantOriginator, headers.Get("originator"))
			require.Equal(t, tt.wantUserAgent, headers.Get("user-agent"))
			require.Equal(t, tt.wantVersion, headers.Get("version"))
		})
	}
}

func TestEnforceCodexIdentityHeadersWithoutOriginatorIsNoop(t *testing.T) {
	headers := http.Header{"User-Agent": []string{"curl/8.0"}}
	enforceCodexIdentityHeaders(headers)
	require.Empty(t, headers.Get("originator"))
	require.Equal(t, "curl/8.0", headers.Get("user-agent"))
}

func TestEnsureCodexIdentityHeadersNilHeadersIsNoop(t *testing.T) {
	require.NotPanics(t, func() {
		ensureCodexIdentityHeaders(nil)
	})
}

func TestEnsureCodexIdentityHeadersPreservesCallerIdentity(t *testing.T) {
	headers := make(http.Header)
	headers.Set("User-Agent", "caller/1.0")
	headers.Set("Originator", "caller-origin")
	headers.Set("Version", "caller-version")

	ensureCodexIdentityHeaders(headers)

	require.Equal(t, "caller/1.0", headers.Get("User-Agent"))
	require.Equal(t, "caller-origin", headers.Get("Originator"))
	require.Equal(t, "caller-version", headers.Get("Version"))
	require.Equal(t, "responses=experimental", headers.Get("OpenAI-Beta"))
}

func TestApplyOpenAICodexProbeHeaders(t *testing.T) {
	headers := make(http.Header)

	applyOpenAICodexProbeHeaders(headers)

	require.Equal(t, codexCLIUserAgent, headers.Get("User-Agent"))
	require.Equal(t, "codex_cli_rs", headers.Get("Originator"))
	require.Equal(t, codexCLIVersion, headers.Get("Version"))
	require.Equal(t, "responses=experimental", headers.Get("OpenAI-Beta"))
	require.NotEmpty(t, headers.Get("X-Codex-Window-ID"))
}
