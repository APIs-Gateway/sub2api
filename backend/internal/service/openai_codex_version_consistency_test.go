//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexVersionConstantsStayInSync(t *testing.T) {
	require.Equal(t, codexCLIVersion, openAICodexProbeVersion,
		"Codex manifest and usage probes must use the same client version")
	require.Contains(t, codexCLIUserAgent, "codex_cli_rs/"+codexCLIVersion,
		"Codex CLI user agent must embed the configured client version")
	// 浏览器 UA 兜底路径的默认身份必须是 CLI 身份而非 TUI 身份：上游按 originator 分桶调度
	// 容量，TUI 身份命中降载桶会被回 server_is_overloaded 并触发账号冷却。
	require.Equal(t, codexCLIUserAgent, DefaultOpenAICodexUserAgent,
		"browser fallback identity must default to the CLI identity to avoid the upstream load-shed bucket")
}
