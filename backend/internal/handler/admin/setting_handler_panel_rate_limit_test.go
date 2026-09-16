//go:build unit

package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingHandlerPanelRateLimitSettingsRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	handler := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/panel-rate-limit", bytes.NewBufferString(`{"enabled":true,"user_rpm":120,"heavy_rpm":30,"exempt_admin":false,"public_ip_rpm":60}`))
	updateContext.Request.Header.Set("Content-Type", "application/json")
	handler.UpdatePanelRateLimitSettings(updateContext)

	require.Equal(t, http.StatusOK, updateRecorder.Code)
	require.Contains(t, repo.values, service.SettingKeyPanelRateLimitSettings)
	require.Contains(t, updateRecorder.Body.String(), `"user_rpm":120`)
	require.Contains(t, updateRecorder.Body.String(), `"exempt_admin":false`)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/panel-rate-limit", nil)
	handler.GetPanelRateLimitSettings(getContext)

	require.Equal(t, http.StatusOK, getRecorder.Code)
	require.Contains(t, getRecorder.Body.String(), `"heavy_rpm":30`)
	require.Contains(t, getRecorder.Body.String(), `"public_ip_rpm":60`)
}

func TestSettingHandlerPanelRateLimitSettingsRejectsInvalidRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	handler := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/panel-rate-limit", bytes.NewBufferString(`{"enabled":true,"user_rpm":-1}`))
	context.Request.Header.Set("Content-Type", "application/json")
	handler.UpdatePanelRateLimitSettings(context)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.NotContains(t, repo.values, service.SettingKeyPanelRateLimitSettings)
}
