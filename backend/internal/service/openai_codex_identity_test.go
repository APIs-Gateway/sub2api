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
		{name: "load-shed originator repaired from ua is normalized to cli identity", originator: "codex_cli_rs", userAgent: "codex-tui/0.144.1", wantOriginator: "codex_cli_rs", wantUserAgent: "codex_cli_rs/0.144.1"},
		{name: "trailer restores overridden load-shed identity then normalizes it", originator: "codex_cli_rs", userAgent: "cccc/0.142.0 (Linux) (codex-tui; 0.142.0)", wantOriginator: "codex_cli_rs", wantUserAgent: "codex_cli_rs/0.142.0 (Linux)"},
		{name: "load-shed identity with full fingerprint drops trailing client-id group", originator: "codex-tui", userAgent: "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)", wantOriginator: "codex_cli_rs", wantUserAgent: "codex_cli_rs/0.140.2 (Mac OS X 14.0; arm64) iTerm"},
		{name: "load-shed identity without trailing client-id group keeps os/arch group", originator: "codex-tui", userAgent: "codex-tui/0.144.1 (Ubuntu 22.4.0; x86_64)", wantOriginator: "codex_cli_rs", wantUserAgent: "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64)"},
		{name: "non load-shed official identity is preserved and paired", originator: "opencode", userAgent: "codex_vscode/1.2.3 (Ubuntu 22.4.0; x86_64) vscode (codex_vscode; 1.2.3)", wantOriginator: "codex_vscode", wantUserAgent: "codex_vscode/1.2.3 (Ubuntu 22.4.0; x86_64) vscode (codex_vscode; 1.2.3)"},
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

// 归一化必须是幂等的：重复收口（如透传路径先后经过多次改写）不得反复裁剪 UA。
func TestEnforceCodexIdentityHeaders_LoadShedNormalizationIsIdempotent(t *testing.T) {
	headers := make(http.Header)
	headers.Set("originator", "codex-tui")
	headers.Set("user-agent", "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)")

	enforceCodexIdentityHeaders(headers)
	first := headers.Get("user-agent")
	require.Equal(t, "codex_cli_rs", headers.Get("originator"))

	enforceCodexIdentityHeaders(headers)

	require.Equal(t, first, headers.Get("user-agent"))
	require.Equal(t, "codex_cli_rs", headers.Get("originator"))
}

func TestNormalizeCodexLoadShedIdentity(t *testing.T) {
	tests := []struct {
		name           string
		originator     string
		userAgent      string
		wantOriginator string
		wantUserAgent  string
	}{
		{name: "load-shed originator normalized and trailing client-id group dropped", originator: "codex-tui", userAgent: "codex-tui/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.144.1)", wantOriginator: "codex_cli_rs", wantUserAgent: "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color"},
		{name: "load-shed originator without version segment only swaps prefix", originator: "codex-tui", userAgent: "codex-tui", wantOriginator: "codex_cli_rs", wantUserAgent: "codex-tui"},
		{name: "healthy identity returned unchanged", originator: "codex_cli_rs", userAgent: "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color", wantOriginator: "codex_cli_rs", wantUserAgent: "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color"},
		{name: "other official identity returned unchanged", originator: "codex_vscode", userAgent: "codex_vscode/1.0.0 (Ubuntu 22.4.0; x86_64) vscode (codex_vscode; 1.0.0)", wantOriginator: "codex_vscode", wantUserAgent: "codex_vscode/1.0.0 (Ubuntu 22.4.0; x86_64) vscode (codex_vscode; 1.0.0)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOriginator, gotUA := normalizeCodexLoadShedIdentity(tt.originator, tt.userAgent)
			require.Equal(t, tt.wantOriginator, gotOriginator)
			require.Equal(t, tt.wantUserAgent, gotUA)
		})
	}
}
