package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnforceCodexIdentityHeaders(t *testing.T) {
	const tuiUA = "codex-tui/0.140.2 (Mac OS X; arm64) (codex-tui; 0.140.2)"
	tests := []struct {
		name, originator, userAgent, version, wantOriginator, wantUA, wantVersion string
	}{
		{"repairs mismatch", "codex_cli_rs", tuiUA, "", "codex-tui", tuiUA, ""},
		{"falls back third party", "opencode", "luna/1.0.0", "", "codex_cli_rs", codexCLIUserAgent, ""},
		{"falls back missing ua", "codex_vscode", "", "", "codex_cli_rs", codexCLIUserAgent, ""},
		{"upgrades old version", "codex_cli_rs", "codex_cli_rs/0.125.0", "0.125.0", "codex_cli_rs", "codex_cli_rs/0.125.0", codexCLIVersion},
		{"keeps new version", "codex_cli_rs", "codex_cli_rs/0.145.0", "0.145.0", "codex_cli_rs", "codex_cli_rs/0.145.0", "0.145.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := make(http.Header)
			h.Set("originator", tt.originator)
			if tt.userAgent != "" {
				h.Set("user-agent", tt.userAgent)
			}
			if tt.version != "" {
				h.Set("version", tt.version)
			}
			enforceCodexIdentityHeaders(h)
			require.Equal(t, tt.wantOriginator, h.Get("originator"))
			require.Equal(t, tt.wantUA, h.Get("user-agent"))
			require.Equal(t, tt.wantVersion, h.Get("version"))
		})
	}
}

func TestEnforceCodexIdentityHeaders_NoOriginatorIsNoop(t *testing.T) {
	h := make(http.Header)
	h.Set("user-agent", "luna/1.0.0")
	enforceCodexIdentityHeaders(h)
	require.Empty(t, h.Get("originator"))
	require.Equal(t, "luna/1.0.0", h.Get("user-agent"))
}
