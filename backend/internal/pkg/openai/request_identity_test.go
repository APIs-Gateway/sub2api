package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPairCodexClientIdentity(t *testing.T) {
	tests := []struct {
		name, ua, wantOriginator, wantUA string
		wantOK                           bool
	}{
		{"cli prefix", "codex_cli_rs/0.144.1", "codex_cli_rs", "codex_cli_rs/0.144.1", true},
		{"tui prefix", "codex-tui/0.140.2 (Mac OS X; arm64) (codex-tui; 0.140.2)", "codex-tui", "codex-tui/0.140.2 (Mac OS X; arm64) (codex-tui; 0.140.2)", true},
		{"desktop preserves case", "Codex Desktop/1.2.3", "Codex Desktop", "Codex Desktop/1.2.3", true},
		{"override restores trailer", "cccc/0.142.0 (Ubuntu; x86_64) (codex-tui; 0.142.0)", "codex-tui", "codex-tui/0.142.0 (Ubuntu; x86_64) (codex-tui; 0.142.0)", true},
		{"normalizes exact originator", "CODEX_CLI_RS/1.0.0", "codex_cli_rs", "codex_cli_rs/1.0.0", true},
		{"rejects trailer slash", "foo/1.0 (Codex Desktop/2; 1.0)", "", "", false},
		{"rejects control bytes", "Codex \x01evil/1.0.0", "", "", false},
		{"rejects non ascii", "Codex " + string([]byte{0xc3, 0xa9}) + "vil/1.0.0", "", "", false},
		{"rejects long prefix", "Codex " + strings.Repeat("a", 80) + "/1.0.0", "", "", false},
		{"rejects third party", "luna/1.0.0", "", "", false},
		{"rejects missing slash", "curl", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originator, pairedUA, ok := PairCodexClientIdentity(tt.ua)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantOriginator, originator)
			require.Equal(t, tt.wantUA, pairedUA)
		})
	}
}

func TestCodexOfficialClientIdentification(t *testing.T) {
	require.True(t, IsBrowserUserAgent("Mozilla/5.0"))
	require.False(t, IsBrowserUserAgent(" "))
	require.True(t, IsCodexCLIRequest("codex_cli_rs/0.144.1"))
	require.False(t, IsCodexCLIRequest("curl/8.0"))

	require.True(t, IsCodexOfficialClientRequestStrict("codex-tui/0.144.1"))
	require.False(t, IsCodexOfficialClientRequestStrict("Mozilla/5.0 codex-tui/0.144.1"))
	require.True(t, IsCodexOfficialClientRequest("Mozilla/5.0 codex-tui/0.144.1"))
	require.True(t, IsCodexOfficialClientRequest("cccc/0.144.1 (Linux) (codex-tui; 0.144.1)"))
	require.False(t, IsCodexOfficialClientRequest("curl/8.0 (codex-tui"))

	require.True(t, IsCodexOfficialClientOriginator("codex-tui"))
	require.False(t, IsCodexOfficialClientOriginator("codex_tui_evil"))
	require.True(t, IsCodexOfficialClientByHeaders("curl/8.0", "Codex Desktop"))

	version, ok := ParseCodexEngineVersion("codex-tui/0.144.1 (Linux)")
	require.True(t, ok)
	require.Equal(t, "0.144.1", version)
	_, ok = ParseCodexEngineVersion("codex-tui/not-a-version")
	require.False(t, ok)
	_, ok = ParseCodexEngineVersion("curl")
	require.False(t, ok)
}
