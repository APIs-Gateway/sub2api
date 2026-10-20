package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnforceCodexIdentityRejectsInvalidUserAgentBytes(t *testing.T) {
	for _, raw := range []string{
		"\ncodex-tui/0.200.1 (Windows)",
		"codex-tui/0.200.1 (Windows)\n",
		"codex-tui/0.200.1 bad\x00",
		"codex-tui/0.200.1 bad\x7f",
		"codex-tui/0.200.1 bad\x01",
	} {
		headers := make(http.Header)
		headers.Set("originator", "codex-tui")
		headers.Set("user-agent", raw)

		enforceCodexIdentityHeaders(headers)

		require.Equal(t, "codex_cli_rs", headers.Get("originator"))
		require.Equal(t, codexCLIUserAgent, headers.Get("user-agent"))
	}
}
