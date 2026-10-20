package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingHandler_ClaudeCodeClientVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name       string
		initial    map[string]string
		body       string
		status     int
		wantManual string
		wantAuto   string
		wantSynced string
	}{
		{"invalid version rejected", nil, `{"claude_code_client_version":"2.1.x"}`, http.StatusBadRequest, "", "", ""},
		{"below baseline rejected", nil, `{"claude_code_client_version":"1.0.0"}`, http.StatusBadRequest, "", "", ""},
		{"valid version trimmed and saved", map[string]string{service.SettingKeyClaudeCodeClientVersionSynced: "2.1.281"},
			`{"claude_code_client_version":" 2.1.280 ","claude_code_version_auto_sync_enabled":false}`, http.StatusOK, "2.1.280", "false", "2.1.281"},
		{"omitted fields keep previous values", map[string]string{
			service.SettingKeyClaudeCodeClientVersion:          "2.1.280",
			service.SettingKeyClaudeCodeClientVersionSynced:    "2.1.281",
			service.SettingKeyClaudeCodeVersionAutoSyncEnabled: "false",
		}, `{}`, http.StatusOK, "2.1.280", "false", "2.1.281"},
		{"empty clears manual pin", map[string]string{service.SettingKeyClaudeCodeClientVersion: "2.1.280"},
			`{"claude_code_client_version":""}`, http.StatusOK, "", "true", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &settingHandlerRepoStub{values: tt.initial}
			svc := service.NewSettingService(repo, &config.Config{})
			handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewBufferString(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			handler.UpdateSettings(c)
			require.Equal(t, tt.status, rec.Code, rec.Body.String())
			if tt.status != http.StatusOK {
				require.Empty(t, repo.lastUpdates)
				return
			}
			require.Equal(t, tt.wantManual, repo.values[service.SettingKeyClaudeCodeClientVersion])
			require.Equal(t, tt.wantAuto, repo.values[service.SettingKeyClaudeCodeVersionAutoSyncEnabled])
			require.NotContains(t, repo.lastUpdates, service.SettingKeyClaudeCodeClientVersionSynced,
				"同步值只能由同步任务写入")

			var body struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, tt.wantManual, body.Data["claude_code_client_version"])
			require.Equal(t, tt.wantSynced, body.Data["claude_code_client_version_synced"])
			require.Equal(t, tt.wantAuto == "true", body.Data["claude_code_version_auto_sync_enabled"])
		})
	}
}

func TestSettingsAuditTracksClaudeCodeVersionFields(t *testing.T) {
	before := &service.SystemSettings{ClaudeCodeVersionAutoSyncEnabled: true}
	after := &service.SystemSettings{ClaudeCodeClientVersion: "2.1.280", ClaudeCodeVersionAutoSyncEnabled: false}
	changed := diffSettings(before, after, nil, nil, UpdateSettingsRequest{})
	require.Contains(t, changed, "claude_code_client_version")
	require.Contains(t, changed, "claude_code_version_auto_sync_enabled")
}
