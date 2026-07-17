package securityaudit

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromptAuditLogAllowlistDoesNotLeakSecrets(t *testing.T) {
	const canary = "PROMPT_AUDIT_CANARY_SECRET_DO_NOT_PERSIST"
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	LogWarn(EventConfigReloadDegraded, map[string]any{
		"status": "degraded", "error_code": "config_reload_failed",
		"error_kind": "Authorization: Bearer " + canary, "token": canary,
		"raw_prompt": canary, "base_url": "https://guard.example.test/path?api_key=" + canary,
	})
	require.NotContains(t, output.String(), canary)
	require.NotContains(t, output.String(), "api_key=")
	require.Contains(t, output.String(), EventConfigReloadDegraded)

	beforeUnknown := output.Len()
	LogWarn("prompt_audit.typo_event", map[string]any{"status": "failed"})
	require.Equal(t, beforeUnknown, output.Len())
	require.Len(t, knownLogEvents, 27)
}
