//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexVersionConstantsStayInSync(t *testing.T) {
	require.Equal(t, codexCLIVersion, openAICodexProbeVersion,
		"Codex manifest and usage probes must use the same client version")
	require.Contains(t, codexCLIUserAgent, "codex_cli_rs/"+codexCLIVersion,
		"Codex CLI user agent must embed the configured client version")
	require.True(t, strings.Contains(DefaultOpenAICodexUserAgent, "codex-tui/"+codexCLIVersion),
		"browser fallback user agent must embed the configured client version")
	require.True(t, strings.Contains(DefaultOpenAICodexUserAgent, "(codex-tui; "+codexCLIVersion+")"),
		"browser fallback terminal metadata must embed the configured client version")
}
