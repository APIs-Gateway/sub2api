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

// A settings save is a PATCH at the API boundary: fields that are not present
// in the request must retain their persisted values instead of being replaced
// by the Go zero value used while decoding UpdateSettingsRequest.
func TestSettingHandlerUpdateSettingsPartialPayloadKeepsUnsentSystemSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	repo.values[service.SettingKeySiteName] = "Example Gateway"
	repo.values[service.SettingKeySMTPHost] = "smtp.example.com"
	repo.values[service.SettingKeyTurnstileEnabled] = "true"
	repo.values[service.SettingKeyDefaultConcurrency] = "7"
	repo.values[service.SettingKeyRiskControlEnabled] = "false"
	repo.values[service.SettingKeyAuthSourceDefaultEmailBalance] = "12.50000000"
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	body, err := json.Marshal(map[string]any{"risk_control_enabled": true})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyRiskControlEnabled])
	require.Equal(t, "Example Gateway", repo.values[service.SettingKeySiteName])
	require.Equal(t, "smtp.example.com", repo.values[service.SettingKeySMTPHost])
	require.Equal(t, "true", repo.values[service.SettingKeyTurnstileEnabled])
	require.Equal(t, "7", repo.values[service.SettingKeyDefaultConcurrency])
	require.Equal(t, "12.50000000", repo.values[service.SettingKeyAuthSourceDefaultEmailBalance])
}

// Explicit zero values are different from absent values. The JSON field
// presence is the authority for the update, so false, zero, and an empty
// string must all be written rather than treated as omissions.
func TestSettingHandlerUpdateSettingsPartialPayloadWritesExplicitZeroValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	repo.values[service.SettingKeyRiskControlEnabled] = "true"
	repo.values[service.SettingKeySMTPPort] = "587"
	repo.values[service.SettingKeySiteName] = "Example Gateway"
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	body, err := json.Marshal(map[string]any{
		"risk_control_enabled": false,
		"smtp_port":            0,
		"site_name":            "",
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "false", repo.values[service.SettingKeyRiskControlEnabled])
	require.Equal(t, "0", repo.values[service.SettingKeySMTPPort])
	require.Equal(t, "", repo.values[service.SettingKeySiteName])
}

func TestSettingHandlerUpdateSettingsPartialPayloadWritesExplicitEmptySMTPFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	repo.values[service.SettingKeySMTPHost] = "smtp.example.com"
	repo.values[service.SettingKeySMTPPort] = "587"
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	body, err := json.Marshal(map[string]any{
		"smtp_host": "",
		"smtp_port": 0,
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "", repo.values[service.SettingKeySMTPHost])
	require.Equal(t, "0", repo.values[service.SettingKeySMTPPort])
}
